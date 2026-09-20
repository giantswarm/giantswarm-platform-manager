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
func TestFactsPerDefinition(t *testing.T) {
	r := Report{Installation: Installation{Name: "maple", Region: "example-region-1", Pipeline: "stable"},
		Record:       &Record{Name: "maple", BaseDomain: "maple.acme.example.test", Customer: "acme", Provider: "capz", ChartLine: "4", MusterClientID: "muster-maple"},
		Capabilities: []CapabilityState{{Name: AgentPlatform, Enabled: true}, {Name: CustomerPortal, Enabled: false}}}
	all := r.Facts()
	if all["agentPlatform"] != true || all["customerPortal"] != false || all["region"] != "example-region-1" || all["chartLine"] != "4" {
		t.Fatalf("all facts: %v", all)
	}
	want := map[string][]string{
		AgentPlatform:  {"baseDomain", "chartLine", "customer", "musterClientId", "name", "podCertificateRequest", "private", "provider"},
		CustomerPortal: {"agentPlatform", "baseDomain", "customer", "name", "pipeline", "provider", "region"},
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
	if factKey("agent-platform") != "agentPlatform" || factKey("customer-portal") != "customerPortal" || factKey("x") != "x" {
		t.Errorf("factKey: %q %q", factKey("agent-platform"), factKey("customer-portal"))
	}
}
