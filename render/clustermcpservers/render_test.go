package clustermcpservers

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/mcpservers"
)

var update = flag.Bool("update", false, "rewrite the goldens from the render")

// shapes are the testdata directories, each an input.yaml and its golden tree.
var shapes = []string{"fleet", "private-dex-ca", "single-server"}

func loadInput(t *testing.T, shape string) map[string]any {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(filepath.Join("testdata", shape)), "input.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input map[string]any `yaml:"input"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Input
}

func TestGolden(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			result, err := Render(loadInput(t, shape), nil, render.ModeCommit)
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
			if err := fs.WalkDir(golden, ".", func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				content, err := fs.ReadFile(golden, path)
				want[filepath.FromSlash(path)] = content
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for name, content := range want {
				if !bytes.Equal(got[name], content) {
					t.Errorf("%s differs from the golden (run with -update to accept):\n--- want\n%s\n--- got\n%s", name, content, got[name])
				}
			}
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("%s is rendered but not in the golden (run with -update to accept)", name)
				}
			}
		})
	}
}

// Every running server's credentials revision is one generated name held by
// exactly its credentials Secret, its Valkey Secret and the revision Secret,
// and its kustomization hands that Secret's key to the server's HelmRelease
// at every checksum value its chart reads and to its Valkey's at the Valkey
// chart's; a server that does not run renders no directory and no Dex client.
func TestCredentialsRevision(t *testing.T) {
	for _, shape := range shapes {
		input := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Render(input, nil, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		tree := result.Tree()
		dex := string(tree[filepath.Join("giantswarm", in.Installation.Customer+"-configs", "installations", in.Installation.Name, "apps", "dex-app", "configmap-values.yaml.patch")])
		for _, s := range mcpservers.Servers {
			dir := filepath.Join("giantswarm", in.Installation.Customer+"-management-clusters", "management-clusters", in.Installation.Name, "extras", s.Name)
			if !in.Servers.of(s).runs() {
				for name := range tree {
					if strings.HasPrefix(name, dir+string(filepath.Separator)) {
						t.Errorf("%s: %s does not run and renders %s", shape, s.Name, name)
					}
				}
				if strings.Contains(dex, s.DexClient+":") {
					t.Errorf("%s: %s does not run and its Dex client renders", shape, s.Name)
				}
				continue
			}
			if !strings.Contains(dex, s.DexClient+":\n") || !strings.Contains(dex, "name: "+render.DexClientSecretName(s.Name)+"\n") {
				t.Errorf("%s: %s's Dex client is no referenced Secret:\n%s", shape, s.Name, dex)
			}
			name := in.Installation.Name + "-" + s.Name + "-credentials-revision"
			var held []string
			for path, file := range result.Files[render.Repository("giantswarm/"+in.Installation.Customer+"-management-clusters")] {
				for _, g := range file.Generated {
					if g.Name == name {
						held = append(held, filepath.Base(path))
					}
				}
			}
			slices.Sort(held)
			if want := []string{mcpservers.RevisionFile, "oauth-credentials.enc.yaml", "valkey-credentials.enc.yaml"}; !slices.Equal(held, want) {
				t.Errorf("%s: %s: %s is held by %v, want %v", shape, s.Name, name, held, want)
			}
			got := revisionSources(t, tree[filepath.Join(dir, "kustomization.yaml")], s.RevisionSecretName())
			want := map[string][]string{s.Name: s.ChecksumValues(), s.Name + "-valkey": {mcpservers.ValkeyChecksumValue}}
			for hr, paths := range want {
				if !slices.Equal(got[hr], paths) {
					t.Errorf("%s: %s: HelmRelease %s reads the revision into %v, want %v", shape, s.Name, hr, got[hr], paths)
				}
			}
		}
	}
}

// revisionSources are, per HelmRelease the kustomization patches, the
// targetPaths its valuesFrom entries read the revision Secret's key into.
func revisionSources(t *testing.T, kustomization []byte, secret string) map[string][]string {
	t.Helper()
	var k struct {
		Patches []struct {
			Patch  string `yaml:"patch"`
			Target struct{ Kind, Name string }
		} `yaml:"patches"`
	}
	if err := yaml.Unmarshal(kustomization, &k); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, p := range k.Patches {
		var ops []struct {
			Op, Path string
			Value    any
		}
		if err := yaml.Unmarshal([]byte(p.Patch), &ops); err != nil {
			t.Fatal(err)
		}
		for _, o := range ops {
			entries := []any{o.Value}
			if list, ok := o.Value.([]any); ok {
				entries = list
			}
			for _, entry := range entries {
				m, _ := entry.(map[string]any)
				if m["kind"] == "Secret" && m["name"] == secret && m["valuesKey"] == mcpservers.RevisionKey {
					path, _ := m["targetPath"].(string)
					out[p.Target.Name] = append(out[p.Target.Name], path)
				}
			}
		}
	}
	return out
}

func TestRefusals(t *testing.T) {
	with := func(edit func(in map[string]any)) map[string]any {
		in := loadInput(t, "fleet")
		edit(in)
		return in
	}
	installation := func(in map[string]any) map[string]any { return in["installation"].(map[string]any) }
	cases := []struct {
		name    string
		input   map[string]any
		secrets map[string]string
		kind    error
		says    string
	}{
		{"the agent platform renders the servers", with(func(in map[string]any) { installation(in)["agentPlatform"] = true }), nil, ErrPolicy, "reconcile agent-platform instead"},
		{"a dex-app that keeps a rotated secret", with(func(in map[string]any) { installation(in)["dexAppVersion"] = "3.2.2" }), nil, ErrPolicy, "need dex-app 3.2.3 or later"},
		{"a dex-app that is no version", with(func(in map[string]any) { installation(in)["dexAppVersion"] = "latest" }), nil, ErrInput, "is no version"},
		{"a supplied value", with(func(map[string]any) {}), map[string]string{"servers.mcpKubernetes.token": "x"}, ErrUnknownSecret, "generates every credential"},
		{"no server runs", with(func(in map[string]any) {
			for _, s := range in["servers"].(map[string]any) {
				s.(map[string]any)["enabled"] = false
			}
		}), nil, ErrInput, "nothing to render"},
		{"an unknown key", with(func(in map[string]any) { in["colourScheme"] = "dark" }), nil, ErrInput, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Render(c.input, c.secrets, render.ModeCommit)
			if !errors.Is(err, c.kind) || !strings.Contains(render.Reason(err), c.says) {
				t.Fatalf("got %v, want %v saying %q", err, c.kind, c.says)
			}
		})
	}
}

// A fresh enable, whose record says nothing about the servers, runs all three;
// an absent dex-app version refuses nothing.
func TestFreshEnableRunsEveryServer(t *testing.T) {
	result, err := Render(map[string]any{"installation": map[string]any{"name": "wren", "baseDomain": "wren.example.test", "customer": "acme"}}, nil, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range mcpservers.Servers {
		path := "management-clusters/wren/extras/" + s.Name + "/kustomization.yaml"
		if _, ok := result.Files["giantswarm/acme-management-clusters"][path]; !ok {
			t.Errorf("%s not rendered", path)
		}
	}
}
