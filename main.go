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
	"strconv"
	"syscall"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/approvals"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
	"github.com/giantswarm/giantswarm-platform-manager/internal/server"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

type options struct {
	listen, mcpPath, githubAPIURL string

	approvalsURL, approvalsTeam, approvalsChannel, approvalsNoticeChannel, approvalsTokenFile string

	registryRepository, registryPath, hub string

	actionsNamespace string

	oauthEnabled                           bool
	oauthBaseURL, oauthAuthorizationServer string

	liveEnabled, liveAllowPrivateIPJWKS                                                                                     bool
	livePath, liveIssuer, liveAudience, liveJWKSURL, liveCAFile, musterURL, liveKubernetesFamily, liveKubernetesInstanceArg string
}

func parseFlags(args []string) (*options, error) {
	o := &options{}
	f := flag.NewFlagSet("giantswarm-platform-manager", flag.ContinueOnError)
	f.StringVar(&o.listen, "listen", envOr("LISTEN", ":8080"), "Listen address (LISTEN)")
	f.StringVar(&o.mcpPath, "mcp-path", envOr("MCP_PATH", "/mcp"), "MCP endpoint path (MCP_PATH)")
	f.StringVar(&o.githubAPIURL, "github-api-url", envOr("GITHUB_API_URL", ""), "GitHub API base URL the caller's calls go to; empty is api.github.com (GITHUB_API_URL)")
	f.StringVar(&o.approvalsURL, "approvals-url", envOr("APPROVALS_URL", ""), "klaus-gateway's base URL for the team-review endpoint an action's review goes to; empty refuses mode commit and get_info says so (APPROVALS_URL)")
	f.StringVar(&o.approvalsTeam, "approvals-team", envOr("APPROVALS_TEAM", ""), "The team whose members decide, as the gateway names it (APPROVALS_TEAM)")
	f.StringVar(&o.approvalsChannel, "approvals-channel", envOr("APPROVALS_CHANNEL", ""), "The capability-owning team's channel the reviews land in (APPROVALS_CHANNEL)")
	f.StringVar(&o.approvalsNoticeChannel, "approvals-notice-channel", envOr("APPROVALS_NOTICE_CHANNEL", ""), "The channel told of a review that targets a customer installation, without buttons (APPROVALS_NOTICE_CHANNEL)")
	f.StringVar(&o.approvalsTokenFile, "approvals-token-file", envOr("APPROVALS_TOKEN_FILE", ""), "File with the projected ServiceAccount token, in the gateway's audience, the review requests carry (APPROVALS_TOKEN_FILE)")
	f.StringVar(&o.registryRepository, "registry-repository", envOr("REGISTRY_REPOSITORY", "giantswarm/github"), "Repository (owner/repo) holding the installations catalog, read as the caller (REGISTRY_REPOSITORY)")
	f.StringVar(&o.registryPath, "registry-path", envOr("REGISTRY_PATH", "catalog/installations.yaml"), "Path of the installations catalog in the registry repository (REGISTRY_PATH)")
	f.StringVar(&o.hub, "hub", envOr("HUB_INSTALLATION", ""), "Name of the hub installation this manager runs on: its management-clusters repository holds the Dev Portal's app-config the registry also reads, and the hub side of every capability lands in its repositories (HUB_INSTALLATION)")
	f.StringVar(&o.actionsNamespace, "actions-namespace", envOr("ACTIONS_NAMESPACE", ""), "Namespace on the hub the Action records live in, read with the pod's ServiceAccount; empty runs without the records and get_action/list_actions say so (ACTIONS_NAMESPACE)")
	f.BoolVar(&o.oauthEnabled, "enable-oauth", envBool("OAUTH_ENABLED"), "Require a GitHub user token as the bearer of every MCP request — behind muster the person's own, through the App giantswarm-platform-manager — verified with GET /user; the caller and the token travel with the request (OAUTH_ENABLED)")
	f.StringVar(&o.oauthBaseURL, "oauth-base-url", envOr("OAUTH_BASE_URL", ""), "URL muster reaches this server at, without the MCP path: the resource of its OAuth protected-resource metadata (OAUTH_BASE_URL)")
	f.StringVar(&o.oauthAuthorizationServer, "oauth-authorization-server", envOr("OAUTH_AUTHORIZATION_SERVER", server.DefaultAuthorizationServer), "Issuer identity of the authorization server muster pins for this server, named in the protected-resource metadata (OAUTH_AUTHORIZATION_SERVER)")
	f.BoolVar(&o.liveEnabled, "enable-live", envBool("LIVE_ENABLED"), "Serve the live surface: a second MCP endpoint muster forwards the person's own ID token to (MCPServer auth.forwardToken), with verify_installation reading an installation through muster as the person (LIVE_ENABLED)")
	f.StringVar(&o.livePath, "live-path", envOr("LIVE_PATH", "/mcp/live"), "Path of the live MCP endpoint (LIVE_PATH)")
	f.StringVar(&o.liveIssuer, "live-issuer", envOr("LIVE_ISSUER", ""), "Issuer of the forwarded tokens: the platform identity provider (LIVE_ISSUER)")
	f.StringVar(&o.liveAudience, "live-audience", envOr("LIVE_AUDIENCE", ""), "Audience every forwarded token carries: the platform's OAuth client (LIVE_AUDIENCE)")
	f.StringVar(&o.liveJWKSURL, "live-jwks-url", envOr("LIVE_JWKS_URL", ""), "The issuer's key set; empty reads it from the issuer's discovery document (LIVE_JWKS_URL)")
	f.BoolVar(&o.liveAllowPrivateIPJWKS, "live-allow-private-ip-jwks", envBool("LIVE_ALLOW_PRIVATE_IP_JWKS"), "Let the issuer or its key set resolve to a private address, an in-cluster identity provider (LIVE_ALLOW_PRIVATE_IP_JWKS)")
	f.StringVar(&o.liveCAFile, "live-ca-file", envOr("LIVE_CA_FILE", ""), "PEM bundle the issuer's certificate chains to; empty is the system trust (LIVE_CA_FILE)")
	f.StringVar(&o.musterURL, "muster-url", envOr("MUSTER_URL", ""), "muster's own MCP endpoint as reached from the pod: where a live read loops back to with the person's token (MUSTER_URL)")
	f.StringVar(&o.liveKubernetesFamily, "live-kubernetes-family", envOr("LIVE_KUBERNETES_FAMILY", "kubernetes"), "The muster family, or singleton server, the installations' kubernetes tools are aggregated under: x_<family>_get, _list, _logs (LIVE_KUBERNETES_FAMILY)")
	f.StringVar(&o.liveKubernetesInstanceArg, "live-kubernetes-instance-arg", envOr("LIVE_KUBERNETES_INSTANCE_ARG", "management_cluster"), "The family's argument that selects the installation; empty for a singleton server (LIVE_KUBERNETES_INSTANCE_ARG)")
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
	deps := tools.Deps{Version: version.String(), GitHubAPIURL: o.githubAPIURL, Log: log, Remote: tools.GitHubRemote(o.githubAPIURL),
		Approvals: approvals.Config{GatewayURL: o.approvalsURL, Team: o.approvalsTeam, Channel: o.approvalsChannel, NoticeChannel: o.approvalsNoticeChannel, TokenFile: o.approvalsTokenFile},
		Registry:  installations.Sources{Catalog: installations.Location{Repository: o.registryRepository, Path: o.registryPath}, Hub: o.hub}}
	if o.actionsNamespace != "" {
		store, err := actions.InCluster(o.actionsNamespace)
		if err != nil {
			return err
		}
		deps.Actions = store
	}
	if err := deps.Approvals.Validate(); err != nil {
		return err
	}
	cfg := server.Config{Addr: o.listen, MCPPath: o.mcpPath}
	if o.oauthEnabled {
		cfg.OAuth = &server.OAuthConfig{BaseURL: o.oauthBaseURL, AuthorizationServer: o.oauthAuthorizationServer, GitHubAPIURL: o.githubAPIURL}
		deps.AuthorizationServer = o.oauthAuthorizationServer
	}
	if o.liveEnabled {
		lc, err := live.New(live.Config{Path: o.livePath, Issuer: o.liveIssuer, Audience: o.liveAudience, JWKSURL: o.liveJWKSURL, AllowPrivateIPJWKS: o.liveAllowPrivateIPJWKS,
			CAFile: o.liveCAFile, MusterURL: o.musterURL, KubernetesFamily: o.liveKubernetesFamily, KubernetesInstanceArg: o.liveKubernetesInstanceArg, Version: deps.Version}, log)
		if err != nil {
			return err
		}
		defer lc.Close()
		deps.Live = lc
	}
	ts := tools.New(deps)
	if deps.Live != nil {
		cfg.Live = &server.LiveConfig{Path: deps.Live.Path(), Verify: deps.Live.Verify, Server: ts.LiveMCPServer()}
	}
	srv, err := server.New(cfg, ts.MCPServer(), log)
	if err != nil {
		return err
	}
	log.Info("giantswarm-platform-manager starting", "version", deps.Version, "listen", o.listen, "mcp", o.mcpPath,
		"oauth", o.oauthEnabled, "authorizationServer", deps.AuthorizationServer, "live", o.liveEnabled, "approvals", o.approvalsURL != "", "approvalsChannel", o.approvalsChannel,
		"registry", deps.Registry.Catalog.String(), "hub", o.hub, "actionsNamespace", o.actionsNamespace)
	return srv.Run(ctx)
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
