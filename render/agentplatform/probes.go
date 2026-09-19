package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// The features of definitions/agent-platform/features.yaml a probe or an
// action belongs to.
const (
	featureIdentity   = "identity"
	featureSecrets    = "secrets"
	featureRuntime    = "runtime"
	featureToolAccess = "tool-access"
)

const (
	// fluxNamespace is where the installation's HelmReleases live.
	fluxNamespace = "flux-giantswarm"
	// oauth2ProxyDeployment is the kagent UI's oauth2-proxy, the workload that
	// protects the UI and the API.
	oauth2ProxyDeployment = "kagent-oauth2-proxy"
	// musterConfigMap carries muster's server configuration as the chart renders it.
	musterConfigMap = "muster-config"
	// conditionTrue and conditionFalse are the statuses a Condition probe expects.
	conditionTrue  = "True"
	conditionFalse = "False"
)

// probes are the checks of the running installation, one or more per live
// dimension of features.yaml, in the order the verify slice runs them: the
// releases, kagent's workloads, the Secrets, the model configuration, the
// identity chain over HTTP, the tool access, the logs, and the drift of live
// values against the render. Everything kagent's is probed only when kagent
// is enabled. Nothing here runs anything: a probe is data.
func (in *Input) probes() []render.Probe {
	var p []render.Probe
	for _, name := range in.helmReleases() {
		p = append(p, resourceProbe("live-helmreleases-ready", featureRuntime, render.HelmReleaseReady, fluxNamespace, "HelmRelease", name))
	}
	if in.kagent() {
		for _, name := range []string{"kagent-controller", "kagent-ui", oauth2ProxyDeployment} {
			p = append(p, conditionProbe("live-kagent-workloads", featureRuntime, kagentNamespace, "Deployment", name, "Available", conditionTrue))
		}
		p = append(p, conditionProbe("live-kagent-workloads", featureRuntime, kagentNamespace, "Cluster.postgresql.cnpg.io", "kagent-pg", "Ready", conditionTrue))
		credentials := resourceProbe("live-oauth2-proxy-secret-and-flux-sa", featureSecrets, render.ResourcePresent, kagentNamespace, "Secret", "kagent-oauth2-proxy-credentials")
		credentials.Expect.Keys = []string{"client-id", "client-secret", "cookie-secret"}
		p = append(p, credentials,
			resourceProbe("live-oauth2-proxy-secret-and-flux-sa", featureSecrets, render.ResourcePresent, kagentNamespace, "ServiceAccount", "kagent-flux"),
			in.modelConfigProbe(),
			httpProbe("live-kagent-api-protected", featureIdentity, "https://"+in.host("kagent")+"/api/agents", render.Expectation{Status: 403}),
			httpProbe("live-kagent-login-redirect", featureIdentity, "https://"+in.host("kagent")+"/oauth2/start", render.Expectation{
				Status: 302, LocationContains: "client_id=kagent",
				Note: "the login redirects to Dex at https://" + in.host("dex") + "/auth as client kagent",
			}))
	}
	for _, c := range in.dexRedirectClients() {
		p = append(p, httpProbe("live-dex-auth-per-client", featureIdentity, in.dexAuthURL(c), render.Expectation{Status: 302}))
	}
	p = append(p, httpProbe("live-muster-protected-resource", featureToolAccess, "https://"+in.host("muster")+"/.well-known/oauth-protected-resource", render.Expectation{
		Status: 200, BodyContains: `"resource":"https://` + in.host("muster") + `/mcp"`,
	}))
	for _, s := range servers {
		p = append(p, resourceProbe("live-own-mcp-servers", featureToolAccess, render.ResourcePresent, platformNamespace, "MCPServer.muster.giantswarm.io", in.Installation.Name+"-"+s.name))
	}
	if in.kagent() {
		audience := resourceProbe("live-oauth2-proxy-audience", featureIdentity, render.LogAbsent, kagentNamespace, "Deployment", oauth2ProxyDeployment)
		audience.Expect.Absent = "audience .* does not match"
		p = append(p, audience)
	}
	p = append(p, driftProbe("live-drift", featureRuntime, fluxNamespace, "HelmRelease", "agent-platform",
		"the values the HelmRelease reads from its ConfigMaps are the rendered values"))
	if in.kagent() {
		p = append(p,
			driftProbe("live-kagent-provider-values", featureRuntime, fluxNamespace, "HelmRelease", "kagent",
				".spec.values.providers are the rendered kagent.providers (the meta chart forwards its kagent block flat)",
				render.Comparison{Live: "spec.values.providers", Rendered: "kagent.providers"}),
			driftProbe("live-oauth2-proxy-extra-audience", featureIdentity, kagentNamespace, "Deployment", oauth2ProxyDeployment,
				"--oidc-extra-audience carries the rendered audiences",
				render.Comparison{Live: "spec.template.spec.containers[0].args[args]", Prefix: "--oidc-extra-audience=", Rendered: "kagent.oauth2-proxy.extraArgs.oidc-extra-audience"}))
	}
	p = append(p, driftProbe("live-muster-trusted-audiences", featureIdentity, platformNamespace, "ConfigMap", musterConfigMap,
		"trustedAudiences are the rendered muster.muster.oauth.server.trustedAudiences",
		render.Comparison{Live: "data.config.yaml:aggregator.oauth.server.trustedAudiences", Rendered: "muster.muster.oauth.server.trustedAudiences"}))
	// The Dex client id is the shared template's, never rendered here; the
	// connector id is rendered only when the installation pins a login
	// connector — without one the verify says the render carries no value
	// to hold the live one against.
	return append(p, driftProbe("live-muster-connector-and-client-id", featureIdentity, platformNamespace, "ConfigMap", musterConfigMap,
		"the Dex connectorId is the rendered muster.muster.oauth.server.dex.connectorId",
		render.Comparison{Live: "data.config.yaml:aggregator.oauth.server.dex.connectorId", Rendered: "muster.muster.oauth.server.dex.connectorId"}))
}

