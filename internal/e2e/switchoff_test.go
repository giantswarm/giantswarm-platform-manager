package e2e

// A policy that drops a running component: rowan's organisation runs kagent
// and the agent-manager by policy, and its agent-platform values patch on
// record runs the cluster-manager too, and repeats the agent-platform-mcps
// toggle the shared defaults set. The dry run names the cluster-manager alone
// as switched off — the template keeps agent-platform-mcps running —, apart
// from the drift, and refuses the commit; the commit
// is refused, nothing written; forced, the commit proceeds and the action's
// summary leads with the warning.

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func TestReconcileWarnsOfARunningComponentThePolicyDrops(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	repo, path, content := onRecord(t, st, "/"+rowan+"/apps/agent-platform/configmap-values.yaml.patch")
	running := strings.Replace(content, "  agent-manager:\n    enabled: true\n", "  agent-manager:\n    enabled: true\n  cluster-manager:\n    enabled: true\n  agent-platform-mcps:\n    enabled: true\n", 1)
	if running == content {
		t.Fatalf("the patch on record lacks the agent-manager toggle:\n%s", content)
	}
	st.ghs.addFile(repo, path, running)

	p := reconcileDryRun(t, c)
	if p.SwitchOff == nil || !slices.Equal(p.SwitchOff.Names(), []string{"cluster-manager"}) || p.SwitchOff.Forced {
		t.Fatalf("the dry run's switch-off: %+v", p.SwitchOff)
	}
	if !strings.Contains(p.CommitRefused, "switches off what runs on "+rowan+": cluster-manager (") || !strings.Contains(p.CommitRefused, "forceSwitchOff") {
		t.Errorf("commitRefused: %q", p.CommitRefused)
	}

	before := len(st.remote.PullRequests())
	_, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, reconcileArgs(nil))
	if !isErr || !strings.Contains(text, "switches off what runs on "+rowan) || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("the commit over a switch-off: %v %s", isErr, text)
	}
	if prs := st.remote.PullRequests(); len(prs) != before {
		t.Fatalf("a refused commit opened pull requests: %+v", prs)
	}

	args := reconcileArgs(nil)
	args[tools.ArgForceSwitchOff] = true
	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args)
	if isErr || out.Action == nil || out.Plan.SwitchOff == nil || !out.Plan.SwitchOff.Forced {
		t.Fatalf("the forced commit: %v %s", isErr, text)
	}
	changes := out.Action.Spec.Changes[rowan]
	if len(changes) == 0 || !strings.HasPrefix(changes[0], "warning: switches off running cluster-manager (") || !strings.HasSuffix(changes[0], "(forced)") {
		t.Errorf("the action's summary does not lead with the warning: %q", changes)
	}
	if !strings.HasPrefix(out.Action.Spec.Change, "warning: switches off running cluster-manager (") {
		t.Errorf("the action's change: %q", out.Action.Spec.Change)
	}
}
