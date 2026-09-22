// Package tools is the MCP surface: behind muster the tools appear as
// x_giantswarm-platform-manager_<tool>. get_info reports who the call runs as
// and what the manager can do; every write tool goes through the framework in
// write.go, which refuses mode apply before any tool runs.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/approvals"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
)

// ToolPrefix is the MCPServer name muster registers this server under.
const ToolPrefix = "giantswarm-platform-manager"

// ToolGetInfo is the tool to call first.
const ToolGetInfo = "get_info"

// ToolListInstallations is the registry read: every installation with, per
// capability, its state, inputs on record and last action.
const ToolListInstallations = "list_installations"

// The capability writes (dry run today; commit follows) and the Action reads.
const (
	ToolEnableCapability    = "enable_capability"
	ToolReconcileCapability = "reconcile_capability"
	ToolGetAction           = "get_action"
	ToolListActions         = "list_actions"
)

// ToolVerifyCapability is the read-only verify of one installation × capability.
const ToolVerifyCapability = "verify_capability"

// PlannedTools are the extension points not registered yet.
func PlannedTools() []string { return []string{} }

// Deps are what the tools run with. The server holds no token of its own:
// every GitHub call runs with the caller's.
type Deps struct {
	Version string
	// GitHubAPIURL is the API base URL the caller's calls go to (empty:
	// api.github.com; the fake in tests).
	GitHubAPIURL string
	// Files is the read cache every caller's client reads repository files
	// through: one tree per repository, validated as the caller once per
	// call, the blobs by SHA shared across calls and callers. nil is one
	// with gh.DefaultFreshness.
	Files *gh.Files
	// AuthorizationServer is the issuer identity muster pins for this server
	// (the App giantswarm-platform-manager's), reported by get_info; empty
	// when the server runs without OAuth.
	AuthorizationServer string
	// Approvals is where the reviews of an action go: klaus-gateway's
	// team-review endpoint, the team, its channel, the notice channel and the
	// projected token file. Without a gateway URL mode commit is refused:
	// nothing is committed that no one can approve. ApprovalsHTTP is the
	// client the requests go through (tests); nil is one with a timeout.
	Approvals     approvals.Config
	ApprovalsHTTP *http.Client
	// Registry names the installations catalog and the hub installation
	// list_installations reads the registry from, as the caller.
	Registry installations.Sources
	// Actions reads and writes the Action records on the hub; nil when the
	// manager runs without the hub's API server, and get_action,
	// list_actions and mode commit say so.
	Actions actions.Store
	// Remote opens the git remote a commit lands on, with the caller's
	// token: GitHub in production (GitHubRemote), gitops-commit's Fake in
	// tests. nil refuses mode commit naming the configuration.
	Remote RemoteFactory
	// Probes sends verify_capability's anonymous HTTP probes; nil is a
	// client with a timeout that does not follow redirects.
	Probes *http.Client
	// Live validates the forwarded tokens of the live path and opens the
	// loop-back sessions verify_installation reads through; nil when the
	// live path is not configured, and get_info says so.
	Live *live.Client
	// ResyncInterval is how old an Action's picture of GitHub may be before
	// a read of the record reads its pull requests and markers again, as
	// the reader; zero is DefaultResyncInterval.
	ResyncInterval time.Duration
	Log            *slog.Logger
}

// Definition is a capability definition as get_info reports it: the name,
// what it enables, the JSON schema of its inputs and the consistency features
// the verify marks — read from the registry (installations.Capabilities) and
// the embedded definitions/<name>/.
type Definition struct {
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	InputSchema json.RawMessage       `json:"inputSchema"`
	Features    []definitions.Feature `json:"features"`
}

// definitionList reads every registered definition with its schema and
// features; a definition whose data is missing from the embedded FS is an
// error, never left out.
func definitionList() ([]Definition, error) {
	caps := installations.Capabilities()
	out := make([]Definition, 0, len(caps))
	for _, c := range caps {
		schema, err := c.Schema()
		if err != nil {
			return nil, fmt.Errorf("definition %s: %w", c.Name, err)
		}
		feats, err := definitions.Features(c.Name)
		if err != nil {
			return nil, fmt.Errorf("definition %s: %w", c.Name, err)
		}
		out = append(out, Definition{Name: c.Name, Description: c.Description, InputSchema: schema, Features: feats})
	}
	return out, nil
}

// Tools is the tool set: get_info and the writes registered through the
// framework before MCPServer builds the server.
type Tools struct {
	d      Deps
	writes []WriteTool
	// approvals is the gateway client, nil without a gateway URL.
	approvals *approvals.Client
}

