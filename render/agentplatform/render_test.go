package agentplatform

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

var update = flag.Bool("update", false, "rewrite the golden filesets from the current render")

// shapes are the installation shapes with a golden fileset under testdata/<shape>/.
var shapes = []string{"public-customer", "giantswarm-owned"}

func loadInput(t *testing.T, shape string) (map[string]any, map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", shape, "input.yaml"))
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

// flatten writes a Result as the golden tree: one file per rendered file under
// <owner>/<repo>/<path>, and includes.txt with the shared kustomization entries.
func flatten(r *render.Result) map[string][]byte {
	out := map[string][]byte{}
	for repo, files := range r.Files {
		for path, f := range files {
			out[filepath.Join(string(repo), path)] = f.Content
		}
	}
	var includes []string
	for _, inc := range r.Includes {
		includes = append(includes, string(inc.Repository)+":"+inc.Path+" "+inc.Resource)
	}
	sort.Strings(includes)
	out["includes.txt"] = []byte(strings.Join(includes, "\n") + "\n")
	return out
}

func TestGolden(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			result, err := Render(input, secrets)
			if err != nil {
				t.Fatal(err)
			}
			got := flatten(result)
			dir := filepath.Join("testdata", shape, "golden")
			if *update {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				for name, content := range got {
					if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := map[string][]byte{}
			err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(dir, path)
				want[rel] = content
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
	input, secrets := loadInput(t, "public-customer")
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

func TestRefusals(t *testing.T) {
	base, secrets := loadInput(t, "public-customer")
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
		{"empty model key", clone(func(m map[string]any) { m["kagent"].(map[string]any)["modelKeySecret"] = "managed" }), nil, ErrEmptySecret, "kagent.modelKey"},
		{"unknown secret value", base, map[string]string{"kagent.modelKey": "x"}, ErrUnknownSecret, "kagent.modelKey"},
		{"klaus-gateway on a customer", clone(func(m map[string]any) { m["klausGateway"] = map[string]any{"enabled": true} }), secrets, ErrPolicy, "klaus-gateway"},
		{"login pin on a customer", clone(func(m map[string]any) { m["identity"] = map[string]any{"loginConnectorId": "x"} }), secrets, ErrPolicy, "identity.loginConnectorId"},
		{"hub outputs", clone(func(m map[string]any) {
			m["federation"].(map[string]any)["targets"] = []any{map[string]any{"installation": "x", "private": false, "groups": []any{"kubernetes"}}}
		}), secrets, ErrNotRendered, "federation.targets"},
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
