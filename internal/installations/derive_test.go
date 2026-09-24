package installations

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The fixture's names: the fleet's hub, an organisation and its aggregator and sibling.
const (
	fixtureHub        = "aspen"
	fixtureFleet      = "fleet"
	fixtureCustomer   = "umbra"
	fixtureAggregator = "linden"
	fixtureSibling    = "rowanberry"
)

// The portals on record derive an installation's portals, hubs and targets:
// a portal lists the installations it signs into; its cluster-token broker
// is the hub of every other listed installation and brokers for them all. An
// installation a portal reaches through the tunnel on its host is private.
func TestDeriveFromPortals(t *testing.T) {
	reg := &Registry{Installations: []Installation{
		{Name: fixtureHub, Customer: fixtureFleet, BaseDomain: "aspen.fleet.test", Hub: true},
		{Name: fixtureAggregator, Customer: fixtureCustomer, BaseDomain: "linden.umbra.test", Repositories: Repositories{Configs: "fleet/umbra-configs", ManagementClusters: "fleet/umbra-management-clusters"}},
		{Name: fixtureSibling, Customer: fixtureCustomer, BaseDomain: "rowanberry.umbra.test", Repositories: Repositories{Configs: "fleet/umbra-configs", ManagementClusters: "fleet/umbra-management-clusters"}},
	}}
	reports := []Report{
		{Installation: reg.Installations[0], Record: &Record{Name: fixtureHub, BaseDomain: "aspen.fleet.test"}, Readable: true},
		{Installation: reg.Installations[1], Record: &Record{Name: fixtureAggregator, BaseDomain: "linden.umbra.test"}, Readable: true},
		{Installation: reg.Installations[2], Record: &Record{Name: fixtureSibling, BaseDomain: "rowanberry.umbra.test", Private: true}, Readable: true},
	}
	portals := []Portal{
		{Host: fixtureHub, Customer: fixtureFleet, Domain: "portal.fleet.test", ClientID: fixtureHubClientID, Broker: "", Installations: []string{fixtureHub, fixtureAggregator, fixtureSibling}, Tunnelled: []string{fixtureSibling}, HandKept: true},
		{Host: fixtureAggregator, Customer: fixtureCustomer, Domain: "portal.linden.umbra.test", ClientID: render.PortalDexClientID, Broker: fixtureAggregator, Installations: []string{fixtureAggregator, fixtureSibling}, PlatformProxied: []string{fixtureAggregator, fixtureSibling}, GrafanaWired: true},
	}
	_ = reg
	targets := reports[1].derivePortals(portals)
	if !slices.Equal(reports[2].derivePortals(portals), nil) {
		t.Fatalf("birch brokers for nobody")
	}
	linden, birch := reports[1], reports[2]
	if len(linden.Portals) != 2 || linden.Portals[1].Customer != fixtureCustomer || len(linden.Federation.Hubs) != 0 || !slices.Equal(targets, []string{fixtureSibling}) ||
		linden.Portals[0].ClientID != fixtureHubClientID || linden.Portals[1].ClientID != render.PortalDexClientID ||
		!linden.Portals[0].HandKept || linden.Portals[1].HandKept || linden.Portals[0].GrafanaWired || !linden.Portals[1].GrafanaWired {
		t.Fatalf("linden: %+v %+v %v", linden.Portals, linden.Federation, targets)
	}
	if !slices.Equal(birch.Federation.Hubs, []string{fixtureAggregator}) || len(birch.Portals) != 2 {
		t.Fatalf("birch: %+v %+v", birch.Portals, birch.Federation)
	}
	facts := linden.Facts()
	if facts["portals"] == nil || facts["federation"] == nil {
		t.Fatalf("facts: %v", facts)
	}
	if !linden.Readable {
		t.Fatalf("maple readable: %v", linden.Errors)
	}
	if !tunnelled(portals, fixtureSibling) || tunnelled(portals, fixtureAggregator) || tunnelled(portals, fixtureHub) {
		t.Fatalf("private: the portal reaches %s through the tunnel and nobody else", fixtureSibling)
	}
	if !proxied(portals, fixtureAggregator, fixtureSibling) || proxied(portals, fixtureHub, fixtureSibling) || proxied(portals, fixtureAggregator, fixtureHub) {
		t.Fatalf("proxied: the portal %s brokers for lists %s under its agent-platform section; the hub's portal lists nobody", fixtureAggregator, fixtureSibling)
	}
}

