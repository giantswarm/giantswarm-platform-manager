// Command giantswarm-platform-manager is Giant Swarm's installation manager:
// an MCP server behind muster that enables, reconciles and verifies platform
// capabilities on opted-in installations as the person calling it. Every
// write is a pull request to the installation's GitOps repository, opened as
// the person; mode apply is refused for every write tool.
//
// Every flag can also be set through the environment variable named next to
// it; flags win over the environment. The server holds no token of its own:
// behind muster every request carries the person's GitHub user token through
// the App giantswarm-platform-manager, verified with GET /user.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/server"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

type options struct {
	listen, mcpPath, githubAPIURL string

	approvalsURL, approvalsChannel string

	registryRepository, registryPath, hub string

	oauthEnabled                           bool
	oauthBaseURL, oauthAuthorizationServer string
}

func parseFlags(args []string) (*options, error) {
	o := &options{}
	f := flag.NewFlagSet("giantswarm-platform-manager", flag.ContinueOnError)
	f.StringVar(&o.listen, "listen", envOr("LISTEN", ":8080"), "Listen address (LISTEN)")
	f.StringVar(&o.mcpPath, "mcp-path", envOr("MCP_PATH", "/mcp"), "MCP endpoint path (MCP_PATH)")
	f.StringVar(&o.githubAPIURL, "github-api-url", envOr("GITHUB_API_URL", ""), "GitHub API base URL the caller's calls go to; empty is api.github.com (GITHUB_API_URL)")
	f.StringVar(&o.approvalsURL, "approvals-url", envOr("APPROVALS_URL", ""), "klaus-gateway's base URL for the team-review endpoint the asks of a write go to; empty leaves the asks undelivered and get_info says so (APPROVALS_URL)")
	f.StringVar(&o.approvalsChannel, "approvals-channel", envOr("APPROVALS_CHANNEL", ""), "The channel the asks land in, as the gateway names it (APPROVALS_CHANNEL)")
	f.StringVar(&o.registryRepository, "registry-repository", envOr("REGISTRY_REPOSITORY", "giantswarm/github"), "Repository (owner/repo) holding the installations catalog, read as the caller (REGISTRY_REPOSITORY)")
	f.StringVar(&o.registryPath, "registry-path", envOr("REGISTRY_PATH", "catalog/installations.yaml"), "Path of the installations catalog in the registry repository (REGISTRY_PATH)")
	f.StringVar(&o.hub, "hub", envOr("HUB_INSTALLATION", ""), "Name of the hub installation this manager runs on: its management-clusters repository holds the Dev Portal's app-config the registry also reads, and the hub side of every capability lands in its repositories (HUB_INSTALLATION)")
	f.BoolVar(&o.oauthEnabled, "enable-oauth", envBool("OAUTH_ENABLED"), "Require a GitHub user token as the bearer of every MCP request — behind muster the person's own, through the App giantswarm-platform-manager — verified with GET /user; the caller and the token travel with the request (OAUTH_ENABLED)")
	f.StringVar(&o.oauthBaseURL, "oauth-base-url", envOr("OAUTH_BASE_URL", ""), "URL muster reaches this server at, without the MCP path: the resource of its OAuth protected-resource metadata (OAUTH_BASE_URL)")
	f.StringVar(&o.oauthAuthorizationServer, "oauth-authorization-server", envOr("OAUTH_AUTHORIZATION_SERVER", server.DefaultAuthorizationServer), "Issuer identity of the authorization server muster pins for this server, named in the protected-resource metadata (OAUTH_AUTHORIZATION_SERVER)")
	if err := f.Parse(args); err != nil {
		return nil, err
	}
	return o, nil
}

func main() {
	o, err := parseFlags(os.Args[1:])
	if err != nil {
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, o, slog.Default()); err != nil {
		slog.Error("giantswarm-platform-manager failed", "error", err)
		os.Exit(1)
	}
}

// run wires the components and serves until ctx is done.
func run(ctx context.Context, o *options, log *slog.Logger) error {
	deps := tools.Deps{Version: version(), GitHubAPIURL: o.githubAPIURL, Log: log,
		Approvals: tools.Approvals{GatewayURL: o.approvalsURL, Channel: o.approvalsChannel},
		Registry:  installations.Sources{Catalog: installations.Location{Repository: o.registryRepository, Path: o.registryPath}, Hub: o.hub}}
	cfg := server.Config{Addr: o.listen, MCPPath: o.mcpPath}
	if o.oauthEnabled {
		cfg.OAuth = &server.OAuthConfig{BaseURL: o.oauthBaseURL, AuthorizationServer: o.oauthAuthorizationServer, GitHubAPIURL: o.githubAPIURL}
		deps.AuthorizationServer = o.oauthAuthorizationServer
	}
	srv, err := server.New(cfg, tools.New(deps).MCPServer(), log)
	if err != nil {
		return err
	}
	log.Info("giantswarm-platform-manager starting", "version", deps.Version, "listen", o.listen, "mcp", o.mcpPath,
		"oauth", o.oauthEnabled, "authorizationServer", deps.AuthorizationServer, "approvals", o.approvalsURL != "", "approvalsChannel", o.approvalsChannel,
		"registry", deps.Registry.Catalog.String(), "hub", o.hub)
	return srv.Run(ctx)
}

// version is the module version of the build, else the VCS revision — the
// org convention: no -ldflags, debug.ReadBuildInfo() is the source.
func version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	rev, dirty := "", ""
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				dirty = "-dirty"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if rev == "" {
		return "dev"
	}
	return "dev-" + rev + dirty
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string) bool {
	b, err := strconv.ParseBool(os.Getenv(key))
	return err == nil && b
}
