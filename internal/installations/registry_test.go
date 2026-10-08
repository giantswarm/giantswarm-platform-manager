package installations

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// The invented customer's repositories.
const (
	acmeConfigs = "example/acme-configs"
	acmeMCs     = "example/acme-management-clusters"
)

// A test installation is the hub's organisation's and not the hub; the hub
// and every customer's installation are production.
func TestTestInstallationIsTheHubsOrganisationsNotTheHub(t *testing.T) {
	const hubName, own, theirs = "oak", "umbrella", "acme"
	hub := Installation{Name: hubName, Customer: own}
	for _, tc := range []struct {
		name, customer string
		want           bool
	}{
		{"garm", own, true},
		{hubName, own, false},
		{"birch", theirs, false},
	} {
		if got := TestInstallation(tc.name, tc.customer, hub); got != tc.want {
			t.Errorf("TestInstallation(%s of %s) = %v, want %v", tc.name, tc.customer, got, tc.want)
		}
	}
}

// The catalog fixture: two installations of invented customers and a Group
// the parser skips.
const catalogFixture = `---
apiVersion: backstage.io/v1alpha1
kind: Group
metadata:
    name: acme
spec:
    type: customer
---
apiVersion: backstage.io/v1alpha1
kind: Resource
metadata:
    name: alder
    labels:
        giantswarm.io/customer: acme
        giantswarm.io/pipeline: stable
        giantswarm.io/provider: capa
        giantswarm.io/region: eu-central-1
    annotations:
        giantswarm.io/account-engineer: Ada Example
        giantswarm.io/base: acme.test
    links:
        - url: https://github.com/example/acme-management-clusters
          title: Customer management clusters (CMC)
          type: CMC
        - url: https://github.com/example/acme-configs/
          title: Customer config (CCR)
          type: CCR
        - url: https://grafana.alder.acme.test/
          title: Grafana
spec:
    owner: acme
    type: installation
---
apiVersion: backstage.io/v1alpha1
kind: Resource
metadata:
    name: willow
    labels:
        giantswarm.io/provider: capz
spec:
    owner: umbrella
    type: installation
`

func TestParseCatalog(t *testing.T) {
	got, err := parseCatalog(catalogFixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("installations: %+v", got)
	}
	alder := got[0]
	if alder.Name != "alder" || alder.Customer != "acme" || alder.Provider != "capa" || alder.Pipeline != "stable" || alder.Region != "eu-central-1" ||
		alder.BaseDomain != "alder.acme.test" || alder.AccountEngineer != "Ada Example" ||
		alder.Repositories.Configs != acmeConfigs || alder.Repositories.ManagementClusters != acmeMCs ||
		len(alder.Sources) != 1 || alder.Sources[0] != SourceCatalog {
		t.Fatalf("alder: %+v", alder)
	}
	willow := got[1]
	if willow.Customer != "umbrella" || willow.BaseDomain != "" || willow.Repositories.Known() {
		t.Fatalf("willow: %+v", willow)
	}
}

func TestParseCatalogWithoutInstallationsIsAnError(t *testing.T) {
	if _, err := parseCatalog("kind: Group\nmetadata:\n  name: acme\n"); err == nil {
		t.Fatal("a catalog without installations parsed")
	}
}

// The portal fixture: the hub's app-config ConfigMap, values as text, the
// app-config as text inside it.
const portalFixture = `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config-backstage
data:
  values: |
    backstage:
      appConfig: |
        app:
          title: Dev Portal
        gs:
          installations:
            alder:
              authProvider: oidc
              baseDomain: alder.acme.test
              pipeline: stable
              providers:
                - capa
              region: eu-central-1
            larch:
              authProvider: gs
              baseDomain: larch.example.test
              providers:
                - capa
                - capz
`

func TestParsePortal(t *testing.T) {
	cfg, err := parsePortalConfig(portalFixture)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Installations
	if len(got) != 2 || got["alder"].AuthProvider != "oidc" || got["alder"].BaseDomain != "alder.acme.test" || got["alder"].Providers[0] != "capa" ||
		got["larch"].AuthProvider != "gs" || len(got["larch"].Providers) != 2 {
		t.Fatalf("portal: %+v", got)
	}
}

