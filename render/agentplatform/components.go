package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// The klaus-gateway and cluster-manager components. The fleet policy
// (policy.yaml) runs the gateway on the installations with a Slack app and
// the cluster-manager where an organisation's list names it; where one runs,
// its values in the configmap patch and the Secrets its chart reads are data
// here. The gateway's shape is the policy's, its routes, URLs and Slack mode
// derive from the installation facts; its Slack credentials are supplied by
// the person, its OBO keys generated. The cluster-manager's egress derives
// from the provider.

const (
	// klausGatewayOBOSecret carries the gateway's HMAC keys for the
	// on-behalf-of links: state-key signs the login state, store-key encrypts
	// the link store. The chart mounts it as obo.existingSecret.
	klausGatewayOBOSecret = "klaus-gateway-obo-keys" // #nosec G101 -- a Secret name, not a value
	// klausGatewaySlackSecret carries the Slack app's credentials under the
	// keys the chart reads: bot-token, signing-secret and, in socket mode,
	// app-token.
	klausGatewaySlackSecret = "klaus-gateway-slack-credentials" // #nosec G101 -- a Secret name, not a value
	// oboStorePath is the bolt file the gateway keeps its links in, on the
	// persistent volume the chart mounts for it.
	oboStorePath = "/var/lib/klaus-gateway/obo/links.bolt"
	// slackDMMode is how the gateway treats direct messages: it serves them.
	slackDMMode = "serve"
	// slackSocketMode is the Slack mode in which the gateway connects out to
	// Slack with the app-level token and needs no route: a private
	// installation's, whose ingress Slack cannot reach.
	slackSocketMode = "socketmode"
	// slackEventsMode is the Slack mode in which Slack calls the gateway over
	// its public route: a public installation's.
	slackEventsMode = "events"
	// fieldSlack prefixes the supplied Slack credential fields.
	fieldSlack = "klausGateway.slack."
)

// teamLabels mark the platform team's Secrets.
var teamLabels = map[string]string{"application.giantswarm.io/team": "bumblebee"}

// slackMode is how the gateway talks to Slack, a fact of the record rather
// than a policy: socket mode where the installation is private (Slack cannot
// reach a private ingress with events), events mode over the public route
// elsewhere.
func (in *Input) slackMode() string {
	if in.Installation.Private {
		return slackSocketMode
	}
	return slackEventsMode
}

// slackSecretKeys are the keys of the Slack Secret, supplied by the person as
// klausGateway.slack.<key>: the app's bot token and signing secret, and in
// socket mode its app-level token.
func (in *Input) slackSecretKeys() []string {
	keys := []string{"bot-token", "signing-secret"}
	if in.slackMode() == slackSocketMode {
		keys = append(keys, "app-token")
	}
	return keys
}

// componentSecretFields are the supplied secret values the components need:
// the Slack app's credentials where the gateway runs.
func (in *Input) componentSecretFields() []string {
	if !in.klausGateway() {
		return nil
	}
	var fields []string
	for _, k := range in.slackSecretKeys() {
		fields = append(fields, fieldSlack+k)
	}
	return fields
}

// componentToggles adds the policy's components to the components map.
func (in *Input) componentToggles(components render.Map) render.Map {
	if in.klausGateway() {
		components = append(components, e(componentKlausGateway, render.Map{e("enabled", true)}))
	}
	if in.clusterManager() {
		components = append(components, e(componentClusterManager, render.Map{e("enabled", true)}))
	}
	return components
}

// componentValues appends the enabled components' values to the configmap patch.
func (in *Input) componentValues(m render.Map) render.Map {
	if in.klausGateway() {
		m = append(m, e("klausGateway", in.klausGatewayValues()))
	}
	if in.clusterManager() {
		m = append(m, e(componentClusterManager, in.clusterManagerValues()))
		if np := in.clusterManagerNetworkPolicy(); len(np) > 0 {
			m = append(m, e("clusterManager", render.Map{e("networkPolicy", np)}))
		}
	}
	return m
}

