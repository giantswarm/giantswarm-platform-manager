package installations

import (
	"slices"
	"strings"
	"testing"
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
		{Host: fixtureHub, Customer: "fleet", Domain: "portal.fleet.test", Broker: "", Installations: []string{fixtureHub, fixtureAggregator, fixtureSibling}, Tunnelled: []string{fixtureSibling}},
		{Host: fixtureAggregator, Customer: fixtureCustomer, Domain: "portal.linden.umbra.test", Broker: fixtureAggregator, Installations: []string{fixtureAggregator, fixtureSibling}},
	}
	_ = reg
	targets := reports[1].derivePortals(portals)
	if !slices.Equal(reports[2].derivePortals(portals), nil) {
		t.Fatalf("birch brokers for nobody")
	}
	linden, birch := reports[1], reports[2]
	if len(linden.Portals) != 2 || linden.Portals[1].Customer != fixtureCustomer || len(linden.Federation.Hubs) != 0 || !slices.Equal(targets, []string{fixtureSibling}) {
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
}

// A portal's cluster entry at the tunnel's Service on its host marks the
// installation as reached through the tunnel; one at its API does not.
func TestPortalConfigTunnelled(t *testing.T) {
	appConfig := "app:\n  baseUrl: https://portal.aspen.fleet.test\ngs:\n  installations:\n    linden: {}\n    rowanberry: {}\n" +
		"kubernetes:\n  clusterLocatorMethods:\n    - type: config\n      clusters:\n" +
		"        - name: linden\n          url: https://happaapi.linden.umbra.test\n" +
		"        - name: rowanberry\n          url: https://kubernetes-rowanberry.agent-platform.svc.cluster.local:8443\n"
	values := "backstage:\n  appConfig: |\n" + indent(appConfig, "    ")
	cm := "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n" + indent(values, "    ")
	cfg, err := parsePortalConfig(cm)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tunnelled[fixtureSibling] || cfg.Tunnelled[fixtureAggregator] || len(cfg.Tunnelled) != 1 {
		t.Fatalf("tunnelled: %v", cfg.Tunnelled)
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
