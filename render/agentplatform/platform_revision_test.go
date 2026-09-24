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
// hands that Secret's key to the HelmRelease of every consumer of muster's
// credentials that runs (musterConsumers), at every value its chart reads it
// from, and to no other — the shape a rotation of muster's credentials rolls
// every workload that reads them on; a toggled consumer keeps its toggle
// beside its refs. A shape on the 3 line (no child valuesFrom in that meta
// chart) renders none of it: no half state either way. Every consumer that
// runs has its Deployment probed Available, on either line.
func TestMusterCredentialsRevision(t *testing.T) {
	with, without, gateway := 0, 0, 0
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		running := in.runningMusterConsumers()
		probed := map[string]bool{}
		for _, p := range result.Probes {
			if p.ID == platformWorkloadsDimension && p.Kind == render.Condition && p.Namespace == platformNamespace && p.Resource == "Deployment" &&
				p.Expect.Condition == "Available" && p.Expect.ConditionStatus == conditionTrue {
				probed[p.Name] = true
			}
		}
		for _, c := range running {
			if !probed[c.deployment] {
				t.Errorf("%s: %s runs, but no %s probe reads Deployment %s/%s Available", shape, c.component, platformWorkloadsDimension, platformNamespace, c.deployment)
			}
			delete(probed, c.deployment)
		}
		if len(probed) > 0 {
			t.Errorf("%s: %s probes Deployments no running consumer has: %v", shape, platformWorkloadsDimension, probed)
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
		// A component named twice in the patch fails the decoding.
		var doc struct {
			Components map[string]struct {
				Enabled        *bool               `yaml:"enabled"`
				ValuesFromRefs []map[string]string `yaml:"valuesFromRefs"`
			} `yaml:"components"`
		}
		if err := yaml.Unmarshal(patch, &doc); err != nil {
			t.Fatalf("%s: the platform patch: %v", shape, err)
		}
		want := map[string][]string{}
		for _, c := range running {
			want[c.component] = c.revision
		}
		for component, body := range doc.Components {
			paths, consumer := want[component]
			if !consumer {
				if body.ValuesFromRefs != nil {
					t.Errorf("%s: components.%s carries valuesFromRefs, but no running consumer of muster's credentials is its", shape, component)
				}
				continue
			}
			delete(want, component)
			var got []string
			for _, ref := range body.ValuesFromRefs {
				if ref["kind"] != "Secret" || ref["name"] != musterRevisionSecret || ref["valuesKey"] != revisionKey {
					t.Errorf("%s: components.%s.valuesFromRefs entry %v", shape, component, ref)
				}
				got = append(got, ref["targetPath"])
			}
			if !slices.Equal(got, paths) {
				t.Errorf("%s: components.%s reads the revision into %v, want %v", shape, component, got, paths)
			}
			toggled := component == componentKlausGateway || component == componentAgentManager || component == componentClusterManager
			if toggled && (body.Enabled == nil || !*body.Enabled) {
				t.Errorf("%s: components.%s carries the revision but lost its toggle (enabled: %v)", shape, component, body.Enabled)
			}
			if component == componentKlausGateway {
				gateway++
			}
		}
		for component := range want {
			t.Errorf("%s: %s runs and reads muster's credentials, but the patch hands its HelmRelease no revision (components.%s.valuesFromRefs)", shape, component, component)
		}
	}
	if with == 0 || without == 0 || gateway == 0 {
		t.Errorf("the shapes cover %d with the revision (the 4 line), %d without (the 3 line) and %d with the chat gateway's; want each", with, without, gateway)
	}
}
