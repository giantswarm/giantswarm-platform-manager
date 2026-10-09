package installations

import (
	"maps"
	"slices"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
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
	if all[agentPlatformFact] != true || all["customerPortal"] != false || all["region"] != "example-region-1" || all["chartLine"] != "4" || all["hub"] != true || all["test"] != false {
		t.Fatalf("all facts: %v", all)
	}
	// Every definition takes these; each adds its own.
	common := []string{agentPlatformFact, "baseDomain", "customer", "name"}
	facts := func(own ...string) []string {
		return slices.Sorted(slices.Values(append(slices.Clone(common), own...)))
	}
	want := map[string][]string{
		AgentPlatform:     facts("chartLine", "dexAppVersion", "hub", "musterClientId", "podCertificateRequest", "portalClientSecret", "private", "provider"),
		CustomerPortal:    facts("pipeline", "provider", "region", "test"),
		ClusterMCPServers: facts("dexAppVersion"),
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

// A fact the schema shapes as an object reaches a definition pruned to the
// properties the schema names, at every depth: the cluster-mcp-servers
// definition takes the federation's connectors and nothing else of it, the
// agent-platform definition the whole federation.
func TestFactsPruneObjectsBySchema(t *testing.T) {
	r := Report{Installation: Installation{Name: fixtureInstallation}, Record: &Record{Name: fixtureInstallation, BaseDomain: "maple.acme.example.test", Customer: "acme"},
		Federation: &Federation{Hubs: []string{"aspen"}, RegistryHub: "aspen", Targets: []FederatedTarget{}, Connectors: []render.HubConnector{{Hub: "aspen", Customer: "fleet", BaseDomain: "aspen.fleet.test", First: true}}}}
	all := r.Facts()
	servers, _ := FindCapability(ClusterMCPServers)
	facts, err := servers.Facts(all)
	if err != nil {
		t.Fatal(err)
	}
	fed, _ := facts["federation"].(map[string]any)
	conns, _ := fed["connectors"].([]any)
	if len(fed) != 1 || len(conns) != 1 {
		t.Fatalf("cluster-mcp-servers takes the connectors alone: %v", facts["federation"])
	}
	if entry, _ := conns[0].(map[string]any); entry["hub"] != "aspen" || entry["customer"] != "fleet" || entry["baseDomain"] != "aspen.fleet.test" || entry["first"] != true {
		t.Errorf("the connector's facts: %v", conns[0])
	}
	platform, _ := FindCapability(AgentPlatform)
	if facts, err = platform.Facts(all); err != nil {
		t.Fatal(err)
	}
	fed, _ = facts["federation"].(map[string]any)
	if fed["hubs"] == nil || fed["registryHub"] != "aspen" || fed["targets"] == nil || fed["connectors"] == nil {
		t.Errorf("agent-platform takes the whole federation: %v", facts["federation"])
	}
}
