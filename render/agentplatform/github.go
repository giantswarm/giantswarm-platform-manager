package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// GitHub as the signed-in person on a portal hosted by an installation that
// is not the registry's hub. The hub's broker releases the person's GitHub
// grant to the hub's Dev Portal (hub.go). Another installation whose muster
// holds the person's own GitHub grant — a registered server at GitHub's
// authorization server with subject-scoped grants
// (installation.mcpServers[*].githubGrant) — and hosts a portal does the same
// for that portal. People connect GitHub there, and the portal needs no GitHub
// App of its own. The broker gets the github grant target and one broker
// client, the portal's. The Component gives the portal that client's
// credentials, the broker's token URL and gs.github. The client's secret is
// drawn once, in the commit that creates both of its files: muster's
// broker-clients Secret and the Component's Secret. A hub, or an
// installation that brokers for targets, keeps its broker as it is: its
// portal's token broker is the customer-portal definition's
// (federation.tokenBroker).

const (
	// portalBrokerClient is the id of the portal's broker client in muster.
	portalBrokerClient = "backstage"
	// portalBrokerSecret carries the broker client's credentials as the chart
	// values the chart exposes as AUTH_DEX_MUSTER_BROKER_CLIENT_ID and
	// _CLIENT_SECRET (dexAuthCredentials.musterBroker). Its file's name
	// matches the fleet's sops rules.
	portalBrokerSecret = "agent-platform-github-broker-backstage" // #nosec G101 -- a Secret name, not a value
	portalBrokerFile   = "github-broker-credentials.enc.yaml"     // #nosec G101 -- a file name, not a value
	// brokerClientSecret is the generated broker client secret's base name.
	brokerClientSecret = "muster-broker-client-secret" // #nosec G101 -- a value's name, not a value
	// The portal's environment variables for the broker client; the doubled
	// dollar survives the fleet's variable substitution.
	brokerClientIDEnv     = "$${AUTH_DEX_MUSTER_BROKER_CLIENT_ID}"
	brokerClientSecretEnv = "$${AUTH_DEX_MUSTER_BROKER_CLIENT_SECRET}" // #nosec G101 -- a variable reference, not a value
)

// githubGrantServer is the registered server that holds the person's own
// GitHub grant, the first on record; "" without one.
func (in *Input) githubGrantServer() string {
	for _, s := range in.Installation.MCPServers {
		if s.GitHubGrant {
			return s.Name
		}
	}
	return ""
}

// portalGitHub says whether the hosted portal reads GitHub as the person
// through this installation's muster: it holds the grant, it is neither the
// hub nor a broker for targets, and it hosts a portal.
func (in *Input) portalGitHub() bool {
	return !in.Installation.Hub && len(in.Installation.Federation.Targets) == 0 &&
		in.hostedPortal() != nil && in.githubGrantServer() != ""
}

// grantsGitHub says whether the broker releases the person's GitHub grant:
// on the hub to the hub's portal, elsewhere to the hosted portal.
func (in *Input) grantsGitHub() bool { return in.Installation.Hub || in.portalGitHub() }

// brokers says whether muster runs the token broker: for the targets, or
// for the hosted portal's GitHub.
func (in *Input) brokers() bool {
	return len(in.Installation.Federation.Targets) > 0 || in.portalGitHub()
}

// brokerClient is the broker's client id: the hub's on record, or the
// hosted portal's.
func (in *Input) brokerClient() string {
	if in.portalGitHub() {
		return portalBrokerClient
	}
	return in.Installation.Federation.BrokerClientID
}

// brokerClientSecretKey is the broker client's generated secret in muster's
// broker-clients Secret.
func (in *Input) brokerClientSecretKey() render.SecretKey {
	return in.generated("client-secret", brokerClientSecret, render.Base64, 32)
}

// portalGitHubConfig is the fragment's gs block: the broker at this
// installation's muster with the portal's client, and GitHub through the
// grant server.
func (in *Input) portalGitHubConfig() render.Map {
	return render.Map{
		e("clusterTokenBroker", render.Map{e("clientId", brokerClientIDEnv), e("clientSecret", brokerClientSecretEnv),
			e("tokenUrl", "https://"+in.host("muster")+"/oauth/token")}),
		e("github", render.Map{e("brokerAudience", githubGrantTarget),
			e("muster", render.Map{e("installation", in.Installation.Name), e("server", in.githubGrantServer())})}),
	}
}

// portalBrokerCredentials is the Component's Secret with the broker client's
// credentials, in the values form the HelmRelease reads a Secret in. The
// chart copies both leaves under its Secret's data as they are, so the id is
// base64 here and the secret takes its encoded placeholder.
func (in *Input) portalBrokerCredentials() render.File {
	secret := in.brokerClientSecretKey().Generated.Encoded(render.EncodedBase64)
	values := render.Map{e("dexAuthCredentials", render.Map{e("musterBroker", render.Map{
		e("clientID", render.Base64Leaf(portalBrokerClient)), e("clientSecret", secret.Placeholder)})})}
	f := render.Secret(portalBrokerSecret, fluxNamespace, teamLabels, render.ValueKey("values", string(render.MustYAML(values))))
	f.Generated = append(f.Generated, secret)
	return f
}
