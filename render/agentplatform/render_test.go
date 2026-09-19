package agentplatform

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

var update = flag.Bool("update", false, "rewrite the golden filesets from the current render")

// The installation shapes with a golden fileset under testdata/<shape>/.
const (
	shapePublicCustomer         = "public-customer"
	shapeGiantswarmOwned        = "giantswarm-owned"
	shapeHubPrivateTarget       = "hub-private-target"
	shapeMultiClusterAggregator = "multi-cluster-aggregator"
)

// shapes are the installation shapes, in the order the goldens are rendered.
var shapes = []string{shapePublicCustomer, shapeGiantswarmOwned, shapeHubPrivateTarget, shapeMultiClusterAggregator}

func loadInput(t *testing.T, shape string) (map[string]any, map[string]string) {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(filepath.Join("testdata", shape)), "input.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input   map[string]any    `yaml:"input"`
		Secrets map[string]string `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Input, doc.Secrets
}

func TestGolden(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			result, err := Render(input, secrets)
			if err != nil {
				t.Fatal(err)
			}
			got := result.Tree()
			dir := filepath.Join("testdata", shape, "golden")
			if *update {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				for name, content := range got {
					if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := map[string][]byte{}
			golden := os.DirFS(dir)
			err = fs.WalkDir(golden, ".", func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				content, err := fs.ReadFile(golden, path)
				if err != nil {
					return err
				}
				want[filepath.FromSlash(path)] = content
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for name, content := range want {
				if !bytes.Equal(got[name], content) {
					t.Errorf("%s differs from the golden (run with -update to accept):\n--- want\n%s\n--- got\n%s", name, content, got[name])
				}
			}
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("%s rendered but not in the golden (run with -update to accept)", name)
				}
			}
		})
	}
}

func TestSecretFilesCarryNoValues(t *testing.T) {
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets)
		if err != nil {
			t.Fatal(err)
		}
		for repo, files := range result.Files {
			for path, f := range files {
				if !strings.Contains(path, "secret") && !strings.Contains(path, "credential") {
					if len(f.Generated) > 0 {
						t.Errorf("%s: %s carries generated placeholders but is not named as a secret file", repo, path)
					}
					continue
				}
				if !bytes.Contains(f.Content, []byte("kind: Secret")) {
					continue
				}
				for _, g := range f.Generated {
					if !bytes.Contains(f.Content, []byte(g.Placeholder)) {
						t.Errorf("%s: %s lists %s but does not carry its placeholder", repo, path, g.Name)
					}
				}
			}
		}
	}
}

func TestOwnedPathsOnly(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) { testOwnedPathsOnly(t, shape) })
	}
}

func testOwnedPathsOnly(t *testing.T, shape string) {
	input, secrets := loadInput(t, shape)
	result, err := Render(input, secrets)
	if err != nil {
		t.Fatal(err)
	}
	name := input["installation"].(map[string]any)["name"].(string)
	for repo, files := range result.Files {
		for path := range files {
			if !strings.Contains(path, "/"+name+"/") {
				t.Errorf("%s: %s is outside the installation's own directories", repo, path)
			}
			for _, inc := range result.Includes {
				if inc.Repository == repo && inc.Path == path {
					t.Errorf("%s: %s is rendered and also an include of a shared file", repo, path)
				}
			}
		}
	}
	// Enable then disable: the disabled fileset is empty by construction, so the
	// delta is exactly the rendered paths, and every one of them may be deleted.
	if result.Len() == 0 {
		t.Fatal("an enabled installation renders no files")
	}
}

// TestProbesAreLiveDimensions holds the probes to features.yaml: every probe
// observes a kind: live dimension of the feature it names, every action names
// a feature, and every live dimension has at least one probe on the
// public-customer shape.
func TestProbesAreLiveDimensions(t *testing.T) {
	raw, err := definitions.FS.ReadFile("agent-platform/features.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Features map[string]struct {
			Dimensions []struct {
				ID   string `yaml:"id"`
				Kind string `yaml:"kind"`
			} `yaml:"dimensions"`
		} `yaml:"features"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	live := map[string]string{} // dimension id -> feature
	for feature, f := range doc.Features {
		for _, d := range f.Dimensions {
			if d.Kind == "live" {
				live[d.ID] = feature
			}
		}
	}
	if len(live) == 0 {
		t.Fatal("features.yaml has no live dimension")
	}
	probed := map[string]bool{}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Probes) == 0 {
			t.Errorf("%s: no probes", shape)
		}
		for _, p := range result.Probes {
			feature, ok := live[p.ID]
			if !ok {
				t.Errorf("%s: probe %s is not a live dimension of features.yaml", shape, p.ID)
				continue
			}
			if feature != p.Feature {
				t.Errorf("%s: probe %s names feature %s, features.yaml has it under %s", shape, p.ID, p.Feature, feature)
			}
			if shape == shapePublicCustomer {
				probed[p.ID] = true
			}
		}
		for _, a := range result.Actions {
			if _, ok := doc.Features[a.Feature]; !ok {
				t.Errorf("%s: action %s names feature %s, which features.yaml does not have", shape, a.ID, a.Feature)
			}
		}
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !probed[id] {
			t.Errorf("live dimension %s of feature %s has no probe on the public-customer shape", id, live[id])
		}
	}
}

