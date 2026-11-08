package plan

import (
	"slices"
	"strings"
	"testing"
)

// The agent-platform values patch on record runs kagent, the agent-manager
// and the cluster-manager; a policy listing the installation with kagent
// alone renders the agent-manager off and leaves the cluster-manager out.
const (
	runningPatch = `global:
  domain: birch.acme.test
components:
  kagent:
    enabled: true
  agent-manager:
    enabled: true
  cluster-manager:
    enabled: true
  modelServing:
    enabled: false
`
	narrowerPatch = `global:
  domain: birch.acme.test
components:
  kagent:
    enabled: true
  agent-manager:
    enabled: false
`
)

// A render that switches running components off names each, in the order on
// record, with what depends on it; the commit is refused naming them until
// the switch-off is forced, and the summaries lead with the warning.
func TestSwitchOffNamesTheRunningComponentsTheRenderDrops(t *testing.T) {
	p := &Installation{Name: birchName}
	p.switchOff(runningPatch, narrowerPatch, false)
	if p.SwitchOff == nil {
		t.Fatal("the render switches off the agent-manager and the cluster-manager and the plan records nothing")
	}
	if got := p.SwitchOff.Names(); !slices.Equal(got, []string{"agent-manager", "cluster-manager"}) {
		t.Errorf("switched off %v", got)
	}
	if d := p.SwitchOff.Components[0].DependedOnBy; !strings.Contains(d, "Create agent") {
		t.Errorf("the agent-manager's dependents: %q", d)
	}
	refusal := p.SwitchOffRefusal()
	for _, want := range []string{"switches off what runs on birch", "agent-manager (the Dev Portal's Create agent", "cluster-manager (", "forceSwitchOff; platformctl --force-switch-off"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("the refusal lacks %q:\n%s", want, refusal)
		}
	}
	if w := p.SwitchOff.Warning(); !strings.HasPrefix(w, "warning: switches off running agent-manager (") || !strings.Contains(w, "; cluster-manager (") {
		t.Errorf("the warning: %q", w)
	}
	if s := Summary(*p); len(s) == 0 || s[0] != p.SwitchOff.Warning() {
		t.Errorf("the summary does not lead with the warning: %q", s)
	}

	p = &Installation{Name: birchName}
	p.switchOff(runningPatch, narrowerPatch, true)
	if p.SwitchOff == nil || !p.SwitchOff.Forced || len(p.SwitchOff.Components) != 2 {
		t.Fatalf("forced, the warning still names every component: %+v", p.SwitchOff)
	}
	if r := p.SwitchOffRefusal(); r != "" {
		t.Errorf("forced, the commit is refused: %s", r)
	}
	if w := p.SwitchOff.Warning(); !strings.HasSuffix(w, " (forced)") {
		t.Errorf("the forced warning: %q", w)
	}
}

// A render that keeps every running component on, or switches on one that
// was off, records nothing; neither does a patch that does not decode.
func TestSwitchOffRecordsNothingWhereNothingRunningGoes(t *testing.T) {
	same := strings.Replace(runningPatch, "  modelServing:\n    enabled: false\n", "  modelServing:\n    enabled: true\n", 1)
	for name, c := range map[string]struct{ current, rendered string }{
		"unchanged":        {runningPatch, runningPatch},
		"one switched on":  {runningPatch, same},
		"none on record":   {"global: {}\n", narrowerPatch},
		"undecodable":      {"components: [\n", narrowerPatch},
		"render undecoded": {runningPatch, "components: [\n"},
	} {
		p := &Installation{Name: birchName}
		p.switchOff(c.current, c.rendered, false)
		if p.SwitchOff != nil {
			t.Errorf("%s: switch-off %+v", name, p.SwitchOff)
		}
		if r := p.SwitchOffRefusal(); r != "" {
			t.Errorf("%s: refused: %s", name, r)
		}
	}
	var none *SwitchOff
	if none.Warning() != "" || none.Names() != nil {
		t.Error("a plan without a switch-off warns")
	}
}
