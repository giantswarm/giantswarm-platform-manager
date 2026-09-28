package e2e

// A merged pull request reverted on the default branch: the files carry
// their content from before the merge again while every object on the
// installation stays healthy on the previous values. The watch reads the
// revert before the installation and moves the action to reverted, naming
// the reverting commit and its pull request — never enabled — and the actor
// withdraws it with deny_action, the reason on the record and in the
// review's thread. An action whose files still carry what it merged stays
// enabled, and deny_action refuses to withdraw it.

import (
	"strconv"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// enabledRowan is rowan's enablement merged and watched to enabled.
func enabledRowan(t *testing.T, st *stack) (actions.Action, *client.Client) {
	t.Helper()
	a, aliceC := rolledOut(t, st)
	w, text, isErr := watchCall(t, adminLive(t, st), a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled {
		t.Fatalf("the watch after the merge: %v %s", isErr, text)
	}
	for _, pr := range w.Action.Status.PullRequests {
		if pr.Revert != nil {
			t.Fatalf("a pull request reads reverted after its merge: %+v", pr)
		}
	}
	return *w.Action, aliceC
}

// revertMCs reverts the action's merged pull request in the
// management-clusters repository by hand, as dave: the files it created
// leave the default branch again, while the configs pull request — and the
// marker with it — stays. It answers the revert pull request.
func revertMCs(t *testing.T, st *stack) commit.FakePullRequest {
	t.Helper()
	for _, pr := range st.remote.PullRequests() {
		if pr.Merged && pr.Repository.String() == acmeMCs {
			st.revertOutside(t, pr.PullRequest, dave)
		}
	}
	prs := st.remote.PullRequests()
	revert := prs[len(prs)-1]
	if !revert.Merged || !strings.HasPrefix(revert.Title, "revert:") || !st.ghs.has(acmeConfigs, installations.Capabilities()[0].EnabledMarker(rowan)) {
		t.Fatalf("the revert: %+v", revert)
	}
	return revert
}

// The revert read by the watch: reverted, naming the commit and the pull
// request, the thread told; nothing moves it back, the approver cannot
// withdraw it, and the actor withdraws it with the reason.
func TestWatchReadsARevertedActionRevertedAndTheActorWithdrawsIt(t *testing.T) {
	st := newStack(t)
	a, aliceC := enabledRowan(t, st)
	admin := adminLive(t, st)

	// Still carrying what it merged: the re-read stays enabled, and the
	// merges' changes are read once — the re-read costs the trees alone.
	read := st.ghs.commits(acmeMCs) + st.ghs.commits(acmeConfigs)
	if read != 2 {
		t.Fatalf("the merge commits were read %d time(s), want once each", read)
	}
	if w, text, isErr := watchCall(t, admin, a.Name); isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled {
		t.Fatalf("the re-read of an enabled action: %v %s", isErr, text)
	}
	if again := st.ghs.commits(acmeMCs) + st.ghs.commits(acmeConfigs); again != read {
		t.Fatalf("the re-read read the merge commits again: %d, was %d", again, read)
	}

	revert := revertMCs(t, st)
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateReverted || w.Action.Status.State != actions.StateReverted || w.Action.Status.Result.State != actions.StateReverted || strings.Contains(w.Message, actions.StateEnabled) {
		t.Fatalf("the watch after the revert: %v %s", isErr, text)
	}
	var mcs actions.PullRequest
	for _, pr := range w.Action.Status.PullRequests {
		switch pr.Repository {
		case acmeMCs:
			mcs = pr
		case acmeConfigs:
			if pr.Revert != nil {
				t.Errorf("the configs pull request reads reverted: %+v", pr.Revert)
			}
		}
	}
	if rv := mcs.Revert; rv == nil || rv.Commit != revert.HeadSHA || rv.PullRequest != revert.Number || rv.PullRequestURL == "" || rv.At == nil {
		t.Fatalf("the revert on record: %+v (the revert %s#%d at %s)", mcs.Revert, revert.Repository, revert.Number, revert.HeadSHA)
	}
	named := acmeMCs + "#" + strconv.Itoa(revert.Number)
	if stage := stageOf(w.Action, rowan); stage.State != actions.StateReverted || !strings.Contains(stage.Message, named) || !strings.Contains(w.Action.Status.Result.Message, named) {
		t.Fatalf("the stage: %+v, the result %q", stage, w.Action.Status.Result.Message)
	}
	if !strings.Contains(w.Next, tools.ToolDenyAction) || !strings.Contains(w.Report, named) {
		t.Fatalf("next %q, report %q", w.Next, w.Report)
	}
	if th := thread(t, st); !strings.Contains(th[len(th)-1], "*reverted*") || !strings.Contains(th[len(th)-1], named) {
		t.Fatalf("the thread: %q", th)
	}

	// Nothing moves it back: the record reads reverted, the watch refuses.
	if got := getAction(t, aliceC, a.Name); got.Status.State != actions.StateReverted {
		t.Fatalf("get_action after the revert: %s", got.Status.State)
	}
	if _, text, isErr := watchCall(t, admin, a.Name); !isErr || !strings.Contains(text, "is reverted") || !strings.Contains(text, tools.ToolDenyAction) {
		t.Fatalf("a second watch: %v %s", isErr, text)
	}

	// The approver does not withdraw; the actor does, with the reason.
	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolDenyAction, map[string]any{tools.ArgAction: a.Name, tools.ArgReason: "reverted"}); !isErr || !strings.Contains(text, "withdrawn by its actor ("+alice+")") {
		t.Fatalf("a withdrawal by the approver: %v %s", isErr, text)
	}
	if _, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, tools.ArgReason) {
		t.Fatalf("a withdrawal without a reason: %v %s", isErr, text)
	}
	const reason = "the portal's new pod could not start; reverted by hand"
	d, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: a.Name, tools.ArgReason: reason})
	if isErr || d.Action.Status.State != actions.StateWithdrawn || d.Action.Status.Withdrawal == nil || d.Action.Status.Withdrawal.By != alice || d.Action.Status.Withdrawal.Reason != reason ||
		!strings.Contains(d.Action.Status.Result.Message, "withdrawn by "+alice+": "+reason) || !strings.Contains(d.Action.Status.Result.Message, named) ||
		d.Action.Status.Approval.Decision != actions.DecisionApproved || stageOf(d.Action, rowan).State != actions.StateWithdrawn {
		t.Fatalf("the withdrawal: %v %s", isErr, text)
	}
	if th := thread(t, st); !strings.Contains(th[len(th)-1], "Withdrawn by *"+alice+"*: "+reason) {
		t.Fatalf("the thread: %q", th)
	}
	if got := getAction(t, aliceC, a.Name); got.Status.State != actions.StateWithdrawn {
		t.Fatalf("get_action after the withdrawal: %s", got.Status.State)
	}
	if _, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: a.Name, tools.ArgReason: reason}); !isErr || !strings.Contains(text, "is "+actions.StateWithdrawn) {
		t.Fatalf("a second withdrawal: %v %s", isErr, text)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// An action that reads enabled with its pull request reverted and no watch
// since: deny_action reads the revert itself as the actor, records it and
// withdraws the action; before the revert it refuses — nothing was taken
// back.
func TestTheActorWithdrawsAnEnabledActionWhosePullRequestWasReverted(t *testing.T) {
	st := newStack(t)
	a, aliceC := enabledRowan(t, st)
	const reason = "rolled back by hand"
	if _, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: a.Name, tools.ArgReason: reason}); !isErr || !strings.Contains(text, "nothing was taken back") {
		t.Fatalf("withdrawing an action whose pull requests stand: %v %s", isErr, text)
	}
	if got := getAction(t, aliceC, a.Name); got.Status.State != actions.StateEnabled {
		t.Fatalf("after the refusal: %s", got.Status.State)
	}

	revert := revertMCs(t, st)
	d, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: a.Name, tools.ArgReason: reason})
	if isErr || d.Action.Status.State != actions.StateWithdrawn || d.Action.Status.Withdrawal == nil || d.Action.Status.Withdrawal.Reason != reason {
		t.Fatalf("the withdrawal: %v %s", isErr, text)
	}
	named := acmeMCs + "#" + strconv.Itoa(revert.Number)
	if !strings.Contains(d.Action.Status.Result.Message, "the action was "+actions.StateReverted) || !strings.Contains(d.Action.Status.Result.Message, named) {
		t.Fatalf("the result: %q", d.Action.Status.Result.Message)
	}
	th := thread(t, st)
	if len(th) < 2 || !strings.Contains(th[len(th)-2], "*reverted*") || !strings.Contains(th[len(th)-2], named) || !strings.Contains(th[len(th)-1], "Withdrawn by *"+alice+"*: "+reason) {
		t.Fatalf("the thread: %q", th)
	}
}
