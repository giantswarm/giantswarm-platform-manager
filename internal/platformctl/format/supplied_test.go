package format

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The dry run of a wave whose targets ask for values at commit, as
// platformctl prints it, held to a golden: each installation names its
// supplied fields, the files carry the fields' markers, a pairing's client
// secret is supplied once for both sides, and the set says how one run
// supplies them all. No value exists at a dry run; the text names fields.
func TestWaveSuppliedDryRunGolden(t *testing.T) {
	const (
		modelKey = "kagent.modelKey"
		pairing  = "muster-token-exchange-oak-hazel-client-secret"
	)
	target := func(name, peer string) tools.DryRun {
		file := "management-clusters/" + name + "/extras/agent-platform/secrets/kagent-model-key.enc.yaml"
		exchange := "management-clusters/" + name + "/extras/agent-platform/secrets/muster-token-exchange.enc.yaml"
		return tools.DryRun{Installation: plan.Installation{
			Name: name, State: "enabled",
			Files: []plan.File{
				{Repository: acmeMCs, Path: file, Change: plan.ChangeCreate, Content: "stringData:\n  apiKey: " + render.Supplied(modelKey) + "\n"},
				{Repository: acmeMCs, Path: exchange, Change: plan.ChangeCreate, Content: "stringData:\n  clientSecret: " + render.Placeholder(pairing) + "\n"},
			},
			GeneratedSecrets: []plan.GeneratedSecret{{Name: pairing, Kind: "base64", Length: 32, Files: []string{exchange}, Peer: acmeMCs + ":management-clusters/" + peer + "/extras/agent-platform/secrets/muster-token-exchange.enc.yaml", Supplied: true}},
			SuppliedSecrets:  []string{modelKey, pairing},
			Diff:             map[plan.Change]int{plan.ChangeCreate: 2},
		}}
	}
	r := tools.CapabilityResult{
		Caller: "jane", Hub: "gopher", Tool: tools.ToolReconcileCapability, Capability: "agent-platform", DryRun: true,
		Order:         []string{oak, hazel},
		Installations: []tools.DryRun{target(oak, hazel), target(hazel, oak)},
		PullRequests:  []plan.PullRequest{{Order: 1, Repository: acmeMCs, Installations: []string{oak, hazel}, Changes: 4}},
	}
	var buf bytes.Buffer
	if err := Plan(&buf, r, true); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"You supply at commit: " + modelKey + ", " + pairing, "--secret <installation>/<field>=<source>", "beekeeper:<ref>"} {
		if !strings.Contains(got, want) {
			t.Errorf("the dry run lacks %q", want)
		}
	}
	const golden = "testdata/wave-supplied-dry-run.golden"
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if got != string(want) {
		t.Fatalf("the dry run differs from %s (run with -update to accept):\n%s", golden, got)
	}
}
