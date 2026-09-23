package e2e

// The record follows GitHub: pull requests merged outside merge_action by a
// person with the repositories' own merge path move the action to rolling
// out on the next read, with who merged them and the approval recorded as
// merged without approval; the watch re-reads the record through the
// manager's own get_action as the person first, so it works on
// such an action; a pull request closed unmerged fails the action naming it;
// and a fileset reverted out of the default branch again removes the action,
// naming the objects the definition rendered that stay on the installation
// until a person deletes them.

import (
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// dave merges and reverts with the repositories' own merge path, never
// through the manager: a login the fake GitHub names as the merger.
const dave = "dave"

// mergeAllOutside merges every open pull request of the remote as dave.
func mergeAllOutside(t *testing.T, st *stack) {
	t.Helper()
	for _, pr := range st.remote.PullRequests() {
		if !pr.Merged && !pr.Closed {
			st.mergeOutside(t, pr.PullRequest, dave)
		}
	}
}

// Both pull requests merged by hand: the next get_action reads them merged
// from GitHub with the commit, the time and dave as the merger, and the
// action rolls out as after merge_action — the approval recorded as merged
// without approval by dave, the thread told; the buttons and the merge
// follow the record, list_installations reads rolling out, and the watch
// carries the installation to enabled.
func TestPullRequestsMergedOutsideMoveTheActionToRollingOut(t *testing.T) {
	st := newStack(t)
	out, aliceC := commitRowan(t, st)
	name := out.Action.Name
	mergeAllOutside(t, st)
	if calls := st.calls(); len(calls) != 0 {
		t.Fatalf("a merge by hand reached the remote through the manager: %v", calls)
	}

	got := getAction(t, aliceC, name)
	if got.Status.State != actions.StateRollingOut || got.Status.Rollout == nil || got.Status.Rollout.StartedAt == nil || stageOf(&got, rowan).State != actions.StateRollingOut {
		t.Fatalf("after the merges by hand: %+v", got.Status)
	}
	remotePRs := st.remote.PullRequests()
	for i, pr := range got.Status.PullRequests {
		if pr.State != actions.PullRequestMerged || pr.MergedBy != dave || pr.MergedAt == nil || pr.MergeCommit == "" || pr.MergeCommit != remotePRs[i].HeadSHA {
			t.Fatalf("pull request on record: %+v (remote %+v)", pr, remotePRs[i].PullRequest)
		}
	}
	if ap := got.Status.Approval; ap == nil || ap.Decision != actions.DecisionMergedWithoutApproval || ap.DecidedBy != dave || ap.At == nil || ap.ReviewID == "" {
		t.Fatalf("the approval: %+v", got.Status.Approval)
	}
	if got.Status.SyncedAt == nil || got.Status.SyncedBy != alice {
		t.Fatalf("synced: %v as %q", got.Status.SyncedAt, got.Status.SyncedBy)
	}
	if want := acmeConfigs + ":" + installations.Capabilities()[0].EnabledMarker(rowan); got.Spec.Markers[rowan] != want {
		t.Fatalf("the marker on record: %q, want %q", got.Spec.Markers[rowan], want)
	}
	if th := thread(t, st); len(th) != 1 || !strings.Contains(th[0], "Merged outside the manager by "+dave) || !strings.Contains(th[0], "*"+rowan+"* is rolling out") || !strings.Contains(th[0], acmeMCs+"#1") || !strings.Contains(th[0], acmeConfigs+"#2") {
		t.Fatalf("the thread: %q", th)
	}

	// Nothing to approve, nothing left to merge: the watch decides.
	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: name}); !isErr || !strings.Contains(text, actions.StateRollingOut) || !strings.Contains(text, actions.DecisionMergedWithoutApproval+" by "+dave) {
		t.Fatalf("approve after the merges by hand: %v %s", isErr, text)
	}
	if _, text, isErr := mergeCall(t, aliceC, name); !isErr || !strings.Contains(text, rowan+" is "+actions.StateRollingOut) || !strings.Contains(text, tools.ToolWatchAction) {
		t.Fatalf("merge after the merges by hand: %v %s", isErr, text)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateRollingOut || r.Capabilities[0].LastAction == nil || r.Capabilities[0].LastAction.Name != name {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}

	populateStage(t, st.inst, got, rowan)
	w, text, isErr := watchCall(t, adminLive(t, st), name)
	if isErr || !w.Ready || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || w.Action.Status.Result == nil || w.Action.Status.Result.State != actions.StateEnabled {
		t.Fatalf("the watch: %v %s", isErr, text)
	}
	if th := thread(t, st); len(th) != 2 || !strings.Contains(th[1], "*"+rowan+"* is *"+actions.StateEnabled+"*") {
		t.Fatalf("the thread after the watch: %q", th)
	}
	// A further read moves nothing and tells the thread nothing.
	if again := getAction(t, aliceC, name); again.Status.State != actions.StateEnabled || len(thread(t, st)) != 2 {
		t.Fatalf("a further read: %+v", again.Status)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// The first call after the merges by hand is the watch, which reads with the
// forwarded identity: it has the manager's own get_action re-read the action
// as the person through muster — muster puts their GitHub grant
// on the call — and watches the action that is rolling out now.
func TestWatchActionReadsThePullRequestsThroughMuster(t *testing.T) {
	st := newStack(t)
	out, _ := commitRowan(t, st)
	mergeAllOutside(t, st)
	populateStage(t, st.inst, *out.Action, rowan)

	w, text, isErr := watchCall(t, adminLive(t, st), out.Action.Name)
	if isErr || !w.Ready || w.State != actions.StateEnabled || strings.Contains(w.Message, "not re-read") {
		t.Fatalf("the watch as the first read: %v %s", isErr, text)
	}
	if ap := w.Action.Status.Approval; ap == nil || ap.Decision != actions.DecisionMergedWithoutApproval || ap.DecidedBy != dave {
		t.Fatalf("the approval: %+v", w.Action.Status.Approval)
	}
	for _, pr := range w.Action.Status.PullRequests {
		if pr.State != actions.PullRequestMerged || pr.MergedBy != dave {
			t.Fatalf("pull request on record: %+v", pr)
		}
	}
	// The loop-back ran as the admin's GitHub grant: alice's.
	if w.Action.Status.SyncedBy != alice || !strings.Contains(st.logs.String(), "action_resynced") {
		t.Fatalf("synced by %q; log:\n%s", w.Action.Status.SyncedBy, st.logs.String())
	}
	if th := thread(t, st); len(th) != 2 || !strings.Contains(th[0], "Merged outside the manager by "+dave) || !strings.Contains(th[1], "*"+rowan+"* is *"+actions.StateEnabled+"*") {
		t.Fatalf("the thread: %q", th)
	}
}

// A pull request closed unmerged by hand fails the action on the next read,
// naming it and the one left open; the buttons follow the record — approve
// refused, deny closing what is left — and the watch has nothing to follow.
func TestPullRequestClosedUnmergedFailsTheAction(t *testing.T) {
	st := newStack(t)
	out, aliceC := commitRowan(t, st)
	name := out.Action.Name
	prs := st.remote.PullRequests()
	st.closeOutside(t, prs[1].PullRequest)

	got := getAction(t, aliceC, name)
	msg := ""
	if got.Status.Result != nil {
		msg = got.Status.Result.Message
	}
	if got.Status.State != actions.StateFailed || got.Status.Result == nil || got.Status.Result.State != actions.StateFailed ||
		!strings.Contains(msg, "closed unmerged outside the manager ("+acmeConfigs+"#2)") || !strings.Contains(msg, "1 pull request(s) stay open ("+acmeMCs+"#1)") || !strings.Contains(msg, tools.ToolDenyAction) {
		t.Fatalf("after the close by hand: %+v", got.Status)
	}
	if got.Status.PullRequests[1].State != actions.PullRequestClosed || got.Status.PullRequests[1].ClosedAt == nil || got.Status.PullRequests[0].State != actions.PullRequestOpen {
		t.Fatalf("pull requests on record: %+v", got.Status.PullRequests)
	}
	if th := thread(t, st); len(th) != 1 || !strings.Contains(th[0], "Closed unmerged outside the manager") || !strings.Contains(th[0], acmeConfigs+"#2") || !strings.Contains(th[0], "failed") {
		t.Fatalf("the thread: %q", th)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateFailed || r.Capabilities[0].LastAction.Result != actions.StateFailed {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}

	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: name}); !isErr || !strings.Contains(text, actions.StateFailed) {
		t.Fatalf("approve a failed action: %v %s", isErr, text)
	}
	d, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: name, tools.ArgReason: "closed by hand"})
	if isErr || d.Action.Status.State != actions.StateFailed || d.Action.Status.PullRequests[0].State != actions.PullRequestClosed || !strings.Contains(d.Action.Status.Result.Message, "denied by "+alice) {
		t.Fatalf("deny: %v %s", isErr, text)
	}
	if calls := st.calls(); len(calls) != 1 || calls[0] != alice+" "+commit.OpClose+" "+acmeMCs+"#1" {
		t.Fatalf("remote calls: %v", calls)
	}
	if _, text, isErr := watchCall(t, adminLive(t, st), name); !isErr || !strings.Contains(text, actions.StateFailed) {
		t.Fatalf("watch a failed action: %v %s", isErr, text)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// The pull requests reverted by hand after the merge: the marker is gone from
// the default branch again, and the next read moves the action to removed —
// naming the file and, the Kustomization over the tree not pruning, the
// objects the definition rendered that stay on the installation until a
// person deletes them; the thread is told, list_installations reads the
// files' state, and nothing watches, merges or denies a removed action.
func TestRevertOutsideRemovesTheActionNamingTheOrphans(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)

	// Merged through the manager: the marker is on the default branch, the
	// next read fills the merge facts in and moves nothing.
	got := getAction(t, aliceC, a.Name)
	if got.Status.State != actions.StateRollingOut || !st.ghs.has(acmeConfigs, marker) {
		t.Fatalf("after the merge: %s, marker on record %v", got.Status.State, st.ghs.has(acmeConfigs, marker))
	}
	for _, pr := range got.Status.PullRequests {
		if pr.State != actions.PullRequestMerged || pr.MergedBy != alice || pr.MergedAt == nil || pr.MergeCommit == "" {
			t.Fatalf("pull request on record: %+v", pr)
		}
	}

	for _, pr := range st.remote.PullRequests() {
		if pr.Merged {
			st.revertOutside(t, pr.PullRequest, dave)
		}
	}
	if st.ghs.has(acmeConfigs, marker) {
		t.Fatal("the marker survived the revert")
	}
	got = getAction(t, aliceC, a.Name)
	if got.Status.State != actions.StateRemoved || got.Status.Result == nil || got.Status.Result.State != actions.StateRemoved || got.Status.Rollout.FinishedAt == nil || stageOf(&got, rowan).State != actions.StateRemoved {
		t.Fatalf("after the revert: %+v", got.Status)
	}
	msg := got.Status.Result.Message
	if !strings.Contains(msg, rowan+" ("+marker+" in "+acmeConfigs+")") || !strings.Contains(msg, "does not prune") {
		t.Fatalf("the result: %q", msg)
	}
	orphans := got.Status.Orphans
	if len(orphans) == 0 || orphans[0].Kind != helmReleaseKind {
		t.Fatalf("orphans: %+v", orphans)
	}
	seen := map[actions.Orphan]bool{}
	for _, o := range orphans {
		if o.Installation != rowan || o.Kind == "Kustomization" || o.Name == "" || seen[o] {
			t.Fatalf("orphan: %+v (seen %v)", o, seen[o])
		}
		seen[o] = true
	}
	for _, want := range []actions.Orphan{
		{Installation: rowan, Kind: helmReleaseKind, Namespace: fluxNamespace, Name: platformRelease},
		{Installation: rowan, Kind: "Secret", Namespace: "agent-platform", Name: "muster-oauth-credentials"},
	} {
		if !seen[want] {
			t.Errorf("orphans lack %+v:\n%+v", want, orphans)
		}
	}
	if th := thread(t, st); len(th) != 2 || !strings.Contains(th[1], "*"+rowan+"*") || !strings.Contains(th[1], "removed") || !strings.Contains(th[1], helmReleaseKind+" "+fluxNamespace+"/"+platformRelease) {
		t.Fatalf("the thread: %q", th)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateNotEnabled || r.Capabilities[0].LastAction.Result != actions.StateRemoved {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}
	if _, text, isErr := watchCall(t, adminLive(t, st), a.Name); !isErr || !strings.Contains(text, actions.StateRemoved) {
		t.Fatalf("watch a removed action: %v %s", isErr, text)
	}
	for _, tool := range []string{tools.ToolMergeAction, tools.ToolDenyAction} {
		if text, isErr := call(t, aliceC, tool, map[string]any{tools.ArgAction: a.Name, tools.ArgReason: "gone"}); !isErr || !strings.Contains(text, actions.StateRemoved) {
			t.Fatalf("%s on a removed action: %v %s", tool, isErr, text)
		}
	}
	// Removed is the last word: a further read reads GitHub no more.
	if again := getAction(t, aliceC, a.Name); again.Status.SyncedAt == nil || !again.Status.SyncedAt.Equal(*got.Status.SyncedAt) || len(thread(t, st)) != 2 {
		t.Fatalf("a further read: %+v", again.Status)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}
