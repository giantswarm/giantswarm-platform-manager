package installations

import (
	"maps"
	"slices"
	"testing"
)

// Every fact on record reaches a definition only where its schema names it
// under installation: the agent-platform definition takes the record's chart
// line and muster client id and not the registry's region; the portal takes
// region, pipeline and the agent-platform capability's enabled state and not
// the chart line. The names of the enabled states are the definitions' in
// lowerCamelCase.
// agentPlatformFact is the fact key of the agent-platform capability: on record for the installation.
const agentPlatformFact = "agentPlatform"

func TestFactsPerDefinition(t *testing.T) {
	r := Report{Installation: Installation{Name: fixtureInstallation, Region: "example-region-1", Pipeline: "stable", Hub: true},
		Record:       &Record{Name: fixtureInstallation, BaseDomain: "maple.acme.example.test", Customer: "acme", Provider: "capz", ChartLine: "4", MusterClientID: "muster-maple", DexAppVersion: pinnedDexApp},
		Capabilities: []CapabilityState{{Name: AgentPlatform, Enabled: true}, {Name: CustomerPortal, Enabled: false}}}
	all := r.Facts()
	if all[agentPlatformFact] != true || all["customerPortal"] != false || all["region"] != "example-region-1" || all["chartLine"] != "4" || all["hub"] != true {
		t.Fatalf("all facts: %v", all)
	}
	want := map[string][]string{
		AgentPlatform:  {agentPlatformFact, "baseDomain", "chartLine", "customer", "dexAppVersion", "hub", "musterClientId", "name", "podCertificateRequest", "private", "provider"},
		CustomerPortal: {agentPlatformFact, "baseDomain", "customer", "name", "pipeline", "provider", "region"},
	}
	for _, c := range Capabilities() {
		facts, err := c.Facts(all)
		if err != nil {
			t.Fatal(err)
		}
		got := slices.Sorted(maps.Keys(facts))
		if !slices.Equal(got, want[c.Name]) {
			t.Errorf("%s: facts %v, want %v", c.Name, got, want[c.Name])
		}
	}
	if factKey("agent-platform") != agentPlatformFact || factKey("customer-portal") != "customerPortal" || factKey("x") != "x" {
		t.Errorf("factKey: %q %q", factKey("agent-platform"), factKey("customer-portal"))
	}
}
