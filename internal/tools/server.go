// Package tools is the MCP surface: behind muster the tools appear as
// x_giantswarm-platform-manager_<tool>. get_info reports who the call runs as
// and what the manager can do; every write tool goes through the framework in
// write.go, which refuses mode apply before any tool runs.
package tools

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
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
	// AuthorizationServer is the issuer identity muster pins for this server
	// (the App giantswarm-platform-manager's), reported by get_info; empty
	// when the server runs without OAuth.
	AuthorizationServer string
	// Approvals is where the asks a write raises go.
	Approvals Approvals
	// Definitions are the capability definitions the manager knows, with
	// their input schemas; empty until the definitions slice lands.
	Definitions []Definition
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
	Log    *slog.Logger
}

// Definition is a capability definition as get_info reports it: the name, what
// it enables and the JSON schema of its inputs.
type Definition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// Approvals is the approval channel configuration: the team-review endpoint
// (klaus-gateway's base URL) the asks go to and the channel they land in.
// Empty leaves the asks undelivered; get_info says so.
type Approvals struct {
	GatewayURL string
	Channel    string
}

// Tools is the tool set: get_info and the writes registered through the
// framework before MCPServer builds the server.
type Tools struct {
	d      Deps
	writes []WriteTool
}

// New builds the tool set.
func New(d Deps) *Tools {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	t := &Tools{d: d}
	t.AddWrite(t.enableCapabilityTool())
	t.AddWrite(t.reconcileCapabilityTool())
	return t
}

// AddWrite registers a write tool through the framework: dryRun and mode are
// the framework's, apply is refused before the tool runs.
func (t *Tools) AddWrite(wt WriteTool) { t.writes = append(t.writes, wt) }

// MCPServer registers every tool on a new MCP server.
func (t *Tools) MCPServer() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer(ToolPrefix, t.d.Version,
		mcpserver.WithToolCapabilities(false),
		mcpserver.WithInstructions("Giant Swarm's installation manager: enables, reconciles and verifies platform capabilities on opted-in installations as the person calling. Call get_info first: it reports who you are to this server (the GitHub login of the token muster put on the call — your own authorization of the App giantswarm-platform-manager), the capability definitions and their input schemas, the write modes and the approval channel. list_installations reads the installations registry and every installation's opt-in declaration with your token, now, and answers the state of each capability per installation. enable_capability and reconcile_capability with dryRun: true render an installation (or a set) through the capability's definition and answer the plan: files, pull requests in dependency order, generated secrets by name, Dex clients, the secrets you supply, customer actions and probes. get_action and list_actions read the Action records on the hub. verify_capability compares one installation against the definition — the repositories' files against the render from the inputs on record, the anonymous probes — grouped into features with one mark each. Every write tool takes dryRun and mode; the only write mode is commit — a pull request to the installation's GitOps repository opened as you — and apply is refused: every target of this manager is GitOps-owned."),
	)
	s.AddTool(mcp.NewTool(ToolGetInfo,
		mcp.WithDescription("Read-only. Report the service version and how this call is authenticated: the caller (the GitHub login and id GET /user answered for the bearer muster put on the call — the person's own user token through the App giantswarm-platform-manager) and the authorization server pinned for it; the capability definitions with their input schemas; the write modes (commit only, apply refused) and the write tools; the approval channel configuration; where the Action records live; the tools still to come. Call first."),
		mcp.WithReadOnlyHintAnnotation(true),
	), t.getInfo)
	s.AddTool(listInstallationsTool(), t.listInstallations)
	s.AddTool(verifyCapabilityTool(), t.verifyCapability)
	t.registerActionTools(s)
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
	// Configured says whether asks are delivered at all (a gateway URL is set).
	Configured bool   `json:"configured"`
	GatewayURL string `json:"gatewayUrl,omitempty"`
	Channel    string `json:"channel,omitempty"`
}

func (t *Tools) getInfo(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	names := make([]string, 0, len(t.writes))
	for _, wt := range t.writes {
		names = append(names, wt.Name)
	}
	info := Info{
		Version:      t.d.Version,
		ToolPrefix:   ToolPrefix,
		GitHub:       GitHubInfo{APIURL: apiURL(t.d.GitHubAPIURL)},
		Definitions:  append([]Definition{}, t.d.Definitions...),
		Capabilities: Capabilities{Commit: true, Modes: []string{string(ModeCommit)}, ApplyRefused: true, WriteTools: names},
		Approvals:    ApprovalsInfo{Configured: t.d.Approvals.GatewayURL != "", GatewayURL: t.d.Approvals.GatewayURL, Channel: t.d.Approvals.Channel},
		Registry:     RegistryConfig{Catalog: t.d.Registry.Catalog, Hub: t.d.Registry.Hub, Configured: t.d.Registry.Hub != ""},
		Actions:      t.actionsInfo(),
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

// result renders v as the tool's JSON text, or err as a tool error.
func result(v any, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("encode result: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
