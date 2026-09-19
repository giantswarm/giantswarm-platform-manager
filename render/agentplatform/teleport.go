package agentplatform

import (
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The hub's entries of teleport-fleet's tunnelport values. The Teleport objects
// a tunnel joins with — per tunnelled app a role pinned to the one Teleport app,
// its bot, its workload identity and one provision token per consumer; the
// trust-bundle singleton with one token per consumer — are rendered by
// teleport-fleet's own template, kubernetes/envs/prod/templates/tunnelport.yaml,
// from .Values.tunnelport. The definition renders no Teleport object: it renders
// the hub's entries of that values file — the hub as a consumer, its
// trust-bundle token and one tunnel per tunnelled app of every private target —
// and the commit step edits them into the file, every other entry kept
// (internal/plan). The singleton and the template are teleport-fleet's. Nothing
// here runs against Teleport: teleport-fleet's CI applies the chart. Shield
// reviews this file (CODEOWNERS).

const (
	teleportFleet = render.Repository("giantswarm/teleport-fleet")
	// tunnelportValues is the values file of teleport-fleet's production chart
	// the tunnelport template reads; the definition's entries are edited into
	// it, the file is never written whole.
	tunnelportValues = "kubernetes/envs/prod/values.yaml"
	// teleportCustomerLabel is the customer label every app a Giant Swarm-managed
	// installation advertises to Teleport carries — the operator's tenancy label,
	// the same on an installation of any organisation — and, with app and
	// cluster, the third label the tunnel's role pins the app on. It is not the
	// target's customer on record.
	teleportCustomerLabel = "giantswarm"
	// providerCAPA is the provider whose installations publish their
	// service-account issuer at irsa.<base domain>: the tunnel's oidc join.
	providerCAPA = "capa"
)

// serviceAccountIssuer is the hub's published service-account issuer, where
// Teleport fetches the signing keys of the tokens the tunnel's bots join with:
// on capa the IRSA discovery at irsa.<base domain>. Empty where the provider
// publishes none the definition knows; Parse refuses a private target on such
// a hub.
func (in *Input) serviceAccountIssuer() string {
	if in.Installation.Provider == providerCAPA {
		return "https://irsa." + in.Installation.BaseDomain
	}
	return ""
}

// tunnelValues renders the hub's entries of the tunnelport values into
// teleport-fleet: the hub as a consumer (its RemoteApps' ServiceAccounts live in
// the platform's namespace, its tokens join by its issuer), its trust-bundle
// token — the one the hub's tunnelport release names — and one tunnel per
// tunnelled app of every private target: the app pinned by its labels and one
// token for this hub. The SVIDs' DNS SANs are the template's, templated off the
// join attributes, so no entry carries them.
func (in *Input) tunnelValues(r *render.Result) {
	if !in.hasPrivateTarget() {
		return
	}
	hub := in.Installation.Name
	var tunnels []render.Map
	for _, t := range in.Installation.Federation.Targets {
		if !t.Private {
			continue
		}
		for _, app := range t.tunnelledApps() {
			name := t.appName(app.name)
			tunnels = append(tunnels, render.Map{
				e("name", name),
				e("appLabels", render.Map{e("app", app.name), e("cluster", t.Installation), e("customer", teleportCustomerLabel)}),
				e("tokens", []render.Map{{e("name", name+"-bot-token"), e("consumer", hub)}}),
			})
		}
	}
	values := render.Map{e("tunnelport", render.Map{
		e("consumers", render.Map{e(hub, render.Map{e("installNamespace", platformNamespace), e("issuer", in.serviceAccountIssuer())})}),
		e("trustBundle", render.Map{e("tokens", []render.Map{{e("name", trustBundleTokenName(hub)), e("consumer", hub)}})}),
		e("tunnels", tunnels),
	})}
	note := "# The hub " + hub + "'s entries of teleport-fleet's tunnelport values, rendered by giantswarm-platform-manager,\n" +
		"# agent-platform definition: the commit step edits them into " + tunnelportValues + " — the hub as a\n" +
		"# consumer, its trust-bundle token, one tunnel per tunnelled app of every private target — and keeps\n" +
		"# every other entry. The template and the trust-bundle singleton are teleport-fleet's.\n"
	r.Add(teleportFleet, tunnelportValues, render.File{Content: append([]byte(note), render.MustYAML(values)...)})
}
