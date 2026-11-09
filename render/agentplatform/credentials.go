package agentplatform

import (
	"net/url"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// ManagerProviderAppName and ManagerGrantIssuer identify agent-manager's
// GitHub App in muster's provider Apps: the grants are filed under the App's
// own identity, not under GitHub's shared OAuth issuer.
const (
	ManagerProviderAppName = componentAgentManager
	ManagerGrantIssuer     = "https://github.com/apps/giantswarm-agent-manager"
)

const (
	agentManagerBrokerAudience = "agent-manager-github"
	agentManagerBrokerSecret   = "agent-manager-github-broker"
	managerOperationFloor      = "2.22.0"
	// musterCredentialFloor is the first muster that accepts the configured
	// Dex audiences alongside resource access tokens.
	musterCredentialFloor = "5.59.5"
)

func (in *Input) validateAgentManagerCredentials() error {
	if !in.AgentManagerTokenExchange && in.AgentManagerCredentialUI == "" {
		return nil
	}
	if !in.agentManager() || in.Installation.ChartLine != lineFour {
		return refuse("agentManager credential operations require agent-manager on the 4 chart line")
	}
	if !floorAtLeast(in.Versions.Components[componentAgentManager], managerOperationFloor) {
		return refuse("agentManager credential operations require versions.components.agent-manager with a floor of " + managerOperationFloor + " or later")
	}
	if in.AgentManagerTokenExchange && in.AgentManagerCommit {
		return refuse("agentManager.githubTokenExchange.enabled and agentManager.github.enabled are mutually exclusive")
	}
	if in.AgentManagerTokenExchange {
		if hold := in.Versions.Components[componentMuster]; hold != "" && !floorAtLeast(hold, musterCredentialFloor) {
			return refuse("agentManager.githubTokenExchange.enabled requires a muster version floor of " + musterCredentialFloor + " or later")
		}
		if in.federationBrokers() && in.brokerClient() == componentAgentManager {
			return refuse("agentManager.githubTokenExchange.enabled reserves the agent-manager broker client ID")
		}
		for _, target := range in.Installation.Federation.Targets {
			if target.Installation == agentManagerBrokerAudience {
				return refuse("agentManager.githubTokenExchange.enabled reserves the agent-manager-github broker target")
			}
		}
	}
	if in.AgentManagerCredentialUI != "" && !validCredentialUI(in.AgentManagerCredentialUI) {
		return refuse("agentManager.credentials.uiURL requires an HTTPS URL with exactly one {operation} in its path or query, optionally {parent} and {organization}, and no other braces, user information or fragment")
	}
	return nil
}

// floorAtLeast says whether the version hold's floor is minimum or later.
func floorAtLeast(hold, minimum string) bool {
	version, err := portalChartFloor(hold)
	floor, _ := portalChartFloor(minimum)
	return err == nil && !version.LessThan(floor)
}

// validCredentialUI applies agent-manager's
// organizations.ValidateCredentialUIURL, so a template the definition accepts
// never stops the manager from starting.
func validCredentialUI(template string) bool {
	const marker = "credential-operation-placeholder"
	resolved := strings.NewReplacer("{operation}", marker, "{parent}", marker, "{organization}", marker).Replace(template)
	parsed, err := url.Parse(resolved)
	return err == nil && !strings.ContainsAny(resolved, "{}") && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.Fragment == "" && !strings.Contains(parsed.Host, marker) && strings.Count(template, "{operation}") == 1
}

// brokerSubjectAudiences are the audiences muster's broker accepts on this
// installation's Dex ID tokens: the portals', and with the manager's grant
// exchange the authenticator's, the cross-client audience of the ID token
// muster forwards to agent-manager.
func (in *Input) brokerSubjectAudiences() []string {
	audiences := in.portalAudiences()
	if in.AgentManagerTokenExchange && !slices.Contains(audiences, authenticatorClient) {
		audiences = append(audiences, authenticatorClient)
	}
	return audiences
}

func agentManagerProviderApp() render.Map {
	return render.Map{
		e("name", ManagerProviderAppName), e("issuer", ManagerGrantIssuer),
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
