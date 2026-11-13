package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

type managerGrant struct {
	component, input, issuer, minimumVersion string
}

func (in *Input) additionalManagerGrants() []managerGrant {
	var grants []managerGrant
	if in.ClusterManagerTokenExchange {
		grants = append(grants, managerGrant{componentClusterManager, "clusterManager", "https://github.com/apps/giantswarm-cluster-manager", "0.37.0-rc.1"})
	}
	if in.ModelManagerTokenExchange {
		grants = append(grants, managerGrant{componentModelManager, "modelManager", "https://github.com/apps/giantswarm-model-manager", "1.20.0-rc.10"})
	}
	if in.SkillManagerTokenExchange {
		grants = append(grants, managerGrant{componentSkillManager, "skillManager", "https://github.com/apps/giantswarm-agent-skills", "0.2.0-rc.1"})
	}
	return grants
}

func (in *Input) hasManagerGrantExchange() bool {
	return in.AgentManagerTokenExchange || len(in.additionalManagerGrants()) > 0
}

func (grant managerGrant) brokerSecret() string { return grant.component + "-github-broker" }

func (grant managerGrant) providerApp() render.Map {
	return render.Map{
		e("name", grant.component), e("issuer", grant.issuer),
		e("authorizationEndpoint", "https://github.com/login/oauth/authorize"),
		e("tokenEndpoint", "https://github.com/login/oauth/access_token"),
		e("expectedIssuer", githubGrantIssuer),
		e("clientCredentialsSecretRef", render.Map{e("name", "giantswarm-"+grant.component+"-oauth-client")}),
	}
}

func (in *Input) managerGrantValues(component string) render.Map {
	return render.Map{e("enabled", true), e("tokenExchange", render.Map{
		e("tokenEndpoint", "https://"+in.host("muster")+"/oauth/token"),
		e("audience", component), e("existingSecret", component+"-github-broker"),
	})}
}

func (in *Input) managerBrokerKey(grant managerGrant) render.SecretKey {
	return in.generated("client-secret", grant.component+"-broker-client-secret", render.Base64, 32)
}

func (in *Input) managerBrokerCredentials(grant managerGrant) render.File {
	return render.Secret(grant.brokerSecret(), platformNamespace, teamLabels,
		render.ValueKey("client-id", grant.component), in.managerBrokerKey(grant))
}

func (in *Input) validateAdditionalManagerGrants() error {
	for _, grant := range in.additionalManagerGrants() {
		field := grant.input + ".github.tokenExchange.enabled"
		running := map[string]bool{
			componentClusterManager: in.clusterManager(),
			componentModelManager:   in.modelManager(),
			componentSkillManager:   in.skillManager(),
		}[grant.component]
		if in.Installation.ChartLine != lineFour || !running {
			return refuse(field + " requires " + grant.component + " on the 4 chart line")
		}
		if !holdAdmits(in.Versions.Chart, managerExchangeChart) {
			return refuse(field + " requires the meta chart " + managerExchangeChart + " or later")
		}
		// Explicit holds select an exchange-capable release over older chart defaults.
		if !floorAtLeast(in.Versions.Components[grant.component], grant.minimumVersion) {
			return refuse(field + " requires a versions.components." + grant.component + " floor of " + grant.minimumVersion + " or later")
		}
		if hold := in.Versions.Components[componentMuster]; hold != "" && !floorAtLeast(hold, musterGrantFloor) {
			return refuse(field + " requires a muster version floor of " + musterGrantFloor + " or later")
		}
		if in.federationBrokers() && in.brokerClient() == grant.component {
			return refuse(field + " reserves the " + grant.component + " broker client ID")
		}
		for _, target := range in.Installation.Federation.Targets {
			if target.Installation == grant.component {
				return refuse(field + " reserves the " + grant.component + " broker target")
			}
		}
		if grant.component == componentSkillManager && in.SkillManagerAppSecret == "" {
			return refuse(field + " requires skillManager.github.app.secretName for the skills App's installation access")
		}
	}
	return nil
}
