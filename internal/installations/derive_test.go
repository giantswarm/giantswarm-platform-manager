package installations

import (
	"slices"
	"testing"
)

// The fixture's names: the fleet's hub, an organisation's aggregator and its sibling.
const (
	fixtureHub        = "aspen"
	fixtureAggregator = "linden"
	fixtureSibling    = "rowanberry"
)

// The portals on record derive an installation's portals, hubs and targets:
// a portal lists the installations it signs into; its cluster-token broker
// is the hub of every other listed installation and brokers for them all.
func TestDeriveFromPortals(t *testing.T) {
	reg := &Registry{Installations: []Installation{
		{Name: fixtureHub, Customer: "fleet", BaseDomain: "aspen.fleet.test", Hub: true},
		{Name: fixtureAggregator, Customer: "umbra", BaseDomain: "linden.umbra.test", Repositories: Repositories{Configs: "fleet/umbra-configs", ManagementClusters: "fleet/umbra-management-clusters"}},
		{Name: fixtureSibling, Customer: "umbra", BaseDomain: "rowanberry.umbra.test", Repositories: Repositories{Configs: "fleet/umbra-configs", ManagementClusters: "fleet/umbra-management-clusters"}},
	}}
	reports := []Report{
		{Installation: reg.Installations[0], Record: &Record{Name: fixtureHub, BaseDomain: "aspen.fleet.test"}, Readable: true},
		{Installation: reg.Installations[1], Record: &Record{Name: fixtureAggregator, BaseDomain: "linden.umbra.test"}, Readable: true},
		{Installation: reg.Installations[2], Record: &Record{Name: fixtureSibling, BaseDomain: "rowanberry.umbra.test", Private: true}, Readable: true},
	}
	portals := []Portal{
		{Host: fixtureHub, Customer: "fleet", Domain: "portal.fleet.test", Broker: "", Installations: []string{fixtureHub, fixtureAggregator, fixtureSibling}},
		{Host: fixtureAggregator, Customer: "umbra", Domain: "portal.linden.umbra.test", Broker: fixtureAggregator, Installations: []string{fixtureAggregator, fixtureSibling}},
	}
	_ = reg
	targets := reports[1].derivePortals(portals)
	if !slices.Equal(reports[2].derivePortals(portals), nil) {
		t.Fatalf("birch brokers for nobody")
	}
	linden, birch := reports[1], reports[2]
	if len(linden.Portals) != 2 || linden.Portals[1].Customer != "umbra" || len(linden.Federation.Hubs) != 0 || !slices.Equal(targets, []string{fixtureSibling}) {
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
}