// New builds the tool set.
func New(d Deps) *Tools {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Files == nil {
		d.Files = gh.NewFiles(gh.DefaultFreshness)
	}
	t := &Tools{d: d}
	if d.Approvals.Configured() {
		t.approvals = approvals.New(d.Approvals, d.ApprovalsHTTP)
	}
	t.AddWrite(t.enableCapabilityTool())
	t.AddWrite(t.reconcileCapabilityTool())
	return t
}

// AddWrite registers a write tool through the framework: dryRun and mode are
// the framework's, apply is refused before the tool runs.
func (t *Tools) AddWrite(wt WriteTool) { t.writes = append(t.writes, wt) }

// person is the caller's GitHub client for one call: their token, the
// shared read cache.
func (t *Tools) person(token string) (*gh.Client, error) {
	return gh.AsPerson(t.d.Files, t.d.GitHubAPIURL, token)
}

// MCPServer registers every tool on a new MCP server.
func (t *Tools) MCPServer() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(ToolPrefix, t.d.Version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions("Giant Swarm's platform manager: enables, reconciles and verifies platform capabilities on installations as the person calling. Call get_info first: it reports who you are to this server (the GitHub login of the token muster put on the call — your own authorization of the App giantswarm-platform-manager), the capability definitions and their input schemas, the write modes and the approval channel. list_installations reads the installations registry and every installation's repositories with your token, now, and answers the state of each capability per installation. enable_capability and reconcile_capability with dryRun: true render an installation (or a set) through the capability's definition and answer the plan: files, pull requests in dependency order, generated secrets by name, Dex clients, the secrets you supply, customer actions and probes. get_action and list_actions read the Action records on the hub. An action in mode commit asks the capability-owning team's approval through a Team review in Slack: approve_action and deny_action are its buttons (called as the clicking member; the actor cannot approve their own action), merge_action merges the approved pull requests as the actor once their checks are green and moves the action to rolling out. verify_capability compares one installation against the definition — the repositories' files against the render from the inputs on record, the anonymous probes — grouped into features with one mark each; its live dimensions are verify_installation's, the tool of the second registration giantswarm-platform-manager-live, which muster forwards your own token to. Every write tool takes dryRun and mode; the only write mode is commit — a pull request to the installation's GitOps repository opened as you — and apply is refused: every target of this manager is GitOps-owned."),
	)
	s.AddTool(mcp.NewTool(ToolGetInfo,
		mcp.WithDescription("Read-only. Answers who you are to this server, the capability definitions with their input schemas, the write modes and tools, the approval channel, where the Action records live and the live registration. Call first."),
		mcp.WithReadOnlyHintAnnotation(true),
	), t.getInfo)
	s.AddTool(listInstallationsTool(), t.listInstallations)
	s.AddTool(verifyCapabilityTool(), t.verifyCapability)
	t.registerActionTools(s)
	t.registerApprovalTools(s)
	for _, wt := range t.writes {
		registerWrite(s, wt)
	}
	return s
}

// Info is get_info's result.
type Info struct {
	Version    string `json:"version"`
	ToolPrefix string `json:"toolPrefix"`
	// Caller is the person the bearer belongs to; null without OAuth.
	Caller       *identity.Identity `json:"caller"`
	Auth         AuthInfo           `json:"auth"`
	GitHub       GitHubInfo         `json:"github"`
	Definitions  []Definition       `json:"definitions"`
	Capabilities Capabilities       `json:"capabilities"`
	Approvals    ApprovalsInfo      `json:"approvals"`
	Registry     RegistryConfig     `json:"registry"`
	Actions      ActionsInfo        `json:"actions"`
	Live         LiveInfo           `json:"live"`
	PlannedTools []string           `json:"plannedTools"`
}

// RegistryConfig is where list_installations reads the registry from.
type RegistryConfig struct {
	Catalog installations.Location `json:"catalog"`
	Hub     string                 `json:"hub"`
	// Configured says whether the hub is set; without it list_installations
	// refuses, and says so.
	Configured bool `json:"configured"`
}

// Authentication modes get_info reports.
const (
	// AuthModeBearer: the person's GitHub user token is the bearer of every
	// call, verified with GET /user.
	AuthModeBearer = "bearer"
	// AuthModeNone: the server runs without OAuth; nothing acts as a person.
	AuthModeNone = "none"
)

