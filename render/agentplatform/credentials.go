package agentplatform

import (
	"net/url"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	agentManagerBrokerAudience = "agent-manager-github"
	agentManagerBrokerSecret   = "agent-manager-github-broker"
	agentManagerGrantIssuer    = "https://github.com/apps/giantswarm-agent-manager"
	managerOperationFloor      = "2.22.0-rc.3"
)

func (in *Input) validateAgentManagerCredentials() error {
	if !in.AgentManagerTokenExchange && in.AgentManagerCredentialUI == "" {
		return nil
	}
	if !in.agentManager() || in.Installation.ChartLine != lineFour {
		return refuse("agentManager credential operations require agent-manager on the 4 chart line")
	}
	version, err := portalChartFloor(in.Versions.Components[componentAgentManager])
	minimum, _ := portalChartFloor(managerOperationFloor)
	if err != nil || version.LessThan(minimum) {
		return refuse("agentManager credential operations require versions.components.agent-manager with a floor of " + managerOperationFloor + " or later")
	}
	if in.AgentManagerTokenExchange && in.AgentManagerCommit {
		return refuse("agentManager.githubTokenExchange.enabled and agentManager.github.enabled are mutually exclusive")
	}
	if in.AgentManagerTokenExchange {
		if hold := in.Versions.Components["muster"]; hold != "" {
			version, err := portalChartFloor(hold)
			minimum, _ := portalChartFloor("5.58.0")
			if err != nil || version.LessThan(minimum) {
				return refuse("agentManager.githubTokenExchange.enabled requires a muster version floor of 5.58.0 or later")
			}
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
	if in.AgentManagerCredentialUI != "" {
		parsed, err := url.Parse(in.AgentManagerCredentialUI)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" || strings.Count(in.AgentManagerCredentialUI, "{operation}") != 1 {
			return refuse("agentManager.credentials.uiURL requires an HTTPS URL with one {operation}, no user information and no fragment")
		}
	}
	return nil
}

func (in *Input) agentManagerProviderApp() render.Map {
	return render.Map{
		e("name", "agent-manager"), e("issuer", agentManagerGrantIssuer),
		e("authorizationEndpoint", "https://github.com/login/oauth/authorize"),
		e("tokenEndpoint", "https://github.com/login/oauth/access_token"),
		e("expectedIssuer", "https://github.com/login/oauth"),
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
