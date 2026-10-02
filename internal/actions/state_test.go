package actions

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// An action recorded in pending approval whose approval is not required —
// a commit or a wave over test installations from before the state of its
// own — reads ready to merge, its stages too; one that waits for the Team
// review still reads pending approval.
func TestPendingApprovalWithoutReviewReadsReadyToMerge(t *testing.T) {
	recorded := func(decision string) Action {
		return Action{Name: "reconcile-wave-1", Namespace: "platform-manager",
			Status: Status{State: StatePendingApproval, Approval: &Approval{Decision: decision},
				Rollout: &Rollout{Installations: []InstallationRollout{{Name: "rowan", State: StatePendingApproval}, {Name: "birch", State: StateEnabled}}}}}
	}
	got, err := fromUnstructured(Unstructured(recorded(DecisionNotRequired)))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.State != StateReadyToMerge || got.Status.Rollout.Installations[0].State != StateReadyToMerge || got.Status.Rollout.Installations[1].State != StateEnabled {
		t.Fatalf("no review: %+v %+v", got.Status.State, got.Status.Rollout.Installations)
	}
	for _, decision := range []string{"", DecisionApproved} {
		got, err := fromUnstructured(Unstructured(recorded(decision)))
		if err != nil {
			t.Fatal(err)
		}
		if got.Status.State != StatePendingApproval || got.Status.Rollout.Installations[0].State != StatePendingApproval {
			t.Fatalf("decision %q: %+v %+v", decision, got.Status.State, got.Status.Rollout.Installations)
		}
	}
	if !installations.State(StateReadyToMerge).FromAction() {
		t.Fatal("ready to merge does not stand over the state read from the files")
	}
}

// The CRD's state enum carries every state an Action records: a state it
// lacks is refused by the API server on the status update.
func TestCRDStateEnumCarriesEveryState(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "helm", "giantswarm-platform-manager", "templates", "crd-actions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var enum string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, `enum: ["pending approval"`) {
			enum = l
		}
	}
	for _, s := range []string{StatePendingApproval, StateReadyToMerge, StateRollingOut, StateWaitingForCustomer, StateEnabled, StateDrifted, StateFailed, StateRefused, StateDenied, StateReverted, StateWithdrawn, StateRemoved} {
		if !slices.ContainsFunc([]string{`"` + s + `"`, " " + s + ",", " " + s + "]"}, func(form string) bool { return strings.Contains(enum, form) }) {
			t.Errorf("the CRD's state enum lacks %q: %s", s, strings.TrimSpace(enum))
		}
	}
}