func TestRefusals(t *testing.T) {
	base, secrets := loadInput(t, shapePublicCustomer)
	target := func(private bool) map[string]any {
		return map[string]any{"installation": "x", "baseDomain": "x.example", "private": private, "groups": []any{"kubernetes"}}
	}
	clone := func(mutate func(map[string]any)) map[string]any {
		var c map[string]any
		b, _ := yaml.Marshal(base)
		_ = yaml.Unmarshal(b, &c)
		mutate(c)
		return c
	}
	cases := []struct {
		name    string
		input   map[string]any
		secrets map[string]string
		err     error
		names   string
	}{
		{"unknown top-level key", clone(func(m map[string]any) { m["colourScheme"] = "dark" }), secrets, ErrInput, "colourScheme"},
		{"unknown nested key", clone(func(m map[string]any) { m["kagent"].(map[string]any)["replicas"] = 3 }), secrets, ErrInput, "replicas"},
		{"empty model key", clone(func(m map[string]any) { m["kagent"].(map[string]any)["modelKeySecret"] = "managed" }), nil, ErrEmptySecret, fieldModelKey},
		{"unknown secret value", base, map[string]string{fieldModelKey: "x"}, ErrUnknownSecret, fieldModelKey},
		{"klaus-gateway on a customer", clone(func(m map[string]any) { m["klausGateway"] = map[string]any{"enabled": true} }), secrets, ErrPolicy, "klaus-gateway"},
		{"login pin on a customer", clone(func(m map[string]any) { m["identity"] = map[string]any{"loginConnectorId": "x"} }), secrets, ErrPolicy, "identity.loginConnectorId"},
		{"targets without a connector", clone(func(m map[string]any) {
			m["federation"].(map[string]any)["targets"] = []any{target(false)}
		}), secrets, ErrInput, "federation.connectorId"},
		{"private target without the tunnel", clone(func(m map[string]any) {
			f := m["federation"].(map[string]any)
			f["connectorId"], f["brokerClientId"], f["targets"] = "c", "b", []any{target(true)}
		}), secrets, ErrInput, "federation.tunnel"},
		{"tunnel without a private target", clone(func(m map[string]any) {
			f := m["federation"].(map[string]any)
			f["connectorId"], f["brokerClientId"], f["targets"] = "c", "b", []any{target(false)}
			f["tunnel"] = map[string]any{"jwks": "{}", "trustBundleProvisioned": true, "teleport": map[string]any{"clusterName": "t", "proxyAddr": "t:443"}}
		}), secrets, ErrInput, "federation.tunnel"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Render(c.input, c.secrets)
			if !errors.Is(err, c.err) {
				t.Fatalf("got %v, want %v", err, c.err)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Fatalf("%q does not name %q", err, c.names)
			}
		})
	}
}
