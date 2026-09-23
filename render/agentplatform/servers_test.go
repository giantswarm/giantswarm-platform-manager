package agentplatform

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Every server's credentials revision is one generated name held by exactly
// its credentials Secret, its Valkey Secret and the revision Secret in the
// Flux namespace, and its kustomization hands that Secret's key to the
// server's HelmRelease at every checksum value its chart reads and to its
// Valkey's at the Valkey chart's — the shape a rotation rolls both pods on.
func TestServersCredentialsRevision(t *testing.T) {
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		installation, _ := input["installation"].(map[string]any)["name"].(string)
		for _, s := range servers {
			name := installation + "-" + s.name + "-credentials-revision"
			var held []string
			var dir string
			for _, files := range result.Files {
				for path, f := range files {
					if !strings.Contains(path, "/extras/"+s.name+"/") {
						continue
					}
					dir = path[:strings.LastIndex(path, "/")+1]
					for _, g := range f.Generated {
						if g.Name == name {
							if g.Kind != render.Alphanumeric || g.Length != revisionLength {
								t.Errorf("%s: %s: %s is %s/%d, want %s/%d", shape, path, name, g.Kind, g.Length, render.Alphanumeric, revisionLength)
							}
							held = append(held, path[len(dir):])
						}
					}
				}
			}
			slices.Sort(held)
			if want := []string{revisionFile, "oauth-credentials.enc.yaml", "valkey-credentials.enc.yaml"}; !slices.Equal(held, want) {
				t.Errorf("%s: %s: %s is held by %v, want %v", shape, s.name, name, held, want)
			}
			files := result.Files[render.Repository(repositoryOf(t, result, dir))]
			if !strings.Contains(string(files[dir+revisionFile].Content), "namespace: "+fluxNamespace+"\n") {
				t.Errorf("%s: %s: the revision Secret is not in %s:\n%s", shape, s.name, fluxNamespace, files[dir+revisionFile].Content)
			}
			want := map[string][]string{s.name: s.checksumValues(), s.name + "-valkey": {valkeyChecksumValue}}
			got := revisionSources(t, files[dir+"kustomization.yaml"].Content, s.revisionSecretName())
			for release, paths := range want {
				if !slices.Equal(got[release], paths) {
					t.Errorf("%s: HelmRelease %s reads the revision into %v, want %v", shape, release, got[release], paths)
				}
			}
		}
	}
}

// repositoryOf finds the repository whose files hold dir.
func repositoryOf(t *testing.T, r *render.Result, dir string) string {
	t.Helper()
	for repo, files := range r.Files {
		for path := range files {
			if strings.HasPrefix(path, dir) {
				return string(repo)
			}
		}
	}
	t.Fatalf("no repository holds %s", dir)
	return ""
}

// revisionSources reads the kustomization's patches and answers, per
// HelmRelease, the targetPaths its valuesFrom entries read the revision
// Secret's key into.
func revisionSources(t *testing.T, kustomization []byte, secret string) map[string][]string {
	t.Helper()
	var k struct {
		Patches []struct {
			Patch  string `yaml:"patch"`
			Target struct {
				Kind, Name string
			} `yaml:"target"`
		} `yaml:"patches"`
	}
	if err := yaml.Unmarshal(kustomization, &k); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, p := range k.Patches {
		if p.Target.Kind != "HelmRelease" {
			t.Fatalf("a patch on a %s", p.Target.Kind)
		}
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
				if m["kind"] != "Secret" || m["name"] != secret {
					continue
				}
				if o.Op != "add" || !strings.HasPrefix(o.Path, "/spec/valuesFrom") || m["valuesKey"] != revisionKey {
					t.Errorf("HelmRelease %s: %+v", p.Target.Name, o)
				}
				path, _ := m["targetPath"].(string)
				out[p.Target.Name] = append(out[p.Target.Name], path)
			}
		}
	}
	return out
}
