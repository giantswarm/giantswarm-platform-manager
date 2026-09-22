package installations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// The fixture's tree: an installation whose extras/agent-platform lists the
// two registration directories among another owner's.
const (
	fixtureRegistering = "heron"
	fixtureLakesideMCs = "fleet/lakeside-management-clusters"
)

// The fixture's registration files.
const (
	fileA = "a.yaml"
	fileC = "c.yaml"
)

var registeringInstallation = Installation{Name: fixtureRegistering, Repositories: Repositories{Configs: "fleet/lakeside-configs", ManagementClusters: fixtureLakesideMCs}}

// treeOf is a Reader over files by repository:path; a path it does not know
// is gh.ErrNotFound, the way the GitHub client answers.
func treeOf(files map[string]string) Reader {
	return func(_ context.Context, repository, path string) (string, error) {
		if data, ok := files[repository+":"+path]; ok {
			return data, nil
		}
		return "", fmt.Errorf("%w: %s:%s", gh.ErrNotFound, repository, path)
	}
}

func registrationFiles(tree string, servers, clients map[string]string) map[string]string {
	prefix := fixtureLakesideMCs + ":" + AgentPlatformExtrasPath(fixtureRegistering) + "/"
	files := map[string]string{prefix + "kustomization.yaml": tree}
	list := func(dir string, entries map[string]string) {
		names := make([]string, 0, len(entries))
		for name, content := range entries {
			names = append(names, name)
			files[prefix+dir+"/"+name] = content
		}
		// A kustomization in a fixed order: the registrations' order is the person's.
		var k strings.Builder
		k.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n")
		for _, n := range sorted(names) {
			k.WriteString("  - " + n + "\n")
		}
		files[prefix+dir+"/kustomization.yaml"] = k.String()
	}
	if servers != nil {
		list(mcpServersDir, servers)
	}
	if clients != nil {
		list(mcpClientsDir, clients)
	}
	return files
}

