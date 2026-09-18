package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// The klaus-gateway and cluster-manager components. The fleet policy
// (policy.yaml) offers them to some customers only; where it does, their
// values in the configmap patch and the Secrets their charts read are data
// here. The gateway's routes and URLs derive from the installation facts; its
// Slack credentials are supplied by the person, its OBO keys generated.

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
	// slackSocketMode is the Slack mode that needs the app-level token.
	slackSocketMode = "socketmode"
	// fieldSlack prefixes the supplied Slack credential fields.
	fieldSlack = "klausGateway.slack."
)

// teamLabels mark the platform team's Secrets.
var teamLabels = map[string]string{"application.giantswarm.io/team": "bumblebee"}

func (in *Input) klausGatewayEnabled() bool {
	return in.KlausGateway != nil && in.KlausGateway.Enabled
}

func (in *Input) clusterManagerEnabled() bool {
	return in.ClusterManager != nil && in.ClusterManager.Enabled
}

// slackSecretKeys are the keys of the Slack Secret, supplied by the person as
// klausGateway.slack.<key>.
func (in *Input) slackSecretKeys() []string {
	keys := []string{"bot-token", "signing-secret"}
	if in.KlausGateway.Slack.Mode == slackSocketMode {
		keys = append(keys, "app-token")
	}
	return keys
}

// componentSecretFields are the supplied secret values the components need:
// the Slack app's credentials when the gateway fronts Slack.
func (in *Input) componentSecretFields() []string {
	if !in.klausGatewayEnabled() || in.KlausGateway.Slack == nil {
		return nil
	}
	var fields []string
	for _, k := range in.slackSecretKeys() {
		fields = append(fields, fieldSlack+k)
	}
	return fields
}

// componentToggles adds the components' enabled flags to the components map.
func (in *Input) componentToggles(components render.Map) render.Map {
	if in.KlausGateway != nil {
		components = append(components, e("klaus-gateway", render.Map{e("enabled", in.KlausGateway.Enabled)}))
	}
	if in.ClusterManager != nil {
		components = append(components, e("cluster-manager", render.Map{e("enabled", in.ClusterManager.Enabled)}))
	}
	return components
}

// componentValues appends the enabled components' values to the configmap patch.
func (in *Input) componentValues(m render.Map) render.Map {
	if in.klausGatewayEnabled() {
		m = append(m, e("klausGateway", in.klausGatewayValues()))
	}
	if in.clusterManagerEnabled() {
		m = append(m, e("cluster-manager", in.clusterManagerValues()))
		if in.ClusterManager.NetworkPolicy != nil {
			m = append(m, e("clusterManager", render.Map{e("networkPolicy", in.clusterManagerNetworkPolicy())}))
		}
	}
	return m
}

// klausGatewayValues is the gateway's section: its public route, the OBO
// links and Slack with their Secrets referenced, A2A and team reviews as given.
func (in *Input) klausGatewayValues() render.Map {
	g := in.KlausGateway
	m := render.Map{e("agentgatewayRoute", render.Map{e("enabled", true), e("hostname", in.host("agentgateway"))})}
	if g.Slack != nil {
		slack := render.Map{e("enabled", true), e("mode", g.Slack.Mode), e("secretName", klausGatewaySlackSecret),
			e("dmMode", slackDMMode), e("channelMode", g.Slack.ChannelMode)}
		if len(g.Slack.ChannelAllowlist) > 0 {
			slack = append(slack, e("channelAllowlist", g.Slack.ChannelAllowlist))
		}
		m = append(m, e("slack", slack))
	}
	obo := render.Map{e("enabled", true)}
	if g.OBO != nil {
		obo = append(obo, e("connectors", render.Map{e("enabled", g.OBO.Connectors)}))
	}
	obo = append(obo, e("existingSecret", klausGatewayOBOSecret),
		e("musterUrl", "https://"+in.host("muster")),
		e("callbackBaseUrl", "https://"+in.host("agentgateway")),
		e("storePath", oboStorePath),
		e("persistence", render.Map{e("enabled", true), e("size", "64Mi")}))
	m = append(m, e("obo", obo))
	if g.A2A != nil {
		a2a := render.Map{e("enabled", g.A2A.Enabled), e("defaultAgent", g.A2A.DefaultAgent)}
		if g.A2A.SATokenAudience != "" {
			a2a = append(a2a, e("saToken", render.Map{e("enabled", true), e("audience", g.A2A.SATokenAudience)}))
		}
		m = append(m, e("a2a", a2a))
	}
	if g.Reviews != nil {
		reviews := render.Map{e("enabled", g.Reviews.Enabled)}
		if g.Reviews.Audience != "" {
			reviews = append(reviews, e("audience", g.Reviews.Audience))
		}
		if len(g.Reviews.AllowedCallers) > 0 {
			reviews = append(reviews, e("allowedCallers", g.Reviews.AllowedCallers))
		}
		m = append(m, e("reviews", reviews))
	}
	return m
}

