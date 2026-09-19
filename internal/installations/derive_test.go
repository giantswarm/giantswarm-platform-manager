package installations

import (
	"slices"
	"testing"
)

// The portals on record derive an installation's portals, hubs and targets:
// a portal lists the installations it signs into; its cluster-token broker
// is the hub of every other listed installation and brokers for them all.
func TestDeriveFromPortals(t *testing.T) {
	reg := &Registry{Installations: []Installation{
		{Name: "hazel", Customer: "example", BaseDomain: "hazel.example.test", Hub: true},
		{Name: "maple", Customer: "acme", BaseDomain: "maple.acme.test", Repositories: Repositories{Configs: "example/acme-configs", ManagementClusters: "example/acme-management-clusters"}},
		{Name: "birch", Customer: "acme", BaseDomain: "birch.acme.test", Repositories: Repositories{Configs: "example/acme-configs", ManagementClusters: "example/acme-management-clusters"}},
	}}
	reports := []Report{
		{Installation: reg.Installations[0], Record: &Record{Name: "hazel", BaseDomain: "hazel.example.test"}, Readable: true},
		{Installation: reg.Installations[1], Record: &Record{Name: "maple", BaseDomain: "maple.acme.test"}, Readable: true},
		{Installation: reg.Installations[2], Record: &Record{Name: "birch", BaseDomain: "birch.acme.test", Private: true}, Readable: true},
	}
	portals := []Portal{
		{Host: "hazel", Customer: "example", Domain: "portal.example.test", Broker: "", Installations: []string{"hazel", "maple", "birch"}},
		{Host: "maple", Customer: "acme", Domain: "portal.maple.acme.test", Broker: "maple", Installations: []string{"maple", "birch"}},
	}
	_ = reg
	targets := reports[1].derivePortals(portals)
	if !slices.Equal(reports[2].derivePortals(portals), nil) {
		t.Fatalf("birch brokers for nobody")
	}
	maple, birch := reports[1], reports[2]
	if len(maple.Portals) != 2 || maple.Portals[1].Customer != "acme" || len(maple.Federation.Hubs) != 0 || !slices.Equal(targets, []string{"birch"}) {
		t.Fatalf("maple: %+v %+v %v", maple.Portals, maple.Federation, targets)
	}
	if !slices.Equal(birch.Federation.Hubs, []string{"maple"}) || len(birch.Portals) != 2 {
		t.Fatalf("birch: %+v %+v", birch.Portals, birch.Federation)
	}
	facts := maple.Facts()
	if facts["portals"] == nil || facts["federation"] == nil {
		t.Fatalf("facts: %v", facts)
	}
	if !maple.Readable {
		t.Fatalf("maple readable: %v", maple.Errors)
	}
}
