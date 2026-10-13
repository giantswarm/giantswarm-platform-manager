package format

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// Invented files of a planned change: a values patch whose keys differ, a
// Secret whose encrypted key differs, a kustomization that stays.
const (
	valuesPatch   = "installations/rowan/apps/agent-platform/configmap-values.yaml.patch"
	secretFile    = "installations/rowan/apps/agent-platform/secret-values.yaml.patch"
	kustomization = "management-clusters/rowan/extras/agent-platform/kustomization.yaml"
	// secretValue stands for a value no output may print.
	secretValue = "hunter2-never-printed"
	plainValue  = "kagent.acme.example-never-printed"
)

// plannedChange is a reconcile's dry run of rowan: the values patch differs
// in two keys (one changed, one added) and drops a third, the Secret in one
// encrypted key, the kustomization is unchanged.
func plannedChange() tools.CapabilityResult {
	return tools.CapabilityResult{
		Caller: someone, Tool: tools.ToolReconcileCapability, Capability: agentPlatform, Hub: hazel, DryRun: true,
		Order: []string{rowan},
		Installations: []tools.DryRun{{
			Summary: map[verify.Mark]int{verify.DiffersByInput: 1, verify.Drifted: 1, verify.AsDefined: 1},
			Installation: plan.Installation{
				Name: rowan, State: installations.StateEnabled,
				Diff: map[plan.Change]int{plan.ChangeUpdate: 2, plan.ChangeUnchanged: 1},
				Files: []plan.File{
					{Repository: acmeConfigs, Path: valuesPatch, Change: plan.ChangeUpdate},
					{Repository: acmeConfigs, Path: secretFile, Change: plan.ChangeUpdate},
					{Repository: acmeMCs, Path: kustomization, Change: plan.ChangeUnchanged},
				},
			},
			Features: []verify.Feature{
				{ID: "gateway", Title: "Gateway", Mark: verify.DiffersByInput, Marks: map[verify.Mark]int{verify.DiffersByInput: 1}, Dimensions: []verify.Dimension{
					{ID: "gateway-host", Kind: configMap, Key: "gateway.host", Mark: verify.DiffersByInput, Files: []string{acmeConfigs + ":" + valuesPatch}, Differences: []verify.Difference{
						{File: acmeConfigs + ":" + valuesPatch, Path: "data.values.gateway.host", Input: "gateway.host", Rendered: plainValue, Current: "old." + plainValue, Line: 4, CurrentLine: 4},
						{File: acmeConfigs + ":" + valuesPatch, Path: "data.values.gateway.replicas", Rendered: "2", Line: 5},
					}},
				}},
				{ID: "kagent", Title: "kagent", Mark: verify.Drifted, Marks: map[verify.Mark]int{verify.Drifted: 1, verify.AsDefined: 1}, Dimensions: []verify.Dimension{
					{ID: "kagent-legacy", Kind: configMap, Key: "kagent.legacy", Mark: verify.Drifted, Differences: []verify.Difference{
						{File: acmeConfigs + ":" + valuesPatch, Path: "data.values.kagent.legacy", Current: "true", CurrentLine: 9},
						// The same key under a second dimension is listed once.
						{File: acmeConfigs + ":" + valuesPatch, Path: "data.values.gateway.host", Input: "gateway.host", Rendered: plainValue, Current: "old." + plainValue, Line: 4, CurrentLine: 4},
					}},
					{ID: "kagent-secret", Kind: "secret", Key: "kagent.secret", Mark: verify.Drifted, Differences: []verify.Difference{
						{File: acmeConfigs + ":" + secretFile, Path: "stringData.values.kagent.clientSecret", Rendered: verify.Redacted, Current: verify.Redacted, Line: 6, CurrentLine: 6},
					}},
					{ID: "kagent-kustomization", Kind: "kustomization", Key: "resources", Mark: verify.AsDefined, Files: []string{acmeMCs + ":" + kustomization}},
				}},
			},
		}},
	}
}

func TestPlanDifferences(t *testing.T) {
	values := "    Differs in " + acmeConfigs + ":" + valuesPatch + ":\n"
	secret := "    Differs in " + acmeConfigs + ":" + secretFile + ":\n"
	cases := []struct {
		name    string
		edit    func(*tools.DryRun)
		want    []string
		wantNot []string
	}{
		{
			name: "two differing keys and a removed one, each once with its marker",
			want: []string{values +
				"      changed  data.values.gateway.host\n" +
				"      added    data.values.gateway.replicas\n" +
				"      removed  data.values.kagent.legacy\n"},
		},
		{
			name: "a secret key prints only its name",
			want: []string{secret + "      changed  stringData.values.kagent.clientSecret  (secret)\n"},
		},
		{
			name:    "an unchanged file prints no block",
			wantNot: []string{"Differs in " + acmeMCs},
		},
		{
			name: "a difference on a file the plan leaves unchanged prints no block",
			edit: func(d *tools.DryRun) {
				d.Files[0].Change = plan.ChangeUnchanged
			},
			want:    []string{secret},
			wantNot: []string{values},
		},
		{
			name: "a changed file without a key-level difference prints no block",
			edit: func(d *tools.DryRun) {
				d.Features[1].Dimensions[1].Differences = nil
			},
			want:    []string{values},
			wantNot: []string{secret},
		},
		{
			name: "a live difference names an object, no file: none of the files'",
			edit: func(d *tools.DryRun) {
				d.Features = []verify.Feature{{ID: "live", Dimensions: []verify.Dimension{{ID: "live", Differences: []verify.Difference{
					{Object: "ConfigMap platform-manager/values", Path: "data.host", Rendered: plainValue, Current: "x"},
				}}}}}
			},
			wantNot: []string{"Differs in", "data.host"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := plannedChange()
			if tc.edit != nil {
				tc.edit(&r.Installations[0])
			}
			var b bytes.Buffer
			if err := Plan(&b, r, false); err != nil {
				t.Fatal(err)
			}
			out := b.String()
			contains(t, out, tc.want...)
			for _, w := range append(tc.wantNot, secretValue, plainValue, verify.Redacted) {
				if strings.Contains(out, w) {
					t.Errorf("output holds %q:\n%s", w, out)
				}
			}
		})
	}
}

// The dry run's JSON answer, recorded from plannedChange and held to a
// golden byte for byte: the text's differences add nothing to it. The text
// dry run is rendered from that recorded answer, as platformctl decodes it,
// and held to a golden of its own.
func TestPlanDifferencesGoldens(t *testing.T) {
	const answer = "testdata/reconcile-differences-dry-run.json"
	raw, err := json.Marshal(plannedChange())
	if err != nil {
		t.Fatal(err)
	}
	var js bytes.Buffer
	if err := JSON(&js, raw); err != nil {
		t.Fatal(err)
	}
	golden(t, answer, js.Bytes())

	recorded, err := os.ReadFile(answer)
	if err != nil {
		t.Fatal(err)
	}
	var r tools.CapabilityResult
	if err := json.Unmarshal(recorded, &r); err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	if err := Plan(&text, r, false); err != nil {
		t.Fatal(err)
	}
	golden(t, "testdata/reconcile-differences-dry-run.golden", text.Bytes())
}

// golden holds got to the file, rewritten with -update.
func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("the output differs from %s (run with -update to accept):\n%s", path, got)
	}
}
