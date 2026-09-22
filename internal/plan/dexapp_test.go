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

// A plan whose Dex patch renders a list the encrypted dex-app secret patch
// on record carries as well is held, naming the file, the lists with their
// entries and the rendered clients and peers they would shadow; a list on
// one side alone, no record or a plan without a Dex patch commit.
func TestDexSecretRefusal(t *testing.T) {
	// The fixture's clients: the chart's muster key, the portals' client and
	// the kagent UI's; the Secret names are names, not values.
	const name, musterKey, portalClient, kagentClient = "maple", "muster", "backstage", "kagent"
	const musterSecret, portalSecret, kagentSecret = "dex-client-muster", "dex-client-backstage", "dex-client-kagent" // #nosec G101 -- Secret names, not values
	source := "fleet/umbra-configs:" + installations.DexSecretPatchPath(name)
	extras := installations.DexSecretList{Path: installations.DexExtraStaticClients, Entries: 9}
	peers := installations.DexSecretList{Path: installations.DexTrustedPeers, Entries: 6}
	both := &installations.Record{DexSecretLists: []installations.DexSecretList{peers, extras}, DexSecretSource: source}
	musterClient := DexClient{Client: musterKey, ID: "muster-maple", SecretRef: musterSecret}
	clients := Installation{Name: name, DexClients: []DexClient{
		musterClient,
		{Client: keyAuthenticator, TrustedPeers: []string{portalClient, "hazel-token-exchange"}},
		{ID: kagentClient, Name: "kagent-ui", SecretRef: kagentSecret},
		{ID: portalClient, Name: "Dev Portal", SecretRef: portalSecret},
	}}
	builtInOnly := Installation{Name: name, DexClients: []DexClient{musterClient}}
	peersOnly := Installation{Name: name, DexClients: []DexClient{{Client: keyAuthenticator, TrustedPeers: []string{portalClient}}}}
	cases := []struct {
		name   string
		plan   Installation
		record *installations.Record
		want   string
	}{
		{"both lists on both sides", clients, both,
			"the encrypted Dex values on record (" + source + ") carry oidc.staticClients.dexK8SAuthenticator.trustedPeers (6 entries) and oidc.extraStaticClients (9 entries), which the values merge takes whole over the plaintext patch, so the rendered clients kagent, backstage and trusted peers backstage, hazel-token-exchange would never reach Dex; carry every entry over by hand first — each client to its own Secret and a plaintext entry, each peer to the plaintext list — then drop the lists from the encrypted values"},
		{"the encrypted patch carries the extra clients alone", clients, &installations.Record{DexSecretLists: []installations.DexSecretList{extras}, DexSecretSource: source},
			"the encrypted Dex values on record (" + source + ") carry oidc.extraStaticClients (9 entries), which the values merge takes whole over the plaintext patch, so the rendered clients kagent, backstage would never reach Dex; carry every entry over by hand first — each client to its own Secret and a plaintext entry, each peer to the plaintext list — then drop the lists from the encrypted values"},
		{"the plan renders the peers alone", peersOnly, both,
			"the encrypted Dex values on record (" + source + ") carry oidc.staticClients.dexK8SAuthenticator.trustedPeers (6 entries), which the values merge takes whole over the plaintext patch, so the rendered trusted peers backstage would never reach Dex; carry every entry over by hand first — each client to its own Secret and a plaintext entry, each peer to the plaintext list — then drop the lists from the encrypted values"},
		{"built-in clients merge by key", builtInOnly, both, ""},
		{"no list in the encrypted patch", clients, &installations.Record{DexAppVersion: "3.2.2"}, ""},
		{"no record", clients, nil, ""},
		{"no Dex patch in the plan", Installation{Name: name}, both, ""},
	}
	for _, c := range cases {
		got := c.plan.DexSecretRefusal(c.record)
		if got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
		if strings.Contains(got, "  ") {
			t.Errorf("%s: %q", c.name, got)
		}
	}
}
