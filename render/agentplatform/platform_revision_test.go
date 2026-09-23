package agentplatform

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// muster's credentials revision is one generated name held by exactly its
// OAuth credentials Secret, its Valkey Secret and the revision Secret in the
// Flux namespace, listed in the secrets kustomization, and the platform patch
// hands that Secret's key to the muster and valkey HelmReleases at every
// checksum value their charts read — the shape a rotation of muster's
// credentials rolls muster and its Valkey on. A shape on the 3 line (no child
// valuesFrom in that meta chart) renders none of it: no half state either way.
func TestMusterCredentialsRevision(t *testing.T) {
	with, without := 0, 0
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		installation, _ := input["installation"].(map[string]any)["name"].(string)
		name := installation + "-muster-credentials-revision"
		var held []string
		var patch, secretsKustomization []byte
		for _, files := range result.Files {
			for path, f := range files {
				if strings.HasSuffix(path, "/apps/agent-platform/configmap-values.yaml.patch") {
					patch = f.Content
				}
				if strings.HasSuffix(path, "/extras/agent-platform/secrets/kustomization.yaml") {
					secretsKustomization = f.Content
				}
				for _, g := range f.Generated {
					if g.Name == name {
						if g.Kind != render.Alphanumeric || g.Length != revisionLength {
							t.Errorf("%s: %s: %s is %s/%d, want %s/%d", shape, path, name, g.Kind, g.Length, render.Alphanumeric, revisionLength)
						}
						held = append(held, path[strings.LastIndex(path, "/")+1:])
					}
				}
			}
		}
		slices.Sort(held)
		listed := strings.Contains(string(secretsKustomization), "- "+musterRevisionSecret+".yaml\n")
		refs := strings.Contains(string(patch), "valuesFromRefs")
		if len(held) == 0 {
			if refs || listed {
				t.Errorf("%s: no revision held, yet the patch carries valuesFromRefs (%v) or the kustomization lists the Secret (%v)", shape, refs, listed)
			}
			without++
			continue
		}
		with++
		if want := []string{musterRevisionSecret + ".yaml", musterOAuthSecret + ".yaml", musterValkeySecret + ".yaml"}; !slices.Equal(held, want) {
			t.Errorf("%s: %s is held by %v, want %v", shape, name, held, want)
		}
		if !listed {
			t.Errorf("%s: the secrets kustomization does not list %s.yaml", shape, musterRevisionSecret)
		}
		var doc struct {
			Components map[string]struct {
				ValuesFromRefs []map[string]string `yaml:"valuesFromRefs"`
			} `yaml:"components"`
		}
		if err := yaml.Unmarshal(patch, &doc); err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{"muster": musterChecksumValues, "valkey": {valkeyChecksumValue}}
		for component, paths := range want {
			var got []string
			for _, ref := range doc.Components[component].ValuesFromRefs {
				if ref["kind"] != "Secret" || ref["name"] != musterRevisionSecret || ref["valuesKey"] != revisionKey {
					t.Errorf("%s: components.%s.valuesFromRefs entry %v", shape, component, ref)
				}
				got = append(got, ref["targetPath"])
			}
			if !slices.Equal(got, paths) {
				t.Errorf("%s: components.%s reads the revision into %v, want %v", shape, component, got, paths)
			}
		}
	}
	if with == 0 || without == 0 {
		t.Errorf("the shapes cover %d with the revision (the 4 line) and %d without (the 3 line); want both", with, without)
	}
}