// The model provider key is the installation's own, on every installation:
// kagent is wired to the Secret by name, the definition renders no file for
// it and nobody supplies a value at commit; the person creates the Secret
// afterwards — the one step the platform leaves to them (CustomerActions),
// held up live by the model-key action until the default ModelConfig is
// Accepted.
const (
	modelKeySecret    = "kagent-anthropic-key" // #nosec G101 -- a Secret name, not a value
	modelKeySecretKey = "ANTHROPIC_API_KEY" // #nosec G101 -- a Secret key name, not a value
	modelKeyActionID  = "model-key"
	modelKeyDimension = "live-model-configs"
	modelKeyNote      = "Create Secret " + modelKeySecret + " in namespace " + kagentNamespace + " with key " + modelKeySecretKey + ", or add a ModelConfig in the portal; until then default-model-config stays Accepted=False."
	modelKeyWhy       = "the model key is the installation's own: the definition references the Secret and renders no value for it, and no value is supplied at commit; until the Secret exists the verify reads the runtime feature as waiting for the customer"
)

// actions are what a person outside the platform team still has to do for the
// installation to work as rendered: the model key, wherever kagent runs.
func (in *Input) actions() []render.Action {
	if !in.kagent() {
		return nil
	}
	return []render.Action{{ID: modelKeyActionID, Feature: featureRuntime, State: render.WaitingForCustomer, Dimension: modelKeyDimension, Note: modelKeyNote}}
}

// helmReleases are the HelmReleases the installation's platform consists of:
// the meta chart, kagent when it runs, the connectivity chart, muster, the
// servers' registration, and every own MCP server with its Valkey.
func (in *Input) helmReleases() []string {
	names := []string{"agent-platform"}
	if in.kagent() {
		names = append(names, "kagent")
	}
	names = append(names, "agent-platform-connectivity", "muster", "agent-platform-mcps")
	for _, s := range servers {
		names = append(names, s.name, s.name+"-valkey")
	}
	return names
}

// modelConfigProbe is the default ModelConfig's Accepted condition, True. The
// note says whose move a False is, and the model-key action (actions) names
// this dimension: until the person acts it reads waiting for the customer,
// not drifted.
func (in *Input) modelConfigProbe() render.Probe {
	p := conditionProbe(modelKeyDimension, featureRuntime, kagentNamespace, "ModelConfig.kagent.dev", "default-model-config", "Accepted", conditionTrue)
	p.Expect.Note = "waiting for the customer's model key (Secret " + modelKeySecret + ", key " + modelKeySecretKey + ", or a ModelConfig in the portal)"
	return p
}

// dexRedirectClient is a Dex client of the installation with the redirect URI
// an authorization request names.
type dexRedirectClient struct {
	id, redirectURI string
}

// dexRedirectClients are the clients the dex patch renders whose client id and
// redirect URI the definition knows, in the patch's order: muster, kagent's UI,
// the portals' client with each portal's redirect URI. The hubs' token-exchange
// clients have no redirect URI and are not probed.
func (in *Input) dexRedirectClients() []dexRedirectClient {
	// The built-in clients carry their client id, not their name: muster's is the
	// installation's musterClientId; the MCP servers' ids are the shared template's
	// and no input here, so their clients are not probed.
	clients := []dexRedirectClient{{id: in.Installation.MusterClientID, redirectURI: "https://" + in.host("muster") + "/oauth/callback"}}
	if in.kagent() {
		clients = append(clients, dexRedirectClient{id: "kagent", redirectURI: in.kagentRedirectURI()})
	}
	for _, p := range in.Installation.Portals {
		clients = append(clients, dexRedirectClient{id: render.PortalDexClientID, redirectURI: render.PortalRedirectURI(p.Domain, in.Installation.Name)})
	}
	return clients
}

// dexAuthURL is the authorization request Dex answers with a redirect for a
// client it knows: 302 to the login, never an error page.
func (in *Input) dexAuthURL(c dexRedirectClient) string {
	return "https://" + in.host("dex") + "/auth?client_id=" + c.id + "&redirect_uri=" + c.redirectURI + "&response_type=code&scope=openid"
}

// resourceProbe is a probe of one object, by kind, namespace, resource and name.
func resourceProbe(id, feature string, kind render.ProbeKind, namespace, resource, name string) render.Probe {
	return render.Probe{ID: id, Feature: feature, Kind: kind, Namespace: namespace, Resource: resource, Name: name}
}

// conditionProbe is a Condition probe: the object's condition has the status.
func conditionProbe(id, feature, namespace, resource, name, condition, status string) render.Probe {
	p := resourceProbe(id, feature, render.Condition, namespace, resource, name)
	p.Expect = render.Expectation{Condition: condition, ConditionStatus: status}
	return p
}

// driftProbe is a Drift probe of one object: the note says which of its
// values are compared to the render, compare names the places — none compares
// the object's whole user values to the rendered values file.
func driftProbe(id, feature, namespace, resource, name, note string, compare ...render.Comparison) render.Probe {
	p := resourceProbe(id, feature, render.Drift, namespace, resource, name)
	p.Expect.Note, p.Expect.Compare = note, compare
	return p
}

// httpProbe is a GET of url answered as expect says.
func httpProbe(id, feature, url string, expect render.Expectation) render.Probe {
	return render.Probe{ID: id, Feature: feature, Kind: render.HTTP, URL: url, Expect: expect}
}
