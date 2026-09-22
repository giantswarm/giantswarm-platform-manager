package customerportal

import (
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The portal's app-config, as the fleet's portals write it: what the chart's
// shared-config.yaml offers is included by anchor, the credentials are the
// environment variables the chart sets from the Secrets' values ($$ survives
// the fleet's variable substitution so Backstage sees ${VAR}), and every
// hostname derives from the installation's base domain.
const (
	// pluginKeysMount is where the chart mounts pluginKeys[*] by keyId.
	pluginKeysMount = "/app/plugin-keys/"
	// The Sentry error reporter's settings the fleet's portals share.
	sentryEnvironment = "production"
	sentryReleaseVar  = "$${VERSION}"
	sentrySampleRate  = 0.5
	// The home page's support link, as the fleet's portals label it; the
	// icon is what the schema's read-back of portal.supportUrl selects the
	// entry by.
	supportLabel = "Giant Swarm \n Support"
	supportIcon  = "LiveHelp"
	// grafanaProxy is the proxy endpoint the Grafana plugin's dashboards
	// card reads Grafana's search API through, the plugin's default path;
	// grafanaTokenVar is the chart's environment variable for the
	// service-account token (grafana.apiToken of the user secrets).
	grafanaProxy    = render.PortalGrafanaProxy
	grafanaTokenVar = "GRAFANA_TOKEN"
)

// include is a $include of an anchor of the base's shared-config.yaml.
func include(anchor string) render.Map {
	return render.Map{e("$include", render.PortalSharedInclude(anchor))}
}

// envVar is the reference Backstage resolves from its environment after the
// fleet's substitution.
func envVar(name string) string { return "$${" + name + "}" }

// brokerCredentials is the dexAuthCredentials key of the token broker's
// client; the chart exposes it as AUTH_DEX_MUSTER_BROKER_CLIENT_ID and
// _CLIENT_SECRET.
const brokerCredentials = "musterBroker"

// dexEnv is the chart's environment variable for a Dex client under an
// installation's name (dexAuthCredentials.<name>).
func dexEnv(installation, suffix string) string {
	return envVar("AUTH_DEX_" + strings.ToUpper(installation) + "_" + suffix)
}

// sentryReporter is the error reporter block of the app or the backend.
func sentryReporter(dsnEnv string) render.Map {
	return render.Map{e("sentry", render.Map{
		e("dsn", envVar(dsnEnv)), e("environment", sentryEnvironment),
		e("releaseVersion", sentryReleaseVar), e("tracesSampleRate", sentrySampleRate)})}
}

// appConfig is backstage.appConfig of the app-config ConfigMap.
func (in *Input) appConfig() render.Map {
	m := render.Map{
		e("app", in.appSection()),
		e("organization", render.Map{e("name", in.Portal.Organization)}),
		e("backend", in.backendSection()),
	}
	if in.Plugins.GitHub.Enabled {
		m = append(m, e("integrations", render.Map{e("github", []render.Map{{
			e("host", "github.com"),
			e("apps", []render.Map{{e("$include", "github-app-credentials.yaml")}}),
		}})}))
	}
	m = append(m,
		e("permission", render.Map{e("enabled", true)}),
		e("techdocs", render.Map{e("builder", "local"), e("generator", render.Map{e("runIn", "local")}), e("publisher", render.Map{e("type", "local")})}),
		e("kubernetes", render.Map{
			e("serviceLocatorMethod", render.Map{e("type", "multiTenant")}),
			e("clusterLocatorMethods", []render.Map{{e("type", "config"), e("clusters", in.clusters())}}),
		}),
		e("auth", render.Map{
			e("environment", "production"),
			e("session", render.Map{e("secret", envVar("AUTH_SESSION_SECRET"))}),
			e("providers", in.oidcProviders()),
		}),
	)
	m = append(m, e("grafana", in.grafanaSection()))
	if in.Plugins.Grafana.Enabled {
		m = append(m, e("proxy", render.Map{e("endpoints", render.Map{e(grafanaProxy, render.Map{
			e("target", in.grafanaURL()+"/"), e("headers", render.Map{e("Authorization", "Bearer "+envVar(grafanaTokenVar))})})})}))
	}
	if in.Plugins.Flux.Enabled {
		flux := render.Map{}
		if len(in.Plugins.Flux.GitRepositoryPatterns) > 0 {
			flux = append(flux, e("gitRepositoryPatterns", in.Plugins.Flux.GitRepositoryPatterns))
		}
		m = append(m, e("flux", flux))
	}
	return append(m, e("gs", in.gsSection()), e("catalog", include("catalog")))
}

// grafanaSection is grafana: the one host the plugin links, the
// installation's own Grafana under the installation's name. Never dropped:
// the plugin's config schema requires the section, and a portal without it
// fails to start; with one host no entity needs the grafana/host-id
// annotation. Wired (plugins.grafana.enabled), the proxy entry the
// dashboards card reads through follows it in appConfig, and the extension
// list appSection includes is the one with the card switched on.
func (in *Input) grafanaSection() render.Map {
	return render.Map{e("hosts", []render.Map{{e("id", in.Installation.Name), e("domain", in.grafanaURL())}})}
}

// appSection is app: the portal's identity, the shared extensions and routes,
// the error reporter and the telemetry. The extension list is the fleet's
// shared one without the platform's section — the agent-platform Component's
// fragment includes the list with it where the platform runs, and Backstage
// takes the later file's list whole — with the Grafana dashboards card
// switched on where the plugin is wired: the card is disabled in the app
// until a portal that carries the proxy entry opts in.
func (in *Input) appSection() render.Map {
	app := render.Map{
		e("title", in.Portal.Title), e("baseUrl", in.portalURL()),
		e("extensions", render.Map{e("$include", render.PortalExtensionsInclude(false, in.Plugins.Grafana.Enabled))}), e("routes", include("routes")),
	}
	if in.Plugins.Sentry.Enabled {
		app = append(app, e("errorReporter", sentryReporter("SENTRY_DSN_APP")))
	}
	if in.Portal.TelemetryDeckApp != "" {
		app = append(app, e("telemetrydeck", render.Map{e("appID", in.Portal.TelemetryDeckApp), e("salt", envVar("TELEMETRYDECK_SALT"))}))
	}
	return app
}

// backendSection is backend: the plugin signing keys, the listener, CORS, the
// CSP over the shared sources, the in-memory database and cache, what the
// backend may read, and the error reporter.
func (in *Input) backendSection() render.Map {
	keyDir := pluginKeysMount + in.PluginKeys.KeyID + "/"
	csp := render.Map{}
	if in.Plugins.Sentry.Enabled {
		csp = append(csp, e("report-uri", []string{envVar("SENTRY_REPORT_URI")}))
	}
	csp = append(csp,
		e("connect-src", include("csp.connectSrc")), e("img-src", include("csp.imgSrc")),
		e("script-src", include("csp.scriptSrc")), e("worker-src", include("csp.workerSrc")))
	b := render.Map{
		e("auth", render.Map{e("pluginKeyStore", render.Map{
			e("type", "static"),
			e("static", render.Map{e("keys", []render.Map{{
				e("publicKeyFile", keyDir+"public.key"), e("privateKeyFile", keyDir+"private.key"), e("keyId", in.PluginKeys.KeyID)}})}),
		})}),
		e("baseUrl", in.portalURL()),
		e("listen", render.Map{e("port", 7007), e("host", "0.0.0.0")}),
		e("cors", render.Map{e("origin", in.portalURL()), e("methods", []string{"GET", "HEAD", "PATCH", "POST", "PUT", "DELETE"}), e("credentials", true)}),
		e("csp", csp),
		e("database", render.Map{e("client", "better-sqlite3"), e("connection", ":memory:")}),
		e("cache", render.Map{e("store", "memory")}),
		e("reading", render.Map{e("allow", []render.Map{{e("host", "*."+in.Installation.BaseDomain)}})}),
	}
	if in.Plugins.Sentry.Enabled {
		b = append(b, e("errorReporter", sentryReporter("SENTRY_DSN_BACKEND")))
	}
	return b
}

// clusters are the Kubernetes cluster entries: one per installation the
// portal shows, each read through the installation's own provider.
func (in *Input) clusters() []render.Map {
	var out []render.Map
	for _, inst := range in.installations() {
		out = append(out, render.Map{
			e("name", inst.Name), e("url", "https://"+hostOn("happaapi", inst.BaseDomain)),
			e("authProvider", "oidc"), e("oidcTokenProvider", render.PortalAuthProvider(inst.Name))})
	}
	return out
}

// oidcProviders are the Dex providers, each with its development and
// production environment: one per installation the portal shows, or the
// sign-in installation's alone where a token broker serves the others.
func (in *Input) oidcProviders() render.Map {
	providers := render.Map{}
	for _, inst := range in.providerInstallations() {
		providers = append(providers, e(render.PortalAuthProvider(inst.Name), render.Map{
			e("development", oidcProvider(inst)), e("production", oidcProvider(inst))}))
	}
	return providers
}

// oidcProvider is one environment of an installation's Dex provider.
func oidcProvider(inst FederatedInstallation) render.Map {
	return render.Map{
		e("metadataUrl", "https://"+hostOn("dex", inst.BaseDomain)+"/.well-known/openid-configuration"),
		e("clientId", dexEnv(inst.Name, "CLIENT_ID")), e("clientSecret", dexEnv(inst.Name, "CLIENT_SECRET")),
	}
}

// gsSection is gs: the Giant Swarm plugin's sign-in provider, the token
// broker where one installation brokers cluster tokens for the others, the
// home page's resources where the portal has a support link, the friendly
// names, the installations it shows (each with its region where it has one
// and its token audience where the broker is another) and the shared groups
// and versions.
func (in *Input) gsSection() render.Map {
	m := render.Map{
		e("authProvider", in.authProvider()),
		e("auth", render.Map{e("extraScopes", include("auth.extraScopes"))}),
	}
	if broker := in.tokenBroker(); broker != "" {
		m = append(m, e("clusterTokenBroker", render.Map{
			e("clientId", envVar("AUTH_DEX_MUSTER_BROKER_CLIENT_ID")), e("clientSecret", envVar("AUTH_DEX_MUSTER_BROKER_CLIENT_SECRET")),
			e("tokenUrl", "https://"+hostOn("muster", in.installation(broker).BaseDomain)+"/oauth/token")}))
	}
	if in.Portal.SupportURL != "" {
		m = append(m, e("homepage", render.Map{e("resources", []render.Map{
			include("homepageResources.gsDocs"), include("homepageResources.gsGitHub"),
			include("homepageResources.portalChangelog"), include("homepageResources.portalRoadmap"),
			{e("label", supportLabel), e("icon", supportIcon), e("url", in.Portal.SupportURL)},
		})}))
	}
	if len(in.Portal.FriendlyLabels) > 0 {
		m = append(m, e("friendlyLabels", in.Portal.FriendlyLabels))
	}
	if len(in.Portal.FriendlyAnnotations) > 0 {
		m = append(m, e("friendlyAnnotations", in.Portal.FriendlyAnnotations))
	}
	entries := render.Map{}
	for _, inst := range in.installations() {
		entries = append(entries, e(inst.Name, in.installationEntry(inst)))
	}
	return append(m,
		e("installations", entries),
		e("adminGroups", include("adminGroups")),
		e("kubernetesVersions", include("kubernetesVersions")),
	)
}

// installationEntry is one installation as the portal's gs.installations
// shows it.
func (in *Input) installationEntry(inst FederatedInstallation) render.Map {
	entry := render.Map{e("authProvider", "oidc"), e("baseDomain", inst.BaseDomain)}
	if broker := in.tokenBroker(); broker != "" && broker != inst.Name {
		entry = append(entry, e("clusterTokenAudience", inst.Name))
	}
	entry = append(entry, e("oidcTokenProvider", render.PortalAuthProvider(inst.Name)), e("pipeline", inst.Pipeline), e("providers", inst.Providers))
	if inst.Region != "" {
		entry = append(entry, e("region", inst.Region))
	}
	return entry
}
