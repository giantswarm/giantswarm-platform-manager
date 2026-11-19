package agentplatform

import (
	"net/url"
	"slices"
	"strings"

	"github.com/yosida95/uritemplate/v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// managerProviderAppName and managerGrantIssuer identify agent-manager's
// GitHub App in muster's provider Apps: the grants are filed under the App's
// own identity, not under GitHub's shared OAuth issuer. The App's name is
// also the broker target agent-manager exchanges for and the audience it
// names (agent-manager.github.tokenExchange.audience): the chart registers
// the MCPServer with that name in auth.providerApps.
const (
	managerProviderAppName = componentAgentManager
	managerGrantIssuer     = "https://github.com/apps/giantswarm-agent-manager"
)

// ManagerExchangeSubjectAudience is the subject-token audience the broker
// accepts only for the manager's grant exchange: the audience of the ID
// token muster forwards to agent-manager.
const ManagerExchangeSubjectAudience = authenticatorClient

const (
	agentManagerBrokerSecret    = "agent-manager-github-broker"
	keyCredentials              = "credentials"
	credentialOperationVariable = "operation"
	credentialNamespaceVariable = "namespace"
	// Credential UI configuration is supported independently of grant exchange.
	credentialChart       = "4.177.0"
	managerOperationFloor = "2.22.0"
	managerExchangeChart  = "4.181.0-rc.3"
	managerExchangeFloor  = "3.0.0-rc.10"
	// The broker must release provider App grants and decode Basic credentials.
	musterGrantFloor = "5.63.2"
	// The first meta chart that renders uiURL without organizations, and the
	// first agent-manager that starts with a {namespace} variable in it.
	installationFormChart = "4.182.0-rc.1"
	namespaceFormFloor    = "3.1.0-rc.11"
)

func (in *Input) validateAgentManagerCredentials() error {
	if !in.AgentManagerTokenExchange && !in.agentManagerRoute() {
		return nil
	}
	if !in.agentManager() || in.Installation.ChartLine != lineFour {
		return refuse("agentManager credential operations require agent-manager on the 4 chart line")
	}
	if !holdAdmits(in.Versions.Chart, credentialChart) {
		return refuse("agentManager credential operations require the meta chart " + credentialChart + " or later, and versions.chart holds an earlier one")
	}
	if hold := in.Versions.Components[componentAgentManager]; hold != "" && !floorAtLeast(hold, managerOperationFloor) {
		return refuse("agentManager credential operations require a versions.components.agent-manager floor of " + managerOperationFloor + " or later")
	}
	if in.AgentManagerTokenExchange {
		if !holdAdmits(in.Versions.Chart, managerExchangeChart) {
			return refuse("agentManager.github.tokenExchange.enabled requires the meta chart " + managerExchangeChart + " or later, and versions.chart holds an earlier one")
		}
		if hold := in.Versions.Components[componentAgentManager]; hold != "" && !floorAtLeast(hold, managerExchangeFloor) {
			return refuse("agentManager.github.tokenExchange.enabled requires an agent-manager version floor of " + managerExchangeFloor + " or later")
		}
		if hold := in.Versions.Components[componentMuster]; hold != "" && !floorAtLeast(hold, musterGrantFloor) {
			return refuse("agentManager.github.tokenExchange.enabled requires a muster version floor of " + musterGrantFloor + " or later")
		}
		if in.federationBrokers() && in.brokerClient() == componentAgentManager {
			return refuse("agentManager.github.tokenExchange.enabled reserves the agent-manager broker client ID")
		}
		for _, target := range in.Installation.Federation.Targets {
			if target.Installation == managerProviderAppName {
				return refuse("agentManager.github.tokenExchange.enabled reserves the agent-manager broker target")
			}
		}
	}
	if ui := in.AgentManagerCredentialUI; ui != "" && !validCredentialUI(ui) {
		return refuse("agentManager.credentials.uiURL requires an HTTPS RFC 6570 URI template with one {operation} expression in its path or query, using only {operation}, {namespace}, {parent} and {organization}, and no user information or fragment")
	}
	if ui := in.AgentManagerCredentialUI; ui != "" && !in.Organizations.Enabled && !namesCredentialNamespace(ui) {
		return refuse("agentManager.credentials.uiURL requires one {namespace} expression without organizations.enabled: the installation's model links name the namespace; add {namespace} to agentManager.credentials.uiURL, or type it empty to remove the credential form")
	}
	if ui := in.AgentManagerCredentialUI; ui != "" && !in.Organizations.Enabled && usesOrganizationVariables(ui) {
		return refuse("agentManager.credentials.uiURL allows no {parent} or {organization} expression without organizations.enabled: the installation's model links have no organization to fill them")
	}
	if usesCredentialNamespace(in.AgentManagerCredentialUI) {
		if !holdAdmits(in.Versions.Chart, installationFormChart) {
			return refuse("a {namespace} expression in agentManager.credentials.uiURL requires the meta chart " + installationFormChart + " or later, and versions.chart holds an earlier one")
		}
		if hold := in.Versions.Components[componentAgentManager]; hold != "" && !floorAtLeast(hold, namespaceFormFloor) {
			return refuse("a {namespace} expression in agentManager.credentials.uiURL requires an agent-manager version floor of " + namespaceFormFloor + " or later")
		}
	}
	return nil
}

// floorAtLeast says whether the version hold's floor is minimum or later.
func floorAtLeast(hold, minimum string) bool {
	version, err := rangeFloor(hold)
	floor, _ := rangeFloor(minimum)
	return err == nil && !version.LessThan(floor)
}

// credentialUIVariables are the variables agent-manager expands in the
// credential UI template.
var credentialUIVariables = []string{credentialOperationVariable, credentialNamespaceVariable, "parent", "organization"}

// credentialUIMarker prefixes the value each template variable expands to
// when a template is checked; credentialUIMarkers names one per variable.
const credentialUIMarker = "credentialoperationplaceholder"

var credentialUIMarkers = map[string]string{
	credentialOperationVariable: credentialUIMarker + "o",
	credentialNamespaceVariable: credentialUIMarker + "s",
	"parent":                    credentialUIMarker + "p",
	"organization":              credentialUIMarker + "n",
}

// validCredentialUI applies agent-manager's
// organizations.ValidateCredentialUIURL (an RFC 6570 URI template), so a
// template the definition accepts never stops the manager from starting.
func validCredentialUI(template string) bool {
	expanded, ok := expandCredentialUI(template)
	if !ok {
		return false
	}
	u, err := url.Parse(expanded)
	return err == nil && strings.HasPrefix(template, "https://") && u.Host != "" && u.User == nil && u.Fragment == "" &&
		!strings.Contains(u.Host, credentialUIMarker) && strings.Count(expanded, credentialUIMarkers[credentialOperationVariable]) == 1
}

// namesCredentialNamespace says whether template expands {namespace} once:
// an installation model's link names the namespace its form writes in.
func namesCredentialNamespace(template string) bool {
	expanded, ok := expandCredentialUI(template)
	return ok && strings.Count(expanded, credentialUIMarkers[credentialNamespaceVariable]) == 1
}

// usesOrganizationVariables says whether template names {parent} or
// {organization}, which expand empty for an installation model's link.
func usesOrganizationVariables(template string) bool {
	parsed, err := uritemplate.New(template)
	return err == nil && (slices.Contains(parsed.Varnames(), "parent") || slices.Contains(parsed.Varnames(), "organization"))
}

// usesCredentialNamespace says whether template names the {namespace}
// variable in any expression.
func usesCredentialNamespace(template string) bool {
	parsed, err := uritemplate.New(template)
	return err == nil && slices.Contains(parsed.Varnames(), credentialNamespaceVariable)
}

// expandCredentialUI expands template with a marker for each variable; it
// refuses a template without {operation} or with another variable.
func expandCredentialUI(template string) (string, bool) {
	parsed, err := uritemplate.New(template)
	if err != nil || !slices.Contains(parsed.Varnames(), credentialOperationVariable) {
		return "", false
	}
	for _, name := range parsed.Varnames() {
		if !slices.Contains(credentialUIVariables, name) {
			return "", false
		}
	}
	values := uritemplate.Values{}
	for name, value := range credentialUIMarkers {
		values[name] = uritemplate.String(value)
	}
	expanded, err := parsed.Expand(values)
	return expanded, err == nil
}

// jwtModeStrict is an agentgateway JWT policy's mode that answers a request
// without a valid bearer 401.
const jwtModeStrict = "Strict"

// agentManagerRoute says whether the meta chart exposes agent-manager's REST
// API on the gateway: where agent-manager links a credential form
// (agentManager.credentials.uiURL). The form's backend reaches the API as the
// person at https://agentgateway.<base domain>/agent-manager, the route's
// path, whichever installation hosts the portal (giantswarm/backstage#3435).
func (in *Input) agentManagerRoute() bool { return in.AgentManagerCredentialUI != "" }

// agentManagerRouteValues is the meta chart's agentManager block for the
// route: on, behind a Strict JWT policy whose issuer is this installation's
// Dex and whose keys come from the in-cluster Dex Service (the edge's
// gateway.jwksEgress opens it). The hostname is the chart's,
// agentgateway.<global.domain>. The chart's policy names no audience:
// agent-manager checks the audience itself (agent-manager.oauth) and acts
// as the caller.
func (in *Input) agentManagerRouteValues() render.Map {
	return render.Map{e("route", render.Map{e("enabled", true), e("jwtAuthentication", render.Map{
		e("enabled", true), e("mode", jwtModeStrict), e("issuer", "https://"+in.host("dex")),
		e("jwks", render.Map{e("host", dexService+"."+dexNamespace+".svc.cluster.local"),
			e("port", dexServicePort), e("path", dexJWKSPath)}),
	})})}
}

// brokerSubjectAudiences are the audiences muster's broker accepts on this
// installation's Dex ID tokens: the portals', and with the manager's grant
// exchange the authenticator's, the cross-client audience of the ID token
// muster forwards to agent-manager.
func (in *Input) brokerSubjectAudiences() []string {
	audiences := in.portalAudiences()
	if in.hasManagerGrantExchange() && !slices.Contains(audiences, ManagerExchangeSubjectAudience) {
		audiences = append(audiences, ManagerExchangeSubjectAudience)
	}
	return audiences
}

func agentManagerProviderApp() render.Map {
	return render.Map{
		e("name", managerProviderAppName), e("issuer", managerGrantIssuer),
		e("authorizationEndpoint", "https://github.com/login/oauth/authorize"),
		e("tokenEndpoint", "https://github.com/login/oauth/access_token"),
		e("expectedIssuer", githubGrantIssuer),
		e("clientCredentialsSecretRef", render.Map{e("name", "giantswarm-agent-manager-oauth-client")}),
	}
}

func (in *Input) agentManagerBrokerCredentials() render.File {
	return render.Secret(agentManagerBrokerSecret, platformNamespace, teamLabels,
		render.ValueKey("client-id", componentAgentManager),
		in.agentManagerBrokerKey())
}

func (in *Input) agentManagerBrokerKey() render.SecretKey {
	return in.generated("client-secret", "agent-manager-broker-client-secret", render.Base64, 32)
}
