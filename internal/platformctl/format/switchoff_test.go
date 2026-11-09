package format

import (
	"bytes"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// The dry run prints the switch-off warning first, apart from the file rows
// and the drift lines, one line per component naming what depends on it.
func TestDryRunPrintsTheSwitchOffWarningFirst(t *testing.T) {
	p := plan.Installation{Name: "birch", State: installations.StateEnabled,
		SwitchOff: &plan.SwitchOff{Components: []plan.SwitchedOff{{Component: "agent-manager", DependedOnBy: "the Dev Portal's Create agent and agent editing"}, {Component: "extra"}}},
		Files:     []plan.File{{Repository: "giantswarm/acme-configs", Path: "installations/birch/apps/agent-platform/configmap-values.yaml.patch", Change: plan.ChangeUpdate}},
		Diff:      map[plan.Change]int{plan.ChangeUpdate: 1}}
	p.CommitRefused = p.SwitchOffRefusal()
	out := tools.CapabilityResult{Tool: tools.ToolReconcileCapability, Capability: installations.AgentPlatform, DryRun: true, Order: []string{"birch"}, Installations: []tools.DryRun{{Installation: p}}}
	var buf bytes.Buffer
	if err := Plan(&buf, out, false); err != nil {
		t.Fatal(err)
	}
	text := buf.String()
	warning := "  Warning: this reconcile switches off components running on birch:\n    agent-manager: the Dev Portal's Create agent and agent editing depend on it\n    extra\n"
	i, files := strings.Index(text, warning), strings.Index(text, "  Files (")
	if i < 0 || files < 0 || i > files {
		t.Fatalf("the warning is not printed ahead of the files:\n%s", text)
	}
	if !strings.Contains(text, "A commit would be refused: this reconcile switches off what runs on birch") {
		t.Errorf("the refusal is not printed:\n%s", text)
	}
}
