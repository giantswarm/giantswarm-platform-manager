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
	agentManagerBrokerSecret = "agent-manager-github-broker"
	// Credential UI configuration is supported independently of grant exchange.
	credentialChart       = "4.177.0"
	managerOperationFloor = "2.22.0"
	managerExchangeFloor  = "3.0.0-rc.10"
	// The broker must release provider App grants and decode Basic credentials.
	musterGrantFloor = "5.63.2"
)

// credentialUI is the credential form's address agent-manager links
// (agentManager.credentials.uiURL), where it renders: with the
// organizations on only. The meta chart refuses the form without the switch
// (agent-manager links it beside its organization tools), so the schema's
// x-requires refuses it typed with the switch off, and a value read back
// with the switch off renders nothing: a person turns the switch off alone,
// and the form goes with it.
func (in *Input) credentialUI() string {
	if !in.Organizations.Enabled {
		return ""
	}
	return in.AgentManagerCredentialUI
}

func (in *Input) validateAgentManagerCredentials() error {
	if !in.AgentManagerTokenExchange && in.credentialUI() == "" {
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
	if ui := in.credentialUI(); ui != "" && !validCredentialUI(ui) {
		return refuse("agentManager.credentials.uiURL requires an HTTPS RFC 6570 URI template with one {operation} expression in its path or query, using only {operation}, {parent} and {organization}, and no user information or fragment")
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
var credentialUIVariables = []string{"operation", "parent", "organization"}

// validCredentialUI applies agent-manager's
// organizations.ValidateCredentialUIURL (an RFC 6570 URI template), so a
// template the definition accepts never stops the manager from starting.
func validCredentialUI(template string) bool {
	const marker = "credentialoperationplaceholder"
	parsed, err := uritemplate.New(template)
	if err != nil || !slices.Contains(parsed.Varnames(), "operation") {
		return false
	}
	for _, name := range parsed.Varnames() {
		if !slices.Contains(credentialUIVariables, name) {
			return false
		}
	}
	expanded, err := parsed.Expand(uritemplate.Values{
		"operation":    uritemplate.String(marker + "o"),
		"parent":       uritemplate.String(marker + "p"),
		"organization": uritemplate.String(marker + "n"),
	})
	if err != nil {
		return false
	}
	u, err := url.Parse(expanded)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" &&
		!strings.Contains(u.Host, marker) && strings.Count(expanded, marker+"o") == 1
}

// jwtModeStrict is an agentgateway JWT policy's mode that answers a request
// without a valid bearer 401.
const jwtModeStrict = "Strict"

// agentManagerRoute says whether the meta chart exposes agent-manager's REST
// API on the gateway: where agent-manager links a credential form
// (agentManager.credentials.uiURL). The form's backend reaches the API as the
// person at https://agentgateway.<base domain>/agent-manager, the route's
// path, whichever installation hosts the portal (giantswarm/backstage#3435).
func (in *Input) agentManagerRoute() bool { return in.credentialUI() != "" }

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
	if in.AgentManagerTokenExchange && !slices.Contains(audiences, ManagerExchangeSubjectAudience) {
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
