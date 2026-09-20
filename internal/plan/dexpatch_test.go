package plan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two definitions' rendered dex patches, from their golden filesets: the
// customer-portal definition's carries the portal's client alone, the
// agent-platform definition's the platform's built-in clients and its extra
// static clients with the portal's among them.
var renderedDexPatches = map[string]string{
	"customer-portal": filepath.Join("..", "..", "render", "customerportal", "testdata", "customer-portal", "golden",
		"giantswarm", "acme-configs", "installations", "maple", "apps", "dex-app", "configmap-values.yaml.patch"),
	"agent-platform": filepath.Join("..", "..", "render", "agentplatform", "testdata", "public-customer", "golden",
		"giantswarm", "oakridge-configs", "installations", "kestrel", "apps", "dex-app", "configmap-values.yaml.patch"),
}

// currentDexPatch is an installation's patch as its owners left it: the
// installation's ingress tuning and login connectors, a built-in client and
// an extra static client no definition declares, and the two definitions'
// own clients with stale inline secrets.
const currentDexPatch = `ingress:
  largeHeaderBuffers: true
  annotations:
    nginx.ingress.kubernetes.io/server-snippet: |
      large_client_header_buffers 4 32k;
oidc:
  customer:
    enabled: true
    connectorType: microsoft
    # the organisation's directory
    connectorName: Example Directory
    connectors:
      - id: customer-simple-oidc
        connectorType: oidc
        connectorConfig: |
          issuer: https://dex.other.example.test
          insecureEnableGroups: true
  staticClients:
    muster:
      clientID: stale-muster-id
      clientSecret: stale-muster-secret
    argocd:
      clientSecretRef:
        name: dex-client-argocd
        key: secret
  extraStaticClients:
    - id: backstage
      name: Dev Portal
      secret: stale-inline-secret
      redirectURIs:
        - https://old.portal.example.test/api/auth/oidc-maple/handler/frame
    - id: grafana
      name: Grafana
      secretRef:
        name: dex-client-grafana
        key: secret
      redirectURIs:
        - https://grafana.example.test/login/generic_oauth
`

func TestKeepDexPatchKeepsOtherOwnersPartsUnderEitherDefinition(t *testing.T) {
	for definition, path := range renderedDexPatches {
		t.Run(definition, func(t *testing.T) {
			rendered, err := os.ReadFile(path) // #nosec G304 -- a golden fixture of this repository
			if err != nil {
				t.Fatal(err)
			}
			got, kept, err := keepDexPatch(rendered, []byte(currentDexPatch))
			if err != nil {
				t.Fatal(err)
			}
			want := []Kept{{"", "ingress"}, {keyOIDC, "customer"}, {listStaticClients, "argocd"}, {listExtraClients, "grafana"}}
			if definition == "customer-portal" {
				// The portal's definition renders no built-in client at all:
				// the whole staticClients mapping is another owner's and
				// stays as one key, the platform's stale client included.
				want = []Kept{{"", "ingress"}, {keyOIDC, "customer"}, {keyOIDC, keyStaticClients}, {listExtraClients, "grafana"}}
			}
			if len(kept) != len(want) {
				t.Fatalf("kept %v, want %v", kept, want)
			}
			for i := range want {
				if kept[i] != want[i] {
					t.Errorf("kept[%d] = %v, want %v", i, kept[i], want[i])
				}
			}
			s := string(got)
			for _, frag := range []string{
				renderedHeader,
				"large_client_header_buffers 4 32k;",
				"# the organisation's directory",
				"connectorName: Example Directory",
				"issuer: https://dex.other.example.test",
				"name: dex-client-argocd",
				"- id: grafana",
				"https://grafana.example.test/login/generic_oauth",
				"name: dex-client-backstage",
			} {
				if !strings.Contains(s, frag) {
					t.Errorf("merged patch lacks %q:\n%s", frag, s)
				}
			}
			if strings.Contains(s, "stale-inline-secret") || strings.Contains(s, "old.portal.example.test") {
				t.Errorf("the definition's own client carries the current entry's stale values:\n%s", s)
			}
			if definition == "agent-platform" && strings.Contains(s, "stale-muster") {
				t.Errorf("the platform's built-in client carries the current entry's stale values:\n%s", s)
			}
			if strings.Count(s, "id: backstage") != 1 {
				t.Errorf("the portal's client appears %d times:\n%s", strings.Count(s, "id: backstage"), s)
			}
			again, keptAgain, err := keepDexPatch(rendered, got)
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != s || len(keptAgain) != len(kept) {
				t.Errorf("a second keep over the merged patch is not stable:\n%s", again)
			}
		})
	}
}

func TestKeepDexPatchLeavesTheRenderAsItIsWithNothingToKeep(t *testing.T) {
	rendered, err := os.ReadFile(renderedDexPatches["customer-portal"]) // #nosec G304 -- a golden fixture of this repository
	if err != nil {
		t.Fatal(err)
	}
	current := "oidc:\n  extraStaticClients:\n    - id: backstage\n      secret: stale-inline-secret\n"
	got, kept, err := keepDexPatch(rendered, []byte(current))
	if err != nil {
		t.Fatal(err)
	}
	if kept != nil || string(got) != string(rendered) {
		t.Errorf("kept %v; content changed:\n%s", kept, got)
	}
}

func TestKeepDexPatchRefusesACurrentFileThatIsNoMapping(t *testing.T) {
	if _, _, err := keepDexPatch([]byte("oidc: {}\n"), []byte("- a\n")); !errors.Is(err, errNoMapping) {
		t.Errorf("err = %v, want errNoMapping", err)
	}
}