// managerOAuth is a manager's OAuth section on the 3 chart line, where the
// template carries no global identity block: the resource server's public base
// URL under the gateway, the platform's Dex client and its Secret, the
// platform client id as the trusted audience.
func (in *Input) managerOAuth(path string) render.Map {
	return render.Map{
		e("baseURL", "https://"+in.host("agentgateway")+"/"+path),
		e("dex", render.Map{e("issuerURL", "https://"+in.host("dex")), e("clientID", in.Installation.MusterClientID)}),
		e("existingSecret", in.Secrets.MusterOAuth),
		e("trustedAudiences", []string{in.Installation.MusterClientID}),
	}
}

// clusterManagerValues names the installation the manager runs on and, on the
// 3 line, its OAuth section; the 4 line derives issuer, client and secret from
// the template's global identity block and the base URL from the domain.
func (in *Input) clusterManagerValues() render.Map {
	m := render.Map{e("installation", render.Map{e("name", in.Installation.Name)})}
	if in.Installation.ChartLine == "3" {
		m = append(m, e("oauth", in.managerOAuth("cluster-manager")))
	}
	return m
}

// clusterManagerNetworkPolicy is the manager's egress as the connectivity
// chart reads it: Cilium FQDN selectors for the workload clusters' API servers
// and for other egress.
func (in *Input) clusterManagerNetworkPolicy() render.Map {
	np := in.ClusterManager.NetworkPolicy
	m := render.Map{}
	if len(np.WorkloadClusterFQDNPatterns) > 0 {
		var fqdns []render.Map
		for _, p := range np.WorkloadClusterFQDNPatterns {
			fqdns = append(fqdns, render.Map{e("matchPattern", p)})
		}
		m = append(m, e("workloadClusters", render.Map{e("fqdns", fqdns)}))
	}
	if len(np.EgressFQDNs) > 0 {
		m = append(m, e("egress", render.Map{e("fqdns", np.EgressFQDNs)}))
	}
	return m
}

// componentSecrets adds the enabled components' Secrets to the platform
// extras: the gateway's generated OBO keys and its supplied Slack credentials.
func (in *Input) componentSecrets(add func(file string, f render.File), secrets map[string]string) {
	if !in.klausGatewayEnabled() {
		return
	}
	add(klausGatewayOBOSecret+".yaml", render.Secret(klausGatewayOBOSecret, platformNamespace, teamLabels,
		render.GeneratedKey("state-key", "klaus-gateway-obo-state-key", render.Base64, 32),
		render.GeneratedKey("store-key", "klaus-gateway-obo-store-key", render.Base64, 32)))
	if in.KlausGateway.Slack == nil {
		return
	}
	var keys []render.SecretKey
	for _, k := range in.slackSecretKeys() {
		keys = append(keys, render.ValueKey(k, secrets[fieldSlack+k]))
	}
	add(klausGatewaySlackSecret+".yaml", render.Secret(klausGatewaySlackSecret, platformNamespace, teamLabels, keys...))
}