func sorted(names []string) []string {
	out := append([]string{}, names...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

const (
	treeWithRegistrations = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main\n  - ./secrets\n  - ./agents\n  - ./mcpservers\n  - mcpclients/\n"
	treeWithoutThem       = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main\n  - ./secrets\n"

	exchangeServer = `apiVersion: muster.giantswarm.io/v1alpha1
kind: MCPServer
metadata:
  name: pond-mcp-timescale
  namespace: agent-platform
spec:
  type: streamable-http
  url: https://mcp-timescale.pond.lakeside.example/mcp
  auth:
    type: oauth
    forwardToken: false
    tokenExchange:
      enabled: true
      dexTokenEndpoint: https://dex.pond.lakeside.example/token
      connectorId: giantswarm-heron
`
	forwardServer = `apiVersion: muster.giantswarm.io/v1alpha1
kind: MCPServer
metadata:
  name: reeds-mcp-docs
  namespace: agent-platform
spec:
  type: streamable-http
  url: https://mcp-docs.reeds.lakeside.example/mcp
  auth:
    type: oauth
    forwardToken: true
    requiredAudiences: [dex-k8s-authenticator]
`
	oauthServer = `apiVersion: muster.giantswarm.io/v1alpha1
kind: MCPServer
metadata:
  name: github
  namespace: agent-platform
spec:
  type: streamable-http
  url: https://api.githubcopilot.com/mcp/
  auth:
    type: oauth
    forwardToken: false
    authorizationServer:
      issuer: https://github.com/login/oauth
      grantScope: subject
`
	plainServer = "apiVersion: muster.giantswarm.io/v1alpha1\nkind: MCPServer\nmetadata:\n  name: gazelle-mcp-runbooks\nspec:\n  url: http://mcp-runbooks.mcp-runbooks.svc:8080/mcp\n  auth:\n    type: none\n"

	hostedClient = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: hosted-agent-runtime\n  namespace: agent-platform\ndata:\n  redirectURIs: |\n    https://agents.runtime.example/identities/oauth2/callback/a\n\n    https://agents.runtime.example/identities/oauth2/callback/b\n"
)

// The registrations on record are the objects the two directories'
// kustomizations list, in their order, where the tree lists the directories:
// each server with muster's auth mode read off its spec, each client with its
// redirect URIs; a document of another kind among them is skipped.
func TestReadRegistered(t *testing.T) {
	files := registrationFiles(treeWithRegistrations,
		map[string]string{"a-exchange.yaml": exchangeServer, "b-forward.yaml": forwardServer, "c-oauth.yaml": oauthServer, "d-plain.yaml": plainServer + "---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: beside\n"},
		map[string]string{"runtime.yaml": hostedClient})
	reg, err := readRegistered(context.Background(), treeOf(files), registeringInstallation)
	if err != nil {
		t.Fatal(err)
	}
	want := []RegisteredServer{
		{Name: "pond-mcp-timescale", URL: "https://mcp-timescale.pond.lakeside.example/mcp", Auth: authExchange, DexTokenEndpoint: "https://dex.pond.lakeside.example/token"}, // #nosec G101 -- an endpoint URL, not a credential
		{Name: "reeds-mcp-docs", URL: "https://mcp-docs.reeds.lakeside.example/mcp", Auth: authForward},
		{Name: "github", URL: "https://api.githubcopilot.com/mcp/", Auth: authOAuth},
		{Name: "gazelle-mcp-runbooks", URL: "http://mcp-runbooks.mcp-runbooks.svc:8080/mcp", Auth: authNone},
	}
	if len(reg.Servers) != len(want) {
		t.Fatalf("servers: %+v", reg.Servers)
	}
	for i, s := range want {
		if reg.Servers[i] != s {
			t.Errorf("server %d: %+v, want %+v", i, reg.Servers[i], s)
		}
	}
	if len(reg.Clients) != 1 || reg.Clients[0].Name != "hosted-agent-runtime" || len(reg.Clients[0].RedirectURIs) != 2 ||
		reg.Clients[0].RedirectURIs[1] != "https://agents.runtime.example/identities/oauth2/callback/b" {
		t.Errorf("clients: %+v", reg.Clients)
	}
	facts := Report{Registered: &reg}.Facts()
	if facts["mcpServers"] == nil || facts["mcpClients"] == nil {
		t.Errorf("facts: %v", facts)
	}
}

// A tree that lists neither directory, and an installation without the tree,
// register nothing — empty lists, never nil, so the schema's arrays hold —
// and read nothing beneath.
func TestReadRegisteredNothing(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no registration directory": registrationFiles(treeWithoutThem, map[string]string{fileA: exchangeServer}, nil),
		"no tree":                   {},
	} {
		t.Run(name, func(t *testing.T) {
			reg, err := readRegistered(context.Background(), treeOf(files), registeringInstallation)
			if err != nil {
				t.Fatal(err)
			}
			if reg.Servers == nil || reg.Clients == nil || len(reg.Servers) != 0 || len(reg.Clients) != 0 {
				t.Errorf("%+v", reg)
			}
		})
	}
}

// A directory the tree lists without its kustomization, a server without a
// URL and a client without redirect URIs are errors naming the file: the
// fact's error, which the report carries.
func TestReadRegisteredRefusals(t *testing.T) {
	prefix := AgentPlatformExtrasPath(fixtureRegistering) + "/"
	noURL := strings.Replace(exchangeServer, "  url: https://mcp-timescale.pond.lakeside.example/mcp\n", "", 1)
	noURIs := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: hosted-agent-runtime\ndata:\n  other: x\n"
	plainURI := strings.Replace(hostedClient, "https://agents.runtime.example/identities/oauth2/callback/b", "http://agents.runtime.example/callback", 1)
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no directory kustomization", func() map[string]string {
			f := registrationFiles(treeWithRegistrations, map[string]string{fileA: exchangeServer}, map[string]string{fileC: hostedClient})
			delete(f, fixtureLakesideMCs+":"+prefix+mcpServersDir+"/kustomization.yaml")
			return f
		}(), prefix + mcpServersDir + "/kustomization.yaml"},
		{"a server without a url", registrationFiles(treeWithRegistrations, map[string]string{fileA: noURL}, nil), prefix + mcpServersDir + "/a.yaml: MCPServer \"pond-mcp-timescale\": metadata.name and spec.url are required"},
		{"a client without redirect URIs", registrationFiles(treeWithRegistrations, map[string]string{}, map[string]string{fileC: noURIs}), prefix + mcpClientsDir + "/c.yaml: ConfigMap \"hosted-agent-runtime\": metadata.name and data.redirectURIs"},
		{"a client with a plain-http redirect URI", registrationFiles(treeWithRegistrations, map[string]string{}, map[string]string{fileC: plainURI}), "is not HTTPS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readRegistered(context.Background(), treeOf(tc.files), registeringInstallation)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %v, want it to name %q", err, tc.want)
			}
			if tc.name == "no directory kustomization" && !errors.Is(err, gh.ErrNotFound) {
				t.Errorf("a missing kustomization keeps GitHub's not found in the chain: %v", err)
			}
		})
	}
}
