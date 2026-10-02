package tools

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A fact the render needs that the record leaves empty is one of the plan's
// refusals: commitRefusal alone asks the plan for it, and the wave's
// pre-check refuses with the same sentence as the single commit and the
// comparison, naming the installation.
func TestFactsRefusalIsACommitRefusal(t *testing.T) {
	for fn, called := range callsByFunction(t) {
		if fn != "commitRefusal" && strings.Contains(strings.Join(called, " "), "FactsRefusal") {
			t.Errorf("%s asks the plan for FactsRefusal itself: take it from commitRefusal", fn)
		}
	}
	p := plan.Installation{Name: rowan, Diff: map[plan.Change]int{plan.ChangeCreate: 1}, Files: []plan.File{{Change: plan.ChangeCreate}},
		MissingFacts: &plan.MissingFacts{File: "acme/configs:" + render.RecordPath(rowan), Missing: []render.Fact{{Key: "provider.azure.subscriptionId", Source: "the AzureCluster's subscription", Renders: "the snapshot store's identity"}}}}
	want := commitRefusal(p, &installations.Record{})
	if want == "" || want != p.FactsRefusal() {
		t.Fatalf("commitRefusal %q, want the plan's FactsRefusal %q", want, p.FactsRefusal())
	}
	if err := waveRefusal(ToolReconcileCapability, p, &installations.Record{}); err == nil || !strings.Contains(err.Error(), rowan+": "+want) {
		t.Errorf("the wave's pre-check: %v, want %q", err, want)
	}
	p.MissingFacts = nil
	if refusal := commitRefusal(p, &installations.Record{}); refusal != "" {
		t.Errorf("every fact set: %q", refusal)
	}
}
