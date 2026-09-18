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
	sharedConfig = "shared-config.yaml#"
	// pluginKeysMount is where the chart mounts pluginKeys[*] by keyId.
	pluginKeysMount = "/app/plugin-keys/"
	// The Sentry error reporter's settings the fleet's portals share.
	sentryEnvironment = "production"
	sentryReleaseVar  = "$${VERSION}"
	sentrySampleRate  = 0.5
	// supportLabel is the home page's support link, as the fleet's portals label it.
	supportLabel = "Giant Swarm \n Support"
)

// include is a $include of an anchor of the base's shared-config.yaml.
func include(anchor string) render.Map {
	return render.Map{e("$include", sharedConfig+anchor)}
}

// envVar is the reference Backstage resolves from its environment after the
// fleet's substitution.
func envVar(name string) string { return "$${" + name + "}" }

// dexEnv is the chart's environment variable for the portal's Dex client
// under the provider's name (dexAuthCredentials.<name>).
func (in *Input) dexEnv(suffix string) string {
	return envVar("AUTH_DEX_" + strings.ToUpper(in.Installation.Name) + "_" + suffix)
}

// sentryReporter is the error reporter block of the app or the backend.
func sentryReporter(dsnEnv string) render.Map {
	return render.Map{e("sentry", render.Map{
		e("dsn", envVar(dsnEnv)), e("environment", sentryEnvironment),
		e("releaseVersion", sentryReleaseVar), e("tracesSampleRate", sentrySampleRate)})}
}

// appConfig is backstage.appConfig of the app-config ConfigMap.
func (in *Input) appConfig() render.Map {
	provider := in.authProvider()
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
			e("clusterLocatorMethods", []render.Map{{
				e("type", "config"),
				e("clusters", []render.Map{{
					e("name", in.Installation.Name), e("url", "https://"+in.host("happaapi")),
					e("authProvider", "oidc"), e("oidcTokenProvider", provider)}}),
			}}),
		}),
		e("auth", render.Map{
			e("environment", "production"),
			e("session", render.Map{e("secret", envVar("AUTH_SESSION_SECRET"))}),
			e("providers", render.Map{e(provider, render.Map{
				e("development", in.oidcProvider()), e("production", in.oidcProvider())})}),
		}),
	)
	if in.Plugins.Grafana.Enabled {
		m = append(m, e("grafana", render.Map{e("domain", in.Plugins.Grafana.Domain)}))
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

// appSection is app: the portal's identity, the shared extensions and routes,
// the error reporter and the telemetry.
func (in *Input) appSection() render.Map {
	app := render.Map{
		e("title", in.Portal.Title), e("baseUrl", in.portalURL()),
		e("extensions", include("extensions")), e("routes", include("routes")),
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

// oidcProvider is one environment of the portal's Dex provider.
func (in *Input) oidcProvider() render.Map {
	return render.Map{
		e("metadataUrl", "https://"+in.host("dex")+"/.well-known/openid-configuration"),
		e("clientId", in.dexEnv("CLIENT_ID")), e("clientSecret", in.dexEnv("CLIENT_SECRET")),
	}
}

// gsSection is gs: the Giant Swarm plugin's sign-in provider, the home page's
// resources where the portal has a support link, the installation it shows
// (its region where it has one) and the shared groups and versions.
func (in *Input) gsSection() render.Map {
	provider := in.authProvider()
	m := render.Map{
		e("authProvider", provider),
		e("auth", render.Map{e("extraScopes", include("auth.extraScopes"))}),
	}
	if in.Portal.SupportURL != "" {
		m = append(m, e("homepage", render.Map{e("resources", []render.Map{
			include("homepageResources.gsDocs"), include("homepageResources.gsGitHub"),
			include("homepageResources.portalChangelog"), include("homepageResources.portalRoadmap"),
			{e("label", supportLabel), e("icon", "LiveHelp"), e("url", in.Portal.SupportURL)},
		})}))
	}
	entry := render.Map{
		e("authProvider", "oidc"), e("baseDomain", in.Installation.BaseDomain),
		e("oidcTokenProvider", provider), e("pipeline", in.Installation.Pipeline),
		e("providers", []string{in.Installation.Provider}),
	}
	if in.Installation.Region != "" {
		entry = append(entry, e("region", in.Installation.Region))
	}
	return append(m,
		e("installations", render.Map{e(in.Installation.Name, entry)}),
		e("adminGroups", include("adminGroups")),
		e("kubernetesVersions", include("kubernetesVersions")),
	)
}
