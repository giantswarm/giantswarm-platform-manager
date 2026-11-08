// Package local runs the manager's own tools inside platformctl, from the
// definitions compiled into this build: `platformctl … --local`. A render
// change built from a checkout is dry-run and verified against an
// installation's repositories before its merge and rollout, as the person
// whose GitHub token the session carries, and every result says it was
// rendered locally (renderedBy.local). The session serves the renders only:
// with no Action records, no approval channel and no git remote, a commit is
// the tools' own refusal.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/aggregator"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

// Options are what a local session renders with.
type Options struct {
	// Token is the person's GitHub token every read runs as (Token finds
	// it); it is never printed.
	Token string
	// GitHubAPIURL is the API the reads go to; empty is api.github.com.
	GitHubAPIURL string
	// Registry is the catalog and hub the deployed manager reads: the
	// registry the plans are rendered against.
	Registry installations.Sources
	// Probes sends verify's anonymous HTTP probes; nil is the tools'
	// default.
	Probes *http.Client
}

// Session is the manager's tools in process, called as one person.
type Session struct {
	c   *client.Client
	ctx func(context.Context) context.Context
}

// Open verifies the token with GitHub (GET /user, as the manager's bearer
// guard does) and starts the tools in process on an initialized MCP
// session.
func Open(ctx context.Context, o Options) (*Session, error) {
	if o.Registry.Hub == "" {
		return nil, errors.New("--local needs the hub the registry is read from: --hub <installation>, or muster in reach so the deployed manager's get_info names it")
	}
	login, id, email, err := gh.User(ctx, o.GitHubAPIURL, o.Token)
	if err != nil {
		return nil, fmt.Errorf("GitHub refused the token --local reads as: %w", err)
	}
	who := &identity.Identity{Login: login, ID: id, Email: email}
	t := tools.New(tools.Deps{Version: version.String(), Local: true, GitHubAPIURL: o.GitHubAPIURL, Registry: o.Registry, Probes: o.Probes,
		Log: slog.New(slog.DiscardHandler)})
	c, err := client.NewInProcessClient(t.MCPServer())
	if err != nil {
		return nil, err
	}
	if err := c.Start(ctx); err != nil {
		return nil, err
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "platformctl", Version: version.String()}
	if _, err := c.Initialize(ctx, req); err != nil {
		_ = c.Close()
		return nil, err
	}
	token := o.Token
	return &Session{c: c, ctx: func(ctx context.Context) context.Context {
		return identity.ContextWithToken(identity.ContextWith(ctx, who), token)
	}}, nil
}

// Close ends the session.
func (s *Session) Close() error { return s.c.Close() }

// Call runs one tool by its plain name (enable_capability,
// verify_capability, …) and returns the JSON document it answered; a
// refusal is the tool's error text.
func (s *Session) Call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := s.c.CallTool(s.ctx(ctx), req)
	if err != nil {
		return nil, err
	}
	text := aggregator.TextOf(res)
	if res.IsError {
		return nil, fmt.Errorf("%s (rendered locally): %s", tool, text)
	}
	return json.RawMessage(text), nil
}

// TokenEnv are the variables Token reads the person's GitHub token from, in
// order, before it asks the gh CLI.
var TokenEnv = []string{"GITHUB_TOKEN", "GH_TOKEN"}

// Token is the person's GitHub token: the first of TokenEnv that is set,
// else `gh auth token`. lookup is os.LookupEnv outside tests, gh the gh
// CLI's path ("" is gh on PATH).
func Token(ctx context.Context, lookup func(string) (string, bool), gh string) (string, error) {
	for _, k := range TokenEnv {
		if v, ok := lookup(k); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
	}
	if gh == "" {
		gh = "gh"
	}
	out, err := exec.CommandContext(ctx, gh, "auth", "token").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("--local reads the repositories as you and needs your GitHub token: set %s, or sign in with `gh auth login`", strings.Join(TokenEnv, " or "))
	}
	return strings.TrimSpace(string(out)), nil
}