// AuthInfo is how this call was authenticated.
type AuthInfo struct {
	Mode string `json:"mode"`
	// AuthorizationServer is the issuer identity muster pins for this server:
	// the App giantswarm-platform-manager's.
	AuthorizationServer string `json:"authorizationServer,omitempty"`
	// Reason says why there is no caller.
	Reason string `json:"reason,omitempty"`
}

// GitHubInfo is the API the caller's calls go to.
type GitHubInfo struct {
	APIURL string `json:"apiUrl"`
}

// Capabilities are the write modes: commit is the one that exists, apply is
// refused for every write tool, and WriteTools names the tools that take them.
type Capabilities struct {
	Commit       bool     `json:"commit"`
	Apply        bool     `json:"apply"`
	Modes        []string `json:"modes"`
	ApplyRefused bool     `json:"applyRefused"`
	WriteTools   []string `json:"writeTools"`
}

// ApprovalsInfo is the approval channel as configured.
type ApprovalsInfo struct {
	// Configured says whether reviews are posted at all (a gateway URL is
	// set); without it mode commit is refused.
	Configured    bool   `json:"configured"`
	GatewayURL    string `json:"gatewayUrl,omitempty"`
	Team          string `json:"team,omitempty"`
	Channel       string `json:"channel,omitempty"`
	NoticeChannel string `json:"noticeChannel,omitempty"`
}

func (t *Tools) getInfo(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	names := make([]string, 0, len(t.writes))
	for _, wt := range t.writes {
		names = append(names, wt.Name)
	}
	defs, err := definitionList()
	if err != nil {
		return result(nil, err)
	}
	info := Info{
		Version:      t.d.Version,
		ToolPrefix:   ToolPrefix,
		GitHub:       GitHubInfo{APIURL: apiURL(t.d.GitHubAPIURL)},
		Definitions:  defs,
		Capabilities: Capabilities{Commit: true, Modes: []string{string(ModeCommit)}, ApplyRefused: true, WriteTools: names},
		Approvals:    ApprovalsInfo{Configured: t.d.Approvals.Configured(), GatewayURL: t.d.Approvals.GatewayURL, Team: t.d.Approvals.Team, Channel: t.d.Approvals.Channel, NoticeChannel: t.d.Approvals.NoticeChannel},
		Registry:     RegistryConfig{Catalog: t.d.Registry.Catalog, Hub: t.d.Registry.Hub, Configured: t.d.Registry.Hub != ""},
		Actions:      t.actionsInfo(),
		Live:         t.liveInfo(),
		PlannedTools: PlannedTools(),
	}
	if id, ok := identity.FromContext(ctx); ok {
		info.Caller = id
		info.Auth = AuthInfo{Mode: AuthModeBearer, AuthorizationServer: t.d.AuthorizationServer}
	} else {
		info.Auth = AuthInfo{Mode: AuthModeNone, Reason: "the request carried no verified bearer: the server runs without OAuth (oauth.enabled), nothing acts as a person"}
	}
	return result(info, nil)
}

func apiURL(u string) string {
	if u == "" {
		return "https://api.github.com"
	}
	return u
}

// AnswerLimit bounds one tool's answer: the bytes of its JSON text, 1 MiB.
// An answer reaches a person through muster's call_tool, which wraps the
// text in one more JSON document, and through the gateway in front of
// muster, which buffers a response whole and drops one above 2 MiB with an
// error naming neither the size nor the limit; the two encodings add about a
// seventh to the text. An answer above the limit is refused here instead,
// with its size, the limit and the way to ask for less.
const AnswerLimit = 1 << 20

// result renders v as the tool's JSON text, or err as a tool error.
func result(v any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return Answer(v), nil
}

// Answer renders v as the tool's JSON text; an answer above AnswerLimit is
// the tool's refusal, naming the size and the limit.
func Answer(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("encode result: " + err.Error())
	}
	if len(b) > AnswerLimit {
		return mcp.NewToolResultError(fmt.Sprintf("the answer is %d bytes (%s), above the %d bytes (%s) one answer may carry through muster and its gateway: ask for less — a smaller set (%s), one installation (%s), no file content (%s: false)",
			len(b), size(len(b)), AnswerLimit, size(AnswerLimit), ArgInstallations, ArgInstallation, ArgContent))
	}
	return mcp.NewToolResultText(string(b))
}

// size is n bytes in KiB or MiB, one decimal.
func size(n int) string {
	const kib, mib = 1 << 10, 1 << 20
	if n >= mib {
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/kib)
}
