package agentplatform

import (
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

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
	// konfiguration renders the meta chart's values from the configs
	// repository into the ConfigMap the agent-platform HelmRelease reads.
	konfiguration = "agent-platform-konfiguration"
	// conditionTrue and conditionFalse are the statuses a Condition probe expects.
	conditionTrue  = "True"
	conditionFalse = "False"
)

// probes are the checks of the running installation, one or more per live
// dimension of features.yaml, in the order the verify slice runs them: the
// configuration on its source's revision, the releases, the workloads that
// read muster's credentials, kagent's workloads, the Secrets, the model
// configuration, the identity chain over HTTP, Dex holding every referenced
// client secret, the tool access, the logs, and the drift of live values
// against the render. Everything kagent's
// is probed only when kagent is enabled. Nothing here runs anything: a probe
// is data.
func (in *Input) probes() []render.Probe {
	p := []render.Probe{resourceProbe("live-konfiguration-source", featureRuntime, render.SourceFollowed, fluxNamespace, "Konfiguration.konfigure.giantswarm.io", konfiguration)}
	for _, name := range in.helmReleases() {
		p = append(p, resourceProbe("live-helmreleases-ready", featureRuntime, render.HelmReleaseReady, fluxNamespace, "HelmRelease", name))
	}
	// A HelmRelease stays Ready when its pods turn unready after the upgrade,
	// as a consumer left on rotated-away credentials does: its Deployment is
	// read itself.
	for _, c := range in.runningMusterConsumers() {
		p = append(p, conditionProbe(platformWorkloadsDimension, featureRuntime, platformNamespace, "Deployment", c.deployment, "Available", conditionTrue))
	}
	if in.kagent() {
		for _, name := range []string{"kagent-controller", "kagent-ui", oauth2ProxyDeployment} {
			p = append(p, conditionProbe("live-kagent-workloads", featureRuntime, kagentNamespace, "Deployment", name, "Available", conditionTrue))
		}
		p = append(p, conditionProbe("live-kagent-workloads", featureRuntime, kagentNamespace, "Cluster.postgresql.cnpg.io", "kagent-pg", "Ready", conditionTrue))
		credentials := resourceProbe("live-oauth2-proxy-secret-and-flux-sa", featureSecrets, render.ResourcePresent, kagentNamespace, "Secret", kagentOAuth2ProxySecret)
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
		p = append(p, render.DexAuthProbe("live-dex-auth-per-client", featureIdentity, in.host("dex"), c.id, c.redirectURI))
	}
	for _, c := range in.dexSecretClients() {
		p = append(p, render.DexSecretLoadedProbe(dexSecretsLoadedDimension, featureIdentity, dexNamespace, c.secret, c.client))
	}
	for _, c := range in.HubConnectors {
		p = append(p, render.DexConnectorSecretLoadedProbe(dexSecretsLoadedDimension, featureIdentity, dexNamespace, render.ConnectorSecretName(c.ID), c.ID))
	}
	if in.agentManagerRoute() {
		// The credential form's path: the route answers a call without a
		// token 401 at the gateway's JWT policy; without the route the
		// hostname answers 404.
		p = append(p, httpProbe(agentManagerRouteDimension, featureToolAccess, "https://"+in.host("agentgateway")+"/agent-manager/api/v1/info", render.Expectation{
			Status: 401, Note: "agent-manager's REST API on the gateway refuses a call without the person's token",
		}))
	}
	p = append(p, httpProbe("live-muster-protected-resource", featureToolAccess, "https://"+in.host("muster")+"/.well-known/oauth-protected-resource", render.Expectation{
		Status: 200, BodyContains: `"resource":"https://` + in.host("muster") + `/mcp"`,
	}))
	for _, s := range servers {
		p = append(p, resourceProbe("live-own-mcp-servers", featureToolAccess, render.ResourcePresent, platformNamespace, mcpServerResource, in.Installation.Name+"-"+s.Name))
	}
	for _, s := range in.Installation.MCPServers {
		p = append(p, registeredServerProbe(s))
	}
	if in.kagent() {
		audience := resourceProbe("live-oauth2-proxy-audience", featureIdentity, render.LogAbsent, kagentNamespace, "Deployment", oauth2ProxyDeployment)
		audience.Expect.Absent = "audience .* does not match"
		p = append(p, audience)
	}
	if in.kagent() && in.Installation.ChartLine == lineFour {
		p = append(p, agentModelsProbe(), in.podCertificateRequestProbe())
	}
	p = append(p, driftProbe("live-drift", featureRuntime, fluxNamespace, "HelmRelease", "agent-platform",
		"the values the HelmRelease reads from its ConfigMaps are the rendered values"))
	if in.kagent() && in.declaresModels() {
		p = append(p, driftProbe("live-kagent-provider-values", featureRuntime, fluxNamespace, "HelmRelease", "kagent",
			".spec.values.providers are the rendered kagent.providers (the meta chart forwards its kagent block flat)",
			render.Comparison{Live: "spec.values.providers", Rendered: "kagent.providers"}))
	}
	if in.kagent() {
		p = append(p,
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

// A model is the installation's own: a ModelConfig with its key Secret,
// added in the portal or as an object under extras, the one step the
// platform leaves to the person (CustomerActions), held up live by the
// model-key action until some ModelConfig in kagent's namespace reads its
// model key resolved (modelConfigCondition). Where the meta chart still takes
// kagent's model provider (declaresModels), the patch wires it to the Secret
// modelKeySecret, which the definition renders no file for and nobody
// supplies at commit.
const (
	modelKeySecret    = "kagent-anthropic-key" // #nosec G101 -- a Secret name, not a value
	modelKeySecretKey = "ANTHROPIC_API_KEY"    // #nosec G101 -- a Secret key name, not a value
	modelKeyActionID  = "model-key"
	modelKeyDimension = "live-model-configs"
	modelKeyWhy       = "the model and its key are the installation's own: the definition renders no model key and none is supplied at commit; until a ModelConfig in " + kagentNamespace + " resolves its key the verify reads the runtime feature as waiting for the customer"
)

// The ModelConfig resource each meta chart line's kagent serves: the 4 line's
// chart renders its catalog at api.kagent.dev/v1alpha3, the 3 line's kagent
// still serves the kagent.dev group.
const (
	modelConfigResourceFour  = "ModelConfig.api.kagent.dev"
	modelConfigResourceThree = "ModelConfig.kagent.dev"
)

// modelConfigResource is the ModelConfig resource of the line the record
// selects: the probe and the customer action name the group kagent serves
// there, never one a hand-made object merely lingers in.
func (in *Input) modelConfigResource() string {
	if in.Installation.ChartLine == lineFour {
		return modelConfigResourceFour
	}
	return modelConfigResourceThree
}

// The ModelConfig condition that says the model key resolves, by line: on the
// 4 line's api.kagent.dev/v1alpha3 Accepted only says the spec is valid and
// ResolvedRefs whether the referenced Secret and key exist; the 3 line's
// kagent.dev ModelConfig carries Accepted alone, False while the Secret is
// missing.
const (
	modelConfigConditionFour  = "ResolvedRefs"
	modelConfigConditionThree = "Accepted"
)

// modelConfigCondition is the condition the model-key probe reads on the
// installation's line.
func (in *Input) modelConfigCondition() string {
	if in.Installation.ChartLine == lineFour {
		return modelConfigConditionFour
	}
	return modelConfigConditionThree
}

// modelKeyNote is the model step, naming the resource and the condition the
// installation's line serves, and the Secret the patch wires kagent to where
// the meta chart still takes kagent's model provider.
func (in *Input) modelKeyNote() string {
	add := "Add a ModelConfig with its key Secret in namespace " + kagentNamespace + ", in the portal or as an object under extras/agent-platform"
	if in.declaresModels() {
		add = "Create Secret " + modelKeySecret + " in namespace " + kagentNamespace + " with key " + modelKeySecretKey + ", or add a ModelConfig in the portal"
	}
	return add + "; until then no ModelConfig (" + in.modelConfigResource() + ") in " + kagentNamespace + " is " + in.modelConfigCondition() + "=True."
}

// kagent's own ModelConfig, which the kagent chart renders from the meta
// chart's providers below modelFreeChart and deletes from it on.
const kagentDefaultModelConfig = "default-model-config"

// The live dimension and the action of the Agents that still name
// kagentDefaultModelConfig.
const (
	agentModelsActionID  = "agent-models"
	agentModelsDimension = "live-agent-models"
	agentModelsWhy       = "the meta chart renders no " + kagentDefaultModelConfig + " from its release that declares no model on, and an Agent that names it stops resolving its model; the Agents and their HelmReleases are the installation's own"
)

// The Agent resource the 4 line's kagent serves, and where an Agent names
// its ModelConfig there.
const (
	agentResource  = "Agent.api.kagent.dev"
	agentModelPath = "spec.template.modelConfig.name"
)

// agentModelsProbe is no Agent in any namespace naming
// kagentDefaultModelConfig, each one that does named with the HelmRelease
// that renders it (an agent chart release's modelConfig.name): the list the
// rollout of modelFreeChart waits on, rendered on the 4 line on either side
// of it.
func agentModelsProbe() render.Probe {
	return render.Probe{ID: agentModelsDimension, Feature: featureRuntime, Kind: render.NoneNames, Resource: agentResource,
		Expect: render.Expectation{Path: agentModelPath, Value: kagentDefaultModelConfig,
			Note: "an Agent names a ModelConfig of its namespace; one naming " + kagentDefaultModelConfig + " is moved by its owner, in its HelmRelease's modelConfig.name where one renders it"}}
}

// agentModelsNote is the step of the Agents still naming
// kagentDefaultModelConfig.
func agentModelsNote() string {
	release, _, _ := strings.Cut(modelFreeChart, "-")
	return "Point every Agent that names " + kagentDefaultModelConfig + " at a ModelConfig of its namespace (modelConfig.name in its HelmRelease's values where an agent chart release renders it); the meta chart " + release + " and later renders no " + kagentDefaultModelConfig + "."
}

// actions are what a person outside the platform team still has to do for the
// installation to work as rendered, wherever kagent runs: a model with its
// key and, on the 4 line, every Agent off kagentDefaultModelConfig.
func (in *Input) actions() []render.Action {
	if !in.kagent() {
		return nil
	}
	actions := []render.Action{{ID: modelKeyActionID, Feature: featureRuntime, State: render.WaitingForCustomer, Dimension: modelKeyDimension, Note: in.modelKeyNote()}}
	if in.Installation.ChartLine == lineFour {
		actions = append(actions, render.Action{ID: agentModelsActionID, Feature: featureRuntime, State: render.WaitingForCustomer, Dimension: agentModelsDimension, Note: agentModelsNote()})
	}
	return actions
}

// platformWorkloadsDimension is the live dimension of the Deployments that
// read muster's credentials (musterConsumers).
const platformWorkloadsDimension = "live-platform-workloads"

// helmReleases are the HelmReleases the installation's platform consists of:
// the meta chart, kagent when it runs, the connectivity chart, muster, the
// servers' registration, agent-platform-operator with the organizations, and
// every own MCP server with its Valkey.
func (in *Input) helmReleases() []string {
	names := []string{"agent-platform"}
	if in.kagent() {
		names = append(names, "kagent")
	}
	names = append(names, "agent-platform-connectivity", "muster", "agent-platform-mcps")
	// The meta chart creates agent-platform-operator's release with the
	// organizations' switch: a release the merge brings for the first time,
	// read like the others so the rollout waits for it.
	if in.Organizations.Enabled {
		names = append(names, "agent-platform-operator")
	}
	for _, s := range servers {
		names = append(names, s.Name, s.Name+"-valkey")
	}
	return names
}

// modelConfigProbe is some ModelConfig in kagent's namespace with its model
// key condition True, in the group and by the condition the installation's
// line serves (modelConfigResource, modelConfigCondition): any ModelConfig
// there, so an installation that brings its own models never waits on a
// Secret the platform names. The note says whose move a False is, and the
// model-key action (actions) names this dimension: until the person acts it
// reads waiting for the customer, not drifted.
func (in *Input) modelConfigProbe() render.Probe {
	p := render.Probe{ID: modelKeyDimension, Feature: featureRuntime, Kind: render.AnyCondition, Namespace: kagentNamespace, Resource: in.modelConfigResource()}
	p.Expect = render.Expectation{Condition: in.modelConfigCondition(), ConditionStatus: conditionTrue,
		Note: "waiting for the customer's model: a ModelConfig in " + kagentNamespace + " with its key Secret in place"}
	return p
}

// podCertificateRequestDimension is the live dimension of the API Agent
// Substrate needs served.
const podCertificateRequestDimension = "live-pod-certificate-request"

// podCertificateRequestProbe discovers the API Agent Substrate needs through
// the installation's apiserver. Rendered where kagent runs on the 4 line,
// which the plan lets through only with the record saying the cluster has
// the gates: not served, the check reads rolling — the control plane and the
// nodes roll after the gates are set, and the meta chart's install waits for
// the API — never a fault of the installation.
func (in *Input) podCertificateRequestProbe() render.Probe {
	p := render.Probe{ID: podCertificateRequestDimension, Feature: featureRuntime, Kind: render.APIServed, Resource: PodCertificateRequestResource}
	p.Expect = render.Expectation{Version: PodCertificateRequestVersion,
		Note: "the record enables the feature gates " + strings.Join(PodCertificateRequestGates, ", ") + " (the cluster App's values, or its chart's default); the API is served once the control plane has rolled with them, and Agent Substrate's install waits for it"}
	return p
}

// dexRedirectClient is a Dex client of the installation with the redirect URI
// an authorization request names.
type dexRedirectClient struct {
	id, redirectURI string
}

// dexRedirectClients are the clients the dex patch renders whose client id and
// redirect URI the definition knows, in the patch's order: muster, kagent's UI,
// the portals' client with each portal's redirect URI, each registered
// confidential client with each of its redirect URIs. The hubs' token-exchange
// clients have no redirect URI and are not probed.
func (in *Input) dexRedirectClients() []dexRedirectClient {
	// The built-in clients carry their client id, not their name: muster's is the
	// installation's musterClientId; the MCP servers' ids are the shared template's
	// and no input here, so their clients are not probed.
	clients := []dexRedirectClient{{id: in.Installation.MusterClientID, redirectURI: "https://" + in.host("muster") + "/oauth/callback"}}
	if in.kagent() {
		clients = append(clients, dexRedirectClient{id: "kagent", redirectURI: in.kagentRedirectURI()})
	}
	if in.portalClient() {
		for _, p := range in.Installation.Portals {
			clients = append(clients, dexRedirectClient{id: render.PortalDexClientID, redirectURI: render.PortalRedirectURI(p.Domain, in.Installation.Name)})
		}
	}
	for _, c := range in.confidentialClients() {
		for _, uri := range c.RedirectURIs {
			clients = append(clients, dexRedirectClient{id: c.ClientID, redirectURI: uri})
		}
	}
	return clients
}

// dexSecretsLoadedDimension is the live dimension of Dex holding the current
// secret of every client whose secret it reads from a Secret.
const dexSecretsLoadedDimension = "live-dex-client-secrets-loaded" // #nosec G101 -- a dimension id, not a value

// dexSecretClient is a client of the dex patch whose secret Dex reads from a
// Secret in its namespace: the client by the patch's name for it (a built-in
// client's chart key, an extra client's id) and the Secret.
type dexSecretClient struct {
	client, secret string
}

// dexSecretClients are the clients of the dex patch with a referenced
// secret, in the patch's order, read from the patch itself so that every
// reference it renders is probed: muster, the MCP servers, kagent's UI, the
// portals' client, the hubs' token-exchange clients.
func (in *Input) dexSecretClients() []dexSecretClient {
	var out []dexSecretClient
	oidc, _ := mapValue(in.dexPatch(), "oidc").(render.Map)
	static, _ := mapValue(oidc, "staticClients").(render.Map)
	for _, c := range static {
		body, _ := c.Value.(render.Map)
		if ref, ok := mapValue(body, "clientSecretRef").(render.Map); ok {
			out = append(out, dexSecretClient{client: c.Key, secret: mapValue(ref, "name").(string)})
		}
	}
	extra, _ := mapValue(oidc, "extraStaticClients").([]render.Map)
	for _, c := range extra {
		if ref, ok := mapValue(c, "secretRef").(render.Map); ok {
			out = append(out, dexSecretClient{client: mapValue(c, "id").(string), secret: mapValue(ref, "name").(string)})
		}
	}
	return out
}

// mapValue is the value of key in m; nil when m has none.
func mapValue(m render.Map, key string) any {
	for _, en := range m {
		if en.Key == key {
			return en.Value
		}
	}
	return nil
}

// registeredServersDimension is the live dimension of the servers registered
// on the installation beyond its own (installation.mcpServers).
const registeredServersDimension = "live-registered-mcp-servers"

// mcpServerResource is muster's MCPServer, as a probe names the resource.
const mcpServerResource = "MCPServer.muster.giantswarm.io"

// mcpServerFailed is the state muster reports for a server it cannot reach.
const mcpServerFailed = "Failed"

// registeredServerProbe reads a registered server's MCPServer object: present
// and in no Failed state. Connected is a session's word — a server that
// forwards or exchanges the person's token reads Awaiting Session or Auth
// Required until a person's session holds a connection — so only Failed is
// muster unable to reach the server as registered.
func registeredServerProbe(s RegisteredServer) render.Probe {
	p := resourceProbe(registeredServersDimension, featureToolAccess, render.ResourcePresent, platformNamespace, mcpServerResource, s.Name)
	p.Expect.NotState = mcpServerFailed
	p.Expect.Note = "registered under extras/agent-platform/mcpservers, auth " + s.Auth + ", at " + s.URL + "; Awaiting Session and Auth Required are a server waiting for a person's session, Failed is muster unable to reach it"
	return p
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

// agentManagerRouteDimension is the live dimension of the credential form's
// route (agentManager.route).
const agentManagerRouteDimension = "live-agent-manager-route-protected"

// httpProbe is a GET of url answered as expect says.
func httpProbe(id, feature, url string, expect render.Expectation) render.Probe {
	return render.Probe{ID: id, Feature: feature, Kind: render.HTTP, URL: url, Expect: expect}
}