// klausGatewayValues is the gateway's section: its routing store, its public
// route where Slack calls in (events mode), the OBO links and Slack with their
// Secrets referenced, A2A with the installation's default agent and team
// reviews as the policy shapes them.
func (in *Input) klausGatewayValues() render.Map {
	g := in.Gateway
	mode := in.slackMode()
	m := render.Map{e("routing", render.Map{e("store", "valkey")})}
	if mode == slackEventsMode {
		m = append(m, e("agentgatewayRoute", render.Map{e("enabled", true), e("hostname", in.host("agentgateway"))}))
	}
	m = append(m, e("slack", render.Map{e("enabled", true), e("mode", mode), e("secretName", klausGatewaySlackSecret),
		e("dmMode", slackDMMode), e("channelMode", g.Slack.ChannelMode)}))
	m = append(m, e("obo", render.Map{e("enabled", true),
		e("connectors", render.Map{e("enabled", g.OBO.Connectors)}),
		e("existingSecret", klausGatewayOBOSecret),
		e("musterUrl", "https://"+in.host("muster")),
		e("callbackBaseUrl", "https://"+in.host("agentgateway")),
		e("storePath", oboStorePath),
		e("persistence", render.Map{e("enabled", true), e("size", "64Mi")})}))
	m = append(m, e("a2a", render.Map{e("enabled", g.A2A.Enabled), e("defaultAgent", g.A2A.DefaultAgent)}))
	reviews := render.Map{e("enabled", g.Reviews.Enabled)}
	if g.Reviews.Audience != "" {
		reviews = append(reviews, e("audience", g.Reviews.Audience))
	}
	if len(g.Reviews.AllowedCallers) > 0 {
		reviews = append(reviews, e("allowedCallers", g.Reviews.AllowedCallers))
	}
	return append(m, e("reviews", reviews))
}

// managerOAuth is a manager's OAuth section on the 3 chart line, where the
// template carries no global identity block: the resource server's public base
// URL under the gateway, the platform's Dex client and its Secret, the
// platform client id as the trusted audience.
func (in *Input) managerOAuth(path string) render.Map {
	return render.Map{
		e("baseURL", "https://"+in.host("agentgateway")+"/"+path),
		e("dex", render.Map{e("issuerURL", "https://"+in.host("dex")), e("clientID", in.Installation.MusterClientID)}),
		e("existingSecret", musterOAuthSecret),
		e("trustedAudiences", []string{in.Installation.MusterClientID}),
	}
}

// clusterManagerValues names the installation the manager runs on and, on the
// 3 line, its OAuth section; the 4 line derives issuer, client and secret from
// the template's global identity block and the base URL from the domain.
func (in *Input) clusterManagerValues() render.Map {
	m := render.Map{e("installation", render.Map{e("name", in.Installation.Name)})}
	if in.Installation.ChartLine == lineThree {
		m = append(m, e("oauth", in.managerOAuth(componentClusterManager)))
	}
	return m
}

// The cluster-manager's egress by provider: the workload clusters' API servers
// sit behind the provider's load balancers; the registry and the Azure blob
// endpoints (release assets, model caches) are the same everywhere.
var (
	workloadClusterAPIPatterns = map[string][]string{"capa": {"*.*.elb.amazonaws.com"}}
	clusterManagerEgress       = []render.Map{{e("matchName", "gsoci.azurecr.io")}, {e("matchPattern", "*.blob.core.windows.net")}}
)

// clusterManagerNetworkPolicy is the manager's egress as the connectivity
// chart reads it: Cilium FQDN selectors for the workload clusters' API servers
// (where the definition knows the provider's pattern) and for other egress.
func (in *Input) clusterManagerNetworkPolicy() render.Map {
	m := render.Map{}
	if patterns := workloadClusterAPIPatterns[in.Installation.Provider]; len(patterns) > 0 {
		var fqdns []render.Map
		for _, p := range patterns {
			fqdns = append(fqdns, render.Map{e("matchPattern", p)})
		}
		m = append(m, e("workloadClusters", render.Map{e("fqdns", fqdns)}))
	}
	return append(m, e("egress", render.Map{e("fqdns", clusterManagerEgress)}))
}

// componentSecrets adds the enabled components' Secrets to the platform
// extras: the gateway's generated OBO keys and its supplied Slack credentials.
func (in *Input) componentSecrets(add func(file string, f render.File), secrets map[string]string) {
	if !in.klausGateway() {
		return
	}
	add(klausGatewayOBOSecret+".yaml", render.Secret(klausGatewayOBOSecret, platformNamespace, teamLabels,
		in.generated("state-key", "klaus-gateway-obo-state-key", render.Base64, 32),
		in.generated("store-key", "klaus-gateway-obo-store-key", render.Base64, 32)))
	var keys []render.SecretKey
	for _, k := range in.slackSecretKeys() {
		keys = append(keys, render.ValueKey(k, secrets[fieldSlack+k]))
	}
	add(klausGatewaySlackSecret+".yaml", render.Secret(klausGatewaySlackSecret, platformNamespace, teamLabels, keys...))
}
