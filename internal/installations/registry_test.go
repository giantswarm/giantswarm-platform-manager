package installations

import (
	"context"
	"strings"
	"testing"
)

// The invented customer's repositories.
const (
	acmeConfigs = "example/acme-configs"
	acmeMCs     = "example/acme-management-clusters"
)

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