func TestParsePortalWithoutTheBlockIsAnError(t *testing.T) {
	for _, data := range []string{
		"kind: ConfigMap\ndata: {}\n",
		"data:\n  values: |\n    backstage: {}\n",
		strings.Replace(portalFixture, "installations:", "other:", 1),
	} {
		if _, err := parsePortalConfig(data); err == nil {
			t.Fatalf("parsed without gs.installations: %q", data)
		}
	}
}

// The portal's facts reach a catalog installation however many installations
// only the portal lists: those grow the entries past their capacity, and a
// catalog installation the merge reaches after that growth keeps every fact.
// The portal is a map, so the order differs between runs; enough runs and
// portal-only installations make the order that loses facts certain.
func TestMergeKeepsTheFactsWhenThePortalOnlyInstallationsGrowTheEntries(t *testing.T) {
	portal := map[string]portalEntry{
		"alder":  {AuthProvider: "oidc", BaseDomain: "alder.portal.test", Pipeline: "testing", Providers: []string{"capa"}, Region: "eu-west-1"},
		"willow": {AuthProvider: "gs", BaseDomain: "willow.umbrella.test", Pipeline: "stable", Providers: []string{"capz"}, Region: "westeurope"},
	}
	for i := range 8 {
		portal[fmt.Sprintf("portal-only-%d", i)] = portalEntry{AuthProvider: "gs", BaseDomain: "portal-only.test"}
	}
	for range 10 {
		catalog, err := parseCatalog(catalogFixture)
		if err != nil {
			t.Fatal(err)
		}
		got := merge(slices.Clip(catalog), "willow", portal)
		if len(got) != 2+8 {
			t.Fatalf("installations: %d, want 10", len(got))
		}
		byName := map[string]Installation{}
		for _, inst := range got {
			byName[inst.Name] = inst
		}
		alder := byName["alder"]
		if !slices.Equal(alder.Sources, []string{SourceCatalog, SourcePortal}) || alder.AuthProvider != "oidc" ||
			alder.BaseDomain != "alder.acme.test" || alder.Pipeline != "stable" || alder.Region != "eu-central-1" || alder.Provider != "capa" {
			t.Fatalf("alder lost facts: %+v", alder)
		}
		willow := byName["willow"]
		if !slices.Equal(willow.Sources, []string{SourceCatalog, SourcePortal}) || willow.AuthProvider != "gs" ||
			willow.BaseDomain != "willow.umbrella.test" || willow.Pipeline != "stable" || willow.Region != "westeurope" || willow.Provider != "capz" {
			t.Fatalf("willow lost facts: %+v", willow)
		}
		only := byName["portal-only-3"]
		if !slices.Equal(only.Sources, []string{SourcePortal}) || only.AuthProvider != "gs" || only.BaseDomain != "portal-only.test" {
			t.Fatalf("portal-only-3: %+v", only)
		}
		if !slices.IsSortedFunc(got, func(a, b Installation) int { return strings.Compare(a.Name, b.Name) }) {
			t.Fatalf("entries not sorted by name: %+v", got)
		}
	}
}

// Without the hub there is no registry to load, and the error names the flag.
func TestLoadNeedsTheHub(t *testing.T) {
	_, err := Load(context.Background(), nil, Sources{Catalog: Location{Repository: "example/registry", Path: "catalog/installations.yaml"}})
	if err == nil || !strings.Contains(err.Error(), "HUB_INSTALLATION") {
		t.Fatalf("no hub: %v", err)
	}
	_, err = Load(context.Background(), nil, Sources{Catalog: Location{Repository: "registry", Path: "x"}, Hub: "hazel"})
	if err == nil || !strings.Contains(err.Error(), "owner/repo") {
		t.Fatalf("bad repository: %v", err)
	}
}

func TestRepoFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/example/acme-configs":  acmeConfigs,
		"https://github.com/example/acme-configs/": acmeConfigs,
		"https://grafana.alder.acme.test/":         "",
		"https://github.com/example":               "",
	} {
		if got := repoFromURL(in); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

func TestStateOf(t *testing.T) {
	// One fact, one word: the fileset on record, or not.
	if stateOf(true) != StateEnabled || stateOf(false) != StateNotEnabled {
		t.Errorf("stateOf: %s / %s", stateOf(true), stateOf(false))
	}
	if !StateEnabled.OnRecord() || StateEnabled.FromAction() {
		t.Error("enabled: on record, not from an action")
	}
	if StateNotEnabled.OnRecord() || StateDrifted.OnRecord() {
		t.Error("a state without the fileset on record reads on record")
	}
}
