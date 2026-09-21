// Package e2e proves the identity chain against a fake GitHub: muster puts the
// person's GitHub user token — their authorization of the App
// giantswarm-platform-manager — on every call as the bearer; the server
// verifies it with GET /user (once per token, then from its cache), refuses a
// request without a bearer or with one GitHub refuses with a bare 401 and the
// protected-resource challenge, names the caller in get_info, and refuses
// mode apply for every write tool before the tool runs.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/approvals"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
	"github.com/giantswarm/giantswarm-platform-manager/internal/server"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

const (
	alice = "alice"
	// The user tokens muster puts on the calls: alice's is GitHub's, bob's is
	// one GitHub refuses (never authorized, or revoked).
	aliceToken  = "alice-token"
	bobToken    = "bob-token"
	carol       = "carol"
	carolToken  = "carol-token"
	testVersion = "test"
	// baseURL is where muster reaches the server: the resource of the
	// protected-resource metadata (the listener is httptest's).
	baseURL = "http://giantswarm-platform-manager.test:8080"
	// testWrite is a write tool registered through the framework, standing
	// in for the ones the later slices add; installation is its invented
	// target.
	testWrite       = "test_write"
	argInstallation = "installation"
	installation    = "example"
	// actionsNamespace is where the Action records live on the fake hub.
	actionsNamespace = "platform-manager"
	// livePath is where the live surface listens.
	livePath = "/mcp/live"
)

type stack struct {
	ghs *fakeGitHub
	// probes answers the anonymous probes of verify_capability.
	probes *fakeProbes
	srv    *httptest.Server
	// dyn is the fake hub API server the Action records are seeded into.
	dyn dynamic.Interface
	// remote is gitops-commit's in-process remote the commits land on.
	remote *commit.Fake
	// logs is everything the server logged at Info and above.
	logs *syncBuffer
	// committedAs is the caller the test write's Commit ran as.
	committedAs string
	// gateway is the fake klaus-gateway the reviews go to.
	gateway *fakeGateway
	// dex is the platform identity provider of the live path, muster the
	// aggregator its loop-back reaches, inst the installation behind it.
	dex    *fakeDex
	muster *fakeMuster
	inst   *fakeInstallation
	// remoteCalls are the approvals, closes and merges by identity: "<login> <op> <owner/repo>#<n>".
	mu          sync.Mutex
	remoteCalls []string
	// pulls are who merged or closed each pull request of the remote, and
	// when — what the fake GitHub answers for it.
	pulls map[string]pullFacts
}

// syncBuffer is a bytes.Buffer the server's log handler and the test share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newStack(t *testing.T) *stack {
	t.Helper()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logins := map[string]string{aliceToken: alice, carolToken: carol}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(fixtureSA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := &stack{ghs: newFakeGitHub(t, logins), probes: newFakeProbes(t), remote: commit.NewFake(), logs: logs, gateway: newFakeGateway(t),
		dyn: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{actions.GVR: actions.Kind + "List"}),
		dex: newFakeDex(t), inst: newFakeInstallation(), pulls: map[string]pullFacts{}}
	st.ghs.pull = st.pullState
	st.muster = newFakeMuster(t, st.inst, rowan)
	lc, err := live.New(live.Config{Path: livePath, Issuer: st.dex.issuer, Audiences: []string{liveAudience, liveRequiredAudience}, JWKSURL: st.dex.issuer + "/keys", AllowPrivateIPJWKS: true, CAFile: st.dex.caFile,
		MusterURL: st.muster.URL + "/mcp", KubernetesFamily: kubernetesFamily, KubernetesInstanceArg: instanceArg, KubernetesMember: memberTemplate, Version: testVersion}, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lc.Close)
	apiURL := st.ghs.URL + "/api/v3"
	ts := tools.New(tools.Deps{Version: testVersion, GitHubAPIURL: apiURL, AuthorizationServer: server.DefaultAuthorizationServer, Log: log, Live: lc,
		Actions: actions.New(st.dyn, actionsNamespace),
		Probes:  st.probes.client(),
		// Every read follows GitHub: the tests move the remote by hand and
		// read straight after.
		ResyncInterval: time.Nanosecond,
		Remote: func(token string) (commit.Remote, error) {
			return asRemote{Remote: st.remote, login: logins[token], st: st}, nil
		},
		Approvals: approvals.Config{GatewayURL: st.gateway.URL, Team: reviewTeam, Channel: reviewChannel, NoticeChannel: noticeChannel, TokenFile: tokenFile},
		Registry:  registrySources})
	ts.AddWrite(tools.WriteTool{Name: testWrite, Description: "A fixture write.",
		DryRun: func(_ context.Context, args map[string]any) (any, error) {
			return map[string]any{"rendered": true, "args": args}, nil
		},
		Commit: func(ctx context.Context, _ map[string]any) (any, error) {
			st.committedAs = identity.Caller(ctx)
			return map[string]any{"committed": true, "as": st.committedAs}, nil
		}})
	s, err := server.New(server.Config{Addr: "127.0.0.1:0", MCPPath: "/mcp",
		OAuth: &server.OAuthConfig{BaseURL: baseURL, AuthorizationServer: server.DefaultAuthorizationServer, GitHubAPIURL: apiURL},
		Live:  &server.LiveConfig{Path: livePath, Verify: lc.Verify, Server: ts.LiveMCPServer()}}, ts.MCPServer(), log)
	if err != nil {
		t.Fatal(err)
	}
	st.srv = httptest.NewServer(s.Handler())
	t.Cleanup(st.srv.Close)
	// muster knows the persons of the live path by their GitHub grants too:
	// the loop-back to the manager's own registration runs with them.
	st.muster.connect(st.srv.URL+"/mcp", map[string]string{liveAdmin: aliceToken, liveViewer: carolToken})
	return st
}

