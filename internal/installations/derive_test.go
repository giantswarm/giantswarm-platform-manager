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
		{Name: fixtureHub, Customer: "fleet", BaseDomain: "aspen.fleet.test", Hub: true},
		{Name: fixtureAggregator, Customer: fixtureCustomer, BaseDomain: "linden.umbra.test", Repositories: Repositories{Configs: "fleet/umbra-configs", ManagementClusters: "fleet/umbra-management-clusters"}},
		{Name: fixtureSibling, Customer: fixtureCustomer, BaseDomain: "rowanberry.umbra.test", Repositories: Repositories{Configs: "fleet/umbra-configs", ManagementClusters: "fleet/umbra-management-clusters"}},
	}}
	reports := []Report{
		{Installation: reg.Installations[0], Record: &Record{Name: fixtureHub, BaseDomain: "aspen.fleet.test"}, Readable: true},
		{Installation: reg.Installations[1], Record: &Record{Name: fixtureAggregator, BaseDomain: "linden.umbra.test"}, Readable: true},
		{Installation: reg.Installations[2], Record: &Record{Name: fixtureSibling, BaseDomain: "rowanberry.umbra.test", Private: true}, Readable: true},
	}
	portals := []Portal{
		{Host: fixtureHub, Customer: "fleet", Domain: "portal.fleet.test", ClientID: fixtureHubClientID, Broker: "", Installations: []string{fixtureHub, fixtureAggregator, fixtureSibling}, Tunnelled: []string{fixtureSibling}},
		{Host: fixtureAggregator, Customer: fixtureCustomer, Domain: "portal.linden.umbra.test", ClientID: render.PortalDexClientID, Broker: fixtureAggregator, Installations: []string{fixtureAggregator, fixtureSibling}, PlatformProxied: []string{fixtureAggregator, fixtureSibling}},
	}
	_ = reg
	targets := reports[1].derivePortals(portals)
	if !slices.Equal(reports[2].derivePortals(portals), nil) {
		t.Fatalf("birch brokers for nobody")
	}
	linden, birch := reports[1], reports[2]
	if len(linden.Portals) != 2 || linden.Portals[1].Customer != fixtureCustomer || len(linden.Federation.Hubs) != 0 || !slices.Equal(targets, []string{fixtureSibling}) ||
		linden.Portals[0].ClientID != fixtureHubClientID || linden.Portals[1].ClientID != render.PortalDexClientID {
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

// fixtureHubClientID is the opaque id of the hub portal's Dex client on record.
const fixtureHubClientID = "Yx7hub0portal0client0id0on0record0"

// A portal's client id is the extra static client of its host's dex-app
// configmap patch that redirects to the portal; a patch without one, or
// without extra static clients at all, names none.
func TestDexClientByRedirectURI(t *testing.T) {
	uri := render.PortalRedirectURI("portal.fleet.test", fixtureHub)
	patch := "oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster, key: secret}\n  extraStaticClients:\n" +
		"    - id: kagent\n      name: kagent-ui\n      redirectURIs:\n        - https://kagent.aspen.fleet.test/oauth2/callback\n" +
		"    - id: " + fixtureHubClientID + "\n      name: Dev Portal\n      redirectURIs:\n        - " + uri + "\n      secretRef: {name: dex-client-backstage, key: secret}\n"
	if id, err := dexClientByRedirectURI(patch, uri); err != nil || id != fixtureHubClientID {
		t.Fatalf("matched by the redirect URI: %q, %v", id, err)
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

// The portals' audiences an installation trusts today are its patch's
// trustedAudiences without the ones the definition renders itself, each once;
// a patch without the key names none.
func TestPortalAudiencesOf(t *testing.T) {
	own := []string{"dex-k8s-authenticator", "kagent", "backstage", "muster-linden", fixtureHub + "-token-exchange"}
	// The four lists on record, each with an id of its own beside the shared ones.
	patch := "muster:\n  muster:\n    oauth:\n      server:\n        trustedAudiences:\n          - dex-k8s-authenticator\n          - " + fixtureHubClientID + "\n          - backstage\n          - muster-linden\n          - local-dev-client\n          - " + fixtureHubClientID + "\n" +
		"kagent:\n  oauth2-proxy:\n    extraArgs:\n      oidc-extra-audience: dex-k8s-authenticator,kagent, " + fixtureHubClientID + ",extra-only-client # gitleaks:allow\n" +
		"agent-platform-mcps:\n  agentgateway:\n    jwt:\n      extraProviders:\n        - issuer: https://dex.linden.umbra.test\n          audiences: [backstage, edge-only-client, local-dev-client]\n"
	dexPatch := "oidc:\n  staticClients:\n    dexK8SAuthenticator:\n      trustedPeers:\n        - " + fixtureHubClientID + "\n        - peer-only-client\n        - " + fixtureHub + "-token-exchange\n        - backstage\n"
	want := []string{fixtureHubClientID, "local-dev-client", "extra-only-client", "edge-only-client", "peer-only-client"}
	if ids, err := portalAudiencesOf(patch, dexPatch, own); err != nil || !slices.Equal(ids, want) {
		t.Fatalf("the union of the four lists: %v, %v", ids, err)
	}
	if ids, err := portalAudiencesOf("", dexPatch, own); err != nil || !slices.Equal(ids, []string{fixtureHubClientID, "peer-only-client"}) {
		t.Fatalf("the trusted peers alone: %v, %v", ids, err)
	}
	if ids, err := portalAudiencesOf("kagent:\n  oauth2-proxy:\n    extraArgs:\n      oidc-extra-audience: [dex-k8s-authenticator, list-client]\n", "", own); err != nil || !slices.Equal(ids, []string{"list-client"}) {
		t.Fatalf("a hand-written list of extra audiences: %v, %v", ids, err)
	}
	if ids, err := portalAudiencesOf("muster:\n  muster:\n    resources: {}\n", "", own); err != nil || len(ids) != 0 {
		t.Fatalf("no list on record names none: %v, %v", ids, err)
	}
	if _, err := portalAudiencesOf("muster: [", "", own); err == nil || !strings.Contains(err.Error(), "agent-platform/") {
		t.Fatalf("a platform patch that is no YAML is an error naming it: %v", err)
	}
	if _, err := portalAudiencesOf("", "oidc: [", own); err == nil || !strings.Contains(err.Error(), "dex-app/") {
		t.Fatalf("a dex patch that is no YAML is an error naming it: %v", err)
	}
}

// A portal's cluster entry at the tunnel's Service on its host marks the
// installation as reached through the tunnel; one at its API does not. The
// installations its agent-platform section lists are the ones whose platform
// it proxies, whatever the entry carries.
func TestPortalConfigTunnelled(t *testing.T) {
	appConfig := "app:\n  baseUrl: https://portal.aspen.fleet.test\ngs:\n  installations:\n    linden: {}\n    rowanberry: {}\n" +
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