// The portal hosted on an installation shows the installations its record
// lists besides its own, by name: each with the registry's base domain,
// region and pipeline (the record's where the registry has none) and the
// providers the record lists (the registry's one where it lists none); a
// name the registry does not know is set apart; an installation without a
// portal hosts none.
func TestHostedPortalFollowsTheRecord(t *testing.T) {
	const (
		capa, capz, stable, euCentral, spruce       = "capa", "capz", "stable", "eu-central-1", "spruce"
		aspenDomain, lindenDomain, rowanberryDomain = "aspen.fleet.test", "linden.umbra.test", "rowanberry.umbra.test"
	)
	reg := &Registry{Installations: []Installation{
		{Name: fixtureHub, Customer: fixtureFleet, BaseDomain: aspenDomain, Provider: capa, Region: "eu-west-1", Pipeline: stable, Hub: true},
		{Name: fixtureAggregator, Customer: fixtureCustomer, BaseDomain: lindenDomain, Provider: capz, Pipeline: "testing"},
		{Name: fixtureSibling, Customer: fixtureCustomer, Provider: capa, Region: euCentral, Pipeline: stable},
	}}
	portals := []Portal{
		{Host: fixtureHub, Installations: []string{fixtureHub, fixtureAggregator, spruce, fixtureSibling}, Tunnelled: []string{fixtureSibling}, Entries: map[string]portalEntry{
			fixtureAggregator: {BaseDomain: lindenDomain, Providers: []string{capz, "capv"}, Region: "westeurope"},
			fixtureSibling:    {BaseDomain: rowanberryDomain, Pipeline: "stable-testing"},
			spruce:            {BaseDomain: "spruce.example.test"},
		}},
		{Host: fixtureAggregator, Installations: []string{fixtureAggregator}},
	}
	hosted := reg.hostedPortal(portals, fixtureHub)
	if hosted == nil || !slices.Equal(hosted.Unknown, []string{spruce}) || len(hosted.Installations) != 2 {
		t.Fatalf("hosted: %+v", hosted)
	}
	linden, rowanberry := hosted.Installations[0], hosted.Installations[1]
	if linden.Name != fixtureAggregator || linden.BaseDomain != lindenDomain || !slices.Equal(linden.Providers, []string{capz, "capv"}) || linden.Region != "westeurope" || linden.Pipeline != "testing" {
		t.Errorf("the record's providers and region, the registry's pipeline: %+v", linden)
	}
	if rowanberry.Name != fixtureSibling || rowanberry.BaseDomain != rowanberryDomain || !slices.Equal(rowanberry.Providers, []string{capa}) || rowanberry.Region != euCentral || rowanberry.Pipeline != stable || !rowanberry.Private || linden.Private {
		t.Errorf("the record's base domain, the registry's provider, region and pipeline, the tunnel: %+v %+v", rowanberry, linden)
	}
	if own := reg.hostedPortal(portals, fixtureAggregator); own == nil || len(own.Installations) != 0 || len(own.Unknown) != 0 {
		t.Errorf("a portal showing its own installation alone: %+v", own)
	}
	if reg.hostedPortal(portals, fixtureSibling) != nil {
		t.Error("an installation without a portal hosts none")
	}
	// The definition's inputs: the set as federation.installations, region only where known; an unknown name refuses.
	in, err := portalFederation(Report{Hosted: &HostedPortal{Installations: hosted.Installations}})
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := in["installations"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["name"] != fixtureAggregator || entries[1].(map[string]any)["pipeline"] != stable || entries[0].(map[string]any)["agentPlatform"] != false || entries[1].(map[string]any)["private"] != true || entries[0].(map[string]any)["private"] != false {
		t.Errorf("record inputs: %v", in)
	}
	if _, err := portalFederation(Report{Hosted: hosted}); err == nil || !strings.Contains(err.Error(), spruce) {
		t.Errorf("an unknown name refuses by name: %v", err)
	}
	if in, err := portalFederation(Report{}); err != nil || in != nil {
		t.Errorf("no portal, no inputs: %v %v", in, err)
	}
}

// A target's hubs are the brokers of the portals that list it, each once and
// never nil; of them, the hubs of one organisation are ordered as the
// connectors the target's Dex registers for them are named: the registry's
// hub first where it is one, then by name — whatever order the portals came
// in. Another organisation's hub is not among them.
func TestOrganisationHubs(t *testing.T) {
	const secondHub, thirdHub, otherHub = "zelkova", "birch", "linden"
	reg := &Registry{Installations: []Installation{
		{Name: secondHub, Customer: fixtureFleet},
		{Name: fixtureHub, Customer: fixtureFleet, Hub: true},
		{Name: thirdHub, Customer: fixtureFleet},
		{Name: otherHub, Customer: fixtureCustomer},
		{Name: fixtureSibling, Customer: fixtureCustomer},
	}}
	portals := []Portal{
		{Host: secondHub, Broker: secondHub, Installations: []string{secondHub, fixtureSibling}},
		{Host: otherHub, Broker: otherHub, Installations: []string{otherHub, fixtureSibling}},
		{Host: fixtureHub, Broker: fixtureHub, Installations: []string{fixtureHub, secondHub, fixtureSibling}},
		{Host: thirdHub, Broker: thirdHub, Installations: []string{fixtureSibling}},
		{Host: "mirror", Broker: fixtureHub, Installations: []string{fixtureSibling}},
	}
	hubs := hubsOf(portals, fixtureSibling)
	if !slices.Equal(hubs, []string{secondHub, otherHub, fixtureHub, thirdHub}) {
		t.Fatalf("the sibling's hubs, in the portals' order, each once: %v", hubs)
	}
	if got := hubsOf(portals, secondHub); !slices.Equal(got, []string{fixtureHub}) {
		t.Fatalf("%s's hubs: %v (its own portal brokers for nobody but its targets)", secondHub, got)
	}
	if got := hubsOf(portals, otherHub); got == nil || len(got) != 0 {
		t.Fatalf("an installation nobody brokers into has an empty list, never nil: %#v", got)
	}
	if got := reg.organisationHubs(hubs, fixtureFleet); !slices.Equal(got, []string{fixtureHub, thirdHub, secondHub}) {
		t.Fatalf("the fleet's hubs, the registry's hub first, then by name: %v", got)
	}
	if got := reg.organisationHubs(hubs, fixtureCustomer); !slices.Equal(got, []string{otherHub}) {
		t.Fatalf("%s's hubs: %v", fixtureCustomer, got)
	}
}

// fixtureHubClientID is the opaque id of the hub portal's Dex client on record.
const fixtureHubClientID = "Yx7hub0portal0client0id0on0record0"

// A portal's client id is the extra static client of its host's dex-app
// configmap patch that redirects to the portal — its own client ahead of the
// definition's backstage client that redirects there too, in either order,
// backstage where it is the only one; a patch without one, or without extra
// static clients at all, names none.
func TestDexClientByRedirectURI(t *testing.T) {
	uri := render.PortalRedirectURI("portal.fleet.test", fixtureHub)
	patch := "oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster, key: secret}\n  extraStaticClients:\n" +
		"    - id: kagent\n      name: kagent-ui\n      redirectURIs:\n        - https://kagent.aspen.fleet.test/oauth2/callback\n" +
		"    - id: " + fixtureHubClientID + "\n      name: Dev Portal\n      redirectURIs:\n        - " + uri + "\n      secretRef: {name: dex-client-backstage, key: secret}\n"
	if id, err := dexClientByRedirectURI(patch, uri); err != nil || id != fixtureHubClientID {
		t.Fatalf("matched by the redirect URI: %q, %v", id, err)
	}
	backstage := "    - id: " + render.PortalDexClientID + "\n      name: Dev Portal\n      redirectURIs:\n        - " + uri + "\n"
	own := patch[strings.Index(patch, "    - id: "+fixtureHubClientID):]
	for name, clients := range map[string]string{"backstage first": backstage + own, "backstage last": own + backstage} {
		p := "oidc:\n  extraStaticClients:\n" + clients
		if id, err := dexClientByRedirectURI(p, uri); err != nil || id != fixtureHubClientID {
			t.Errorf("%s: %q, %v, want the portal's own client", name, id, err)
		}
	}
	if id, err := dexClientByRedirectURI("oidc:\n  extraStaticClients:\n"+backstage, uri); err != nil || id != render.PortalDexClientID {
		t.Errorf("the definition's client alone: %q, %v", id, err)
	}
	if id, err := dexClientByRedirectURI(patch, render.PortalRedirectURI("other.fleet.test", fixtureHub)); err != nil || id != "" {
		t.Fatalf("another portal's redirect URI names no client: %q, %v", id, err)
	}
	if id, err := dexClientByRedirectURI("oidc:\n  staticClients:\n    dexK8SAuthenticator:\n      trustedPeers: ["+fixtureHubClientID+"]\n", uri); err != nil || id != "" {
		t.Fatalf("a trusted peer is no client of the portal: %q, %v", id, err)
	}
	if _, err := dexClientByRedirectURI("oidc: [", uri); err == nil {
		t.Fatal("a patch that is no YAML is an error")
	}
}

// A portal's cluster entry at the tunnel's Service on its host marks the
// installation as reached through the tunnel; one at its API does not. The
// installations its agent-platform section lists are the ones whose platform
// it proxies, whatever the entry carries. A literal extension list marks the
// portal hand-kept.
func TestPortalConfigTunnelled(t *testing.T) {
	appConfig := "app:\n  baseUrl: https://portal.aspen.fleet.test\n  extensions:\n    - page:gs/clusters\n    - entity-card:catalog/labels: false\ngs:\n  installations:\n    linden: {}\n    rowanberry: {}\n" +
		"kubernetes:\n  clusterLocatorMethods:\n    - type: config\n      clusters:\n" +
		"        - name: linden\n          url: https://happaapi.linden.umbra.test\n" +
		"        - name: rowanberry\n          url: https://kubernetes-rowanberry.agent-platform.svc.cluster.local:8443\n" +
		"agentPlatform:\n  kagent:\n    installations:\n      aspen: {}\n" +
		"      rowanberry:\n        apiBaseUrl: https://agentgateway-rowanberry.agent-platform.svc.cluster.local:8443\n"
	values := "backstage:\n  appConfig: |\n" + indent(appConfig, "    ")
	cm := "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n" + indent(values, "    ")
	cfg, err := parsePortalConfig(cm)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tunnelled[fixtureSibling] || cfg.Tunnelled[fixtureAggregator] || len(cfg.Tunnelled) != 1 {
		t.Fatalf("tunnelled: %v", cfg.Tunnelled)
	}
	if !cfg.PlatformProxied[fixtureSibling] || !cfg.PlatformProxied[fixtureHub] || cfg.PlatformProxied[fixtureAggregator] || len(cfg.PlatformProxied) != 2 {
		t.Fatalf("platform proxied: %v", cfg.PlatformProxied)
	}
	if !cfg.HandKept {
		t.Fatal("a literal app.extensions list marks the portal hand-kept")
	}
}

// A portal whose app.extensions is the shared include — what the
// customer-portal definition renders — is not hand-kept, nor is one without
// the key: only a list of the portal's own is. Its Grafana plugin is wired
// where its proxy carries the plugin's endpoint, whatever else the proxy
// carries; another endpoint alone, or no proxy, is not.
func TestPortalConfigHandKept(t *testing.T) {
	for name, extensions := range map[string]string{
		"the shared include": "  extensions:\n    $include: shared-config.yaml#extensions\n",
		"no extensions":      "",
	} {
		appConfig := "app:\n  baseUrl: https://portal.linden.umbra.test\n" + extensions + "gs:\n  installations:\n    linden: {}\n"
		values := "backstage:\n  appConfig: |\n" + indent(appConfig, "    ")
		cm := "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n" + indent(values, "    ")
		cfg, err := parsePortalConfig(cm)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.HandKept || cfg.GrafanaWired {
			t.Errorf("%s: read hand-kept %v, Grafana wired %v", name, cfg.HandKept, cfg.GrafanaWired)
		}
	}
	grafana := "proxy:\n  endpoints:\n    " + render.PortalGrafanaProxy + ":\n      target: https://grafana.linden.umbra.test/\n      headers:\n        Authorization: Bearer $${GRAFANA_TOKEN}\n"
	circleci := "proxy:\n  endpoints:\n    /circleci/api:\n      target: https://circleci.com/api/v1.1\n"
	for name, tc := range map[string]struct {
		proxy string
		wired bool
	}{
		"the plugin's endpoint":     {grafana, true},
		"the endpoint among others": {grafana + "    /circleci/api:\n      target: https://circleci.com/api/v1.1\n", true},
		"another endpoint alone":    {circleci, false},
	} {
		appConfig := "app:\n  baseUrl: https://portal.linden.umbra.test\ngs:\n  installations:\n    linden: {}\n" + tc.proxy
		values := "backstage:\n  appConfig: |\n" + indent(appConfig, "    ")
		cm := "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n" + indent(values, "    ")
		cfg, err := parsePortalConfig(cm)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cfg.GrafanaWired != tc.wired {
			t.Errorf("%s: read Grafana wired %v, want %v", name, cfg.GrafanaWired, tc.wired)
		}
		if cfg.HandKeptChat {
			t.Errorf("%s: read a hand-kept chat without an aiChat block", name)
		}
	}
	// The chat is hand-kept where the app-config carries the aiChat block, whatever it holds.
	for name, chat := range map[string]string{
		"a full block": "aiChat:\n  anthropic:\n    apiKey: " + dollar + dollar + "{ANTHROPIC_API_KEY}\n  model: claude-opus-4-8\n",
		"an empty one": "aiChat: {}\n",
	} {
		appConfig := "app:\n  baseUrl: https://portal.linden.umbra.test\ngs:\n  installations:\n    linden: {}\n" + chat
		values := "backstage:\n  appConfig: |\n" + indent(appConfig, "    ")
		cm := "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n" + indent(values, "    ")
		cfg, err := parsePortalConfig(cm)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !cfg.HandKeptChat {
			t.Errorf("%s: an aiChat block marks the chat hand-kept", name)
		}
	}
}

// A portal's chart line is the ref its directory kustomization patches onto
// the fleet base's backstage OCIRepository: the semver range of a JSON patch's
// add operation (the form the customer-portal definition writes), a tag the
// same way, or a strategic-merge patch's spec.ref; a kustomization that
// patches no ref names none, and one that is no YAML is an error.
func TestPortalChartLine(t *testing.T) {
	const base = "resources:\n  - https://github.com/giantswarm/management-cluster-bases/extras/backstage/main?ref=main\n  - app-config.yaml\n"
	ops := func(path, value string) string {
		return "patches:\n  - patch: |-\n      - op: remove\n        path: /spec/ref/tag\n      - op: add\n        path: " + path + "\n        value: \"" + value + "\"\n    target:\n      kind: OCIRepository\n      name: backstage\n      namespace: flux-giantswarm\n"
	}
	release := "  - patch: |\n      apiVersion: helm.toolkit.fluxcd.io/v2\n      kind: HelmRelease\n      metadata:\n        name: backstage\n      spec:\n        valuesFrom:\n          - kind: ConfigMap\n            name: user-values-backstage\n    target:\n      kind: HelmRelease\n      name: backstage\n"
	for _, tc := range []struct{ name, data, want string }{
		{"a bounded range", base + ops("/spec/ref/semver", ">=0.244.7 <1.0.0") + release, ">=0.244.7 <1.0.0"},
		{"a tag", base + ops("/spec/ref/tag", "0.120.0") + release, "0.120.0"},
		{"a strategic-merge patch", base + "patches:\n  - patch: |\n      apiVersion: source.toolkit.fluxcd.io/v1\n      kind: OCIRepository\n      metadata:\n        name: backstage\n      spec:\n        ref:\n          semver: '>=2.1.0 <3.0.0'\n    target:\n      kind: OCIRepository\n      name: backstage\n", ">=2.1.0 <3.0.0"},
		{"no ref patched", base + "patches:\n" + release, ""},
		{"no patches", base, ""},
	} {
		got, err := portalChartLine(tc.data)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
	if _, err := portalChartLine("patches: ["); err == nil {
		t.Fatal("a kustomization that is no YAML is an error")
	}
}

func indent(s, prefix string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if line != "" {
			b.WriteString(prefix + line)
		}
	}
	return b.String()
}