// mcpClient is a connected MCP client carrying token as the bearer.
func (st *stack) mcpClient(t *testing.T, token string) *client.Client {
	t.Helper()
	return st.clientAt(t, "/mcp", token)
}

// liveClient is a session on the live path with the bearer muster would
// forward: the person's ID token.
func (st *stack) liveClient(t *testing.T, token string) *client.Client {
	t.Helper()
	return st.clientAt(t, livePath, token)
}

func (st *stack) clientAt(t *testing.T, path, token string) *client.Client {
	t.Helper()
	c, err := client.NewStreamableHttpClient(st.srv.URL+path, transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err != nil {
		t.Fatalf("initialize as %q: %v", token, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// call runs tool and returns the text of the result and whether it is an error.
func call(t *testing.T, c *client.Client, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: args}})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var b strings.Builder
	for _, cnt := range res.Content {
		if tc, ok := cnt.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// A request without a bearer is a bare 401 with the RFC 6750 challenge naming
// the protected-resource metadata and no error code; the metadata names the
// pinned authorization server.
func TestNoBearerIsABare401(t *testing.T) {
	st := newStack(t)
	res, err := http.Post(st.srv.URL+"/mcp", "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	challenge := res.Header.Get("WWW-Authenticate")
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(challenge, `resource_metadata="`+baseURL+`/.well-known/oauth-protected-resource/mcp"`) || strings.Contains(challenge, "error=") {
		t.Fatalf("no bearer: %d %q", res.StatusCode, challenge)
	}
	if st.ghs.userCalls.Load() != 0 {
		t.Fatal("GitHub was asked about a request that carried no bearer")
	}
	meta, err := http.Get(st.srv.URL + "/.well-known/oauth-protected-resource/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = meta.Body.Close() }()
	var doc struct {
		Resource string   `json:"resource"`
		Servers  []string `json:"authorization_servers"`
	}
	if err := json.NewDecoder(meta.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Resource != baseURL+"/mcp" || len(doc.Servers) != 1 || doc.Servers[0] != server.DefaultAuthorizationServer {
		t.Fatalf("metadata: %+v", doc)
	}
}

// A bearer GitHub refuses is a 401 with error invalid_token and the sign-in.
func TestRefusedBearerIsInvalidToken(t *testing.T) {
	st := newStack(t)
	req, _ := http.NewRequest(http.MethodPost, st.srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req.Header.Set("Authorization", "Bearer "+bobToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	challenge := res.Header.Get("WWW-Authenticate")
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(challenge, `error="invalid_token"`) || !strings.Contains(challenge, "core_auth_login") {
		t.Fatalf("refused bearer: %d %q", res.StatusCode, challenge)
	}
}

// get_info names the person the bearer belongs to, the pinned authorization
// server, the definitions, the write modes and the approval channel; the
// bearer is verified once and then served from the cache.
func TestGetInfoNamesTheCaller(t *testing.T) {
	st := newStack(t)
	c := st.mcpClient(t, aliceToken)
	text, isErr := call(t, c, tools.ToolGetInfo, nil)
	if isErr {
		t.Fatalf("get_info: %s", text)
	}
	var info tools.Info
	if err := json.Unmarshal([]byte(text), &info); err != nil {
		t.Fatal(err)
	}
	switch {
	case info.Caller == nil || info.Caller.Login != alice || info.Caller.ID != userID(alice):
		t.Fatalf("caller: %+v", info.Caller)
	case info.Auth.Mode != tools.AuthModeBearer || info.Auth.AuthorizationServer != server.DefaultAuthorizationServer:
		t.Fatalf("auth: %+v", info.Auth)
	case !info.Capabilities.Commit || info.Capabilities.Apply || !info.Capabilities.ApplyRefused || strings.Join(info.Capabilities.WriteTools, ",") != strings.Join([]string{tools.ToolEnableCapability, tools.ToolReconcileCapability, testWrite}, ","):
		t.Fatalf("capabilities: %+v", info.Capabilities)
	case len(info.Definitions) == 0:
		t.Fatal("definitions: empty; the registry's definitions are missing from get_info")
	case !definitionListed(t, info.Definitions, installations.AgentPlatform):
		t.Fatalf("definitions: %s missing its schema or features: %+v", installations.AgentPlatform, info.Definitions)
	case !definitionListed(t, info.Definitions, installations.CustomerPortal):
		t.Fatalf("definitions: %s missing its schema or features: %+v", installations.CustomerPortal, info.Definitions)
	case len(info.Definitions) != len(installations.Capabilities()):
		t.Fatalf("definitions: %d listed, the registry has %d", len(info.Definitions), len(installations.Capabilities()))
	case !info.Approvals.Configured || info.Approvals.Channel != reviewChannel || info.Approvals.Team != reviewTeam || info.Approvals.NoticeChannel != noticeChannel:
		t.Fatalf("approvals: %+v", info.Approvals)
	case strings.Join(info.PlannedTools, ",") != strings.Join(tools.PlannedTools(), ","):
		t.Fatalf("planned tools: %v", info.PlannedTools)
	case !info.Actions.Configured || info.Actions.Namespace != actionsNamespace || info.Actions.Group != actions.Group:
		t.Fatalf("actions: %+v", info.Actions)
	}
	if _, isErr := call(t, c, tools.ToolGetInfo, nil); isErr {
		t.Fatal("second get_info failed")
	}
	if n := st.ghs.userCalls.Load(); n != 1 {
		t.Fatalf("GET /user was called %d times for one token, want 1 (the cache)", n)
	}
}

// definitionListed says whether defs carry the definition name with an input
// schema that declares the installation inputs and at least one feature.
func definitionListed(t *testing.T, defs []tools.Definition, name string) bool {
	t.Helper()
	for _, d := range defs {
		if d.Name != name {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(d.InputSchema, &schema); err != nil {
			t.Fatalf("definition %s: input schema: %v", name, err)
		}
		_, hasInstallation := schema.Properties["installation"]
		return hasInstallation && d.Description != "" && len(d.Features) > 0
	}
	return false
}

// The framework refuses mode apply for a write tool before the tool runs,
// requires a mode unless dryRun, and dispatches dryRun and commit — the
// commit as the caller.
func TestWriteFrameworkRefusesApply(t *testing.T) {
	st := newStack(t)
	c := st.mcpClient(t, aliceToken)
	text, isErr := call(t, c, testWrite, map[string]any{tools.ArgMode: "apply", argInstallation: installation})
	if !isErr || !strings.Contains(text, tools.ApplyRefusal) {
		t.Fatalf("mode apply: error %v %q", isErr, text)
	}
	if text, isErr := call(t, c, testWrite, map[string]any{argInstallation: installation}); !isErr || !strings.Contains(text, "mode is required") {
		t.Fatalf("no mode: error %v %q", isErr, text)
	}
	if text, isErr := call(t, c, testWrite, map[string]any{tools.ArgDryRun: true, argInstallation: installation}); isErr || !strings.Contains(text, `"rendered": true`) || strings.Contains(text, "dryRun") {
		t.Fatalf("dry run: error %v %q", isErr, text)
	}
	if text, isErr := call(t, c, testWrite, map[string]any{tools.ArgMode: "commit"}); isErr || !strings.Contains(text, `"as": "alice"`) || st.committedAs != alice {
		t.Fatalf("commit: error %v %q (as %q)", isErr, text, st.committedAs)
	}
}

// The review configuration of the stack: the team, its channel and the
// notice channel, as chart values would name them.
const (
	reviewTeam    = "team-fixture"
	reviewChannel = "C-fixture-team"
	noticeChannel = "C-fixture-notice"
)

func (st *stack) record(login, op string, pr commit.PullRequest) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.remoteCalls = append(st.remoteCalls, fmt.Sprintf("%s %s %s#%d", login, op, pr.Repository, pr.Number))
}

// calls returns the recorded remote calls, in order.
func (st *stack) calls() []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string{}, st.remoteCalls...)
}
