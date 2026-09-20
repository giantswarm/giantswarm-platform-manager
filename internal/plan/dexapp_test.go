package plan

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// A plan whose Dex patch declares a client with a referenced Secret is held
// where the dex-app on record is older than 3.2.2, naming the version, the
// file it was read from and the kustomization to pin it in; 3.2.2 and later
// commit, so does a record without a version (the report says why) and a
// plan without a referenced client.
func TestDexAppRefusal(t *testing.T) {
	const name, fleetDexApp = "maple", "2.2.3"
	referenced := Installation{Name: name, DexClients: []DexClient{{ID: "backstage", SecretRef: "dex-client-backstage"}}} // #nosec G101 -- a Secret name, not a value
	source := "fleet/management-cluster-bases:" + installations.DexAppBasePath
	cases := []struct {
		name    string
		plan    Installation
		record  *installations.Record
		refused bool
	}{
		{"the fleet's base before the referenced secrets", referenced, &installations.Record{DexAppVersion: fleetDexApp, DexAppSource: source}, true},
		{"a pin with the referenced secrets but not next to an inline one", referenced, &installations.Record{DexAppVersion: "3.2.1", DexAppSource: source}, true},
		{"a pre-release of the version that takes them", referenced, &installations.Record{DexAppVersion: "3.2.2-rc.1", DexAppSource: source}, true},
		{"the version that takes them", referenced, &installations.Record{DexAppVersion: "3.2.2", DexAppSource: source}, false},
		{"a later version", referenced, &installations.Record{DexAppVersion: "v3.3.0", DexAppSource: source}, false},
		{"no version on record", referenced, &installations.Record{}, false},
		{"no record", referenced, nil, false},
		{"a version that is no semantic version", referenced, &installations.Record{DexAppVersion: "latest", DexAppSource: source}, false},
		{"no referenced client in the plan", Installation{Name: name, DexClients: []DexClient{{ID: "kagent", Public: true}}}, &installations.Record{DexAppVersion: fleetDexApp, DexAppSource: source}, false},
		{"no Dex patch in the plan", Installation{Name: name}, &installations.Record{DexAppVersion: fleetDexApp, DexAppSource: source}, false},
	}
	for _, c := range cases {
		got := c.plan.DexAppRefusal(c.record)
		if (got != "") != c.refused {
			t.Errorf("%s: %q, want refused %v", c.name, got, c.refused)
			continue
		}
		if !c.refused {
			continue
		}
		want := "dex-app " + c.record.DexAppVersion + " on record (" + source + "): the referenced Dex client secrets need dex-app 3.2.2 or later; pin it in management-clusters/" + name + "/collections/kustomization.yaml first"
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
		if strings.Contains(got, "  ") {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}
