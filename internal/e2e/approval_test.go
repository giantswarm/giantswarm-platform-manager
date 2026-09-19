package e2e

// The approval of an action: the review posted on the commit, Approve as a
// member (never the actor) submitting the approving reviews as that member,
// merge_action merging as the actor once green and posting into the thread,
// Deny closing the pull requests with the reason, and the re-post of a review
// the gateway no longer holds.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// commitRowan commits the enablement of rowan as alice and returns the answer.
func commitRowan(t *testing.T, st *stack) (tools.CommitResult, *client.Client) {
	t.Helper()
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	inputs := minimalInputs(nil)
	out, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr || out.Action == nil || len(out.PullRequests) != 2 {
		t.Fatal(text)
	}
	return out, c
}

func decide(t *testing.T, c *client.Client, tool string, args map[string]any) (tools.Decision, string, bool) {
	t.Helper()
	text, isErr := call(t, c, tool, args)
	var d tools.Decision
	if !isErr && json.Unmarshal([]byte(text), &d) != nil {
		t.Fatalf("decode %s: %s", tool, text)
	}
	return d, text, isErr
}

func mergeCall(t *testing.T, c *client.Client, name string) (tools.MergeResult, string, bool) {
	t.Helper()
	text, isErr := call(t, c, tools.ToolMergeAction, map[string]any{tools.ArgAction: name})
	var m tools.MergeResult
	if !isErr && json.Unmarshal([]byte(text), &m) != nil {
		t.Fatalf("decode merge_action: %s", text)
	}
	return m, text, isErr
}

func getAction(t *testing.T, c *client.Client, name string) actions.Action {
	t.Helper()
	var a actions.Action
	if text, isErr := call(t, c, tools.ToolGetAction, map[string]any{tools.ArgName: name}); isErr || json.Unmarshal([]byte(text), &a) != nil {
		t.Fatalf("get_action: %s", text)
	}
	return a
}

// The commit posts one review with the actor's email, the pull requests, both
// buttons carrying the action id, the team's channel and — rowan being a
// customer installation — the notice channel; the receipt is on the Action.
func TestCommitPostsTheTeamReview(t *testing.T) {
	st := newStack(t)
	out, _ := commitRowan(t, st)
	a := out.Action
	if a.Status.Approval == nil || a.Status.Approval.ReviewID == "" || a.Status.Approval.Channel != reviewChannel || a.Status.Approval.NoticeChannel != noticeChannel || a.Status.Approval.Decision != "" || a.Status.Approval.PostedAt == nil {
		t.Fatalf("approval on the action: %+v", a.Status.Approval)
	}
	if !a.Spec.Customer || a.Spec.Actor.Email != alice+"@example.test" || a.Spec.Change == "" || !strings.Contains(out.Next, tools.ToolMergeAction) {
		t.Fatalf("spec %+v next %q", a.Spec, out.Next)
	}
	posted := st.gateway.posted()
	if len(posted) != 1 || posted[0].ID != a.Status.Approval.ReviewID {
		t.Fatalf("the gateway received %d review(s): %+v", len(posted), posted)
	}
	body := posted[0].Body
	approve, _ := body["approve"].(map[string]any)
	deny, _ := body["deny"].(map[string]any)
	if body["team"] != reviewTeam || body["channel"] != reviewChannel || body["noticeChannel"] != noticeChannel || body["actor"] != alice+"@example.test" ||
		approve["tool"] != "x_"+tools.ToolPrefix+"_"+tools.ToolApproveAction || deny["tool"] != "x_"+tools.ToolPrefix+"_"+tools.ToolDenyAction ||
		approve["arguments"].(map[string]any)[tools.ArgAction] != a.Name || deny["arguments"].(map[string]any)[tools.ArgAction] != a.Name {
		t.Fatalf("review body: %v", body)
	}
	prs, _ := body["pullRequests"].([]any)
	if len(prs) != 2 || prs[0] != out.PullRequests[0].URL || prs[1] != out.PullRequests[1].URL {
		t.Fatalf("review pull requests: %v", prs)
	}
	text, _ := body["text"].(string)
	for _, want := range []string{alice, rowan, a.Spec.Capability, "customer"} {
		if !strings.Contains(text, want) {
			t.Fatalf("review text lacks %q: %s", want, text)
		}
	}
	assertNoLeak(t, "the review", fmt.Sprint(body))
	assertNoLeak(t, "the server's log", st.logs.String())
	if !strings.Contains(st.logs.String(), "action_review_posted") {
		t.Fatalf("the log has no review record:\n%s", st.logs.String())
	}
}

// The actor's Approve is refused; a member's Approve submits an approving
// review on every pull request as that member; the actor merges as the actor
// once the checks are green, in order, stopping at a pending check; the
// Action becomes rolling out and the thread is told.
func TestApproveAsMemberThenMergeAsActor(t *testing.T) {
	st := newStack(t)
	out, aliceC := commitRowan(t, st)
	name := out.Action.Name
	carolC := st.mcpClient(t, carolToken)

	if _, text, isErr := decide(t, aliceC, tools.ToolApproveAction, map[string]any{tools.ArgAction: name}); !isErr || !strings.Contains(text, "yours") {
		t.Fatalf("the actor's approve: %v %s", isErr, text)
	}
	if calls := st.calls(); len(calls) != 0 {
		t.Fatalf("the actor's approve reached the remote: %v", calls)
	}
	if got := getAction(t, aliceC, name); got.Status.Approval.Decision != "" {
		t.Fatalf("the actor's approve was recorded: %+v", got.Status.Approval)
	}
	if _, text, isErr := mergeCall(t, carolC, name); !isErr || !strings.Contains(text, "actor") {
		t.Fatalf("a member's merge: %v %s", isErr, text)
	}
	if m, text, isErr := mergeCall(t, aliceC, name); isErr || !strings.Contains(m.Message, "waits for the team's approval") || len(m.Merged) != 0 {
		t.Fatalf("merge before the approval: %v %s", isErr, text)
	}
	if posted := st.gateway.posted(); len(posted) != 1 || len(posted[0].Results) != 1 {
		t.Fatalf("the waiting note: %+v", posted)
	}

	d, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: name})
	if isErr || !strings.Contains(d.Message, carol) || !strings.Contains(d.Message, tools.ToolMergeAction) || d.Action == nil {
		t.Fatalf("carol's approve: %v %s", isErr, text)
	}
	want := []string{carol + " " + commit.OpApprove + " " + acmeConfigs + "#1", carol + " " + commit.OpApprove + " " + acmeMCs + "#2"}
	if calls := st.calls(); !slices.Equal(calls, want) {
		t.Fatalf("remote calls %v, want %v", calls, want)
	}
	for _, pr := range st.remote.PullRequests() {
		if pr.Review != commit.ReviewApproved || len(pr.Approvals) != 1 || !strings.Contains(pr.Approvals[0], carol) || !strings.Contains(pr.Approvals[0], name) {
			t.Fatalf("remote review of %s#%d: %+v", pr.Repository, pr.Number, pr)
		}
	}
	got := getAction(t, aliceC, name)
	if got.Status.State != actions.StatePendingApproval || got.Status.Approval.Decision != actions.DecisionApproved || got.Status.Approval.DecidedBy != carol || got.Status.Approval.At == nil {
		t.Fatalf("after the approval: %+v", got.Status)
	}
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: name}); !isErr || !strings.Contains(text, "already approved") {
		t.Fatalf("a second approve: %v %s", isErr, text)
	}

	// The checks: green on the first, pending on the second, then green.
	remotePRs := st.remote.PullRequests()
	st.remote.SetChecks(remotePRs[0].PullRequest, commit.ChecksSuccess)
	st.remote.SetChecks(remotePRs[1].PullRequest, commit.ChecksPending)
	m, text, isErr := mergeCall(t, aliceC, name)
	if isErr || len(m.Merged) != 1 || m.Merged[0].Repository != acmeConfigs || !strings.Contains(m.Waiting, acmeMCs+"#2") || m.Action.Status.State != actions.StatePendingApproval {
		t.Fatalf("merge with a pending check: %v %s", isErr, text)
	}
	st.remote.SetChecks(remotePRs[1].PullRequest, commit.ChecksSuccess)
	m, text, isErr = mergeCall(t, aliceC, name)
	if isErr || len(m.Merged) != 1 || m.Merged[0].Repository != acmeMCs || m.Waiting != "" || m.Action.Status.State != actions.StateRollingOut || m.Action.Status.Rollout == nil || m.Action.Status.Rollout.StartedAt == nil {
		t.Fatalf("merge once green: %v %s", isErr, text)
	}
	if calls := st.calls(); len(calls) != 4 || !strings.HasPrefix(calls[2], alice+" "+commit.OpMerge+" "+acmeConfigs) || !strings.HasPrefix(calls[3], alice+" "+commit.OpMerge+" "+acmeMCs) {
		t.Fatalf("remote calls %v", calls)
	}
	for _, pr := range st.remote.PullRequests() {
		if !pr.Merged {
			t.Fatalf("%s#%d is not merged", pr.Repository, pr.Number)
		}
	}
	got = getAction(t, aliceC, name)
	for _, pr := range got.Status.PullRequests {
		if pr.State != actions.PullRequestMerged {
			t.Fatalf("recorded pull request %+v", pr)
		}
	}
	posted := st.gateway.posted()
	if len(posted) != 1 || len(posted[0].Results) != 2 || !strings.Contains(fmt.Sprint(posted[0].Results[1]["text"]), "rolling out") || !strings.Contains(fmt.Sprint(posted[0].Results[1]["text"]), alice) {
		t.Fatalf("the thread: %+v", posted)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateRollingOut || r.Capabilities[0].LastAction.Name != name {
		t.Fatalf("after the merge: %+v", r.Capabilities[0])
	}
	// The next call is the verify of the installation rolling out: green
	// (every probe answers), the wave of one is done.
	if m, text, isErr := mergeCall(t, aliceC, name); isErr || m.Verified != rowan || m.Action.Status.State != actions.StateEnabled || m.Action.Status.Rollout.FinishedAt == nil || len(m.Action.Status.Probes) == 0 {
		t.Fatalf("merge again: %v %s", isErr, text)
	}
	if _, text, isErr := mergeCall(t, aliceC, name); !isErr || !strings.Contains(text, actions.StateEnabled) {
		t.Fatalf("merge a done action: %v %s", isErr, text)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// Deny — by the actor too — needs a reason, closes every pull request as the
// member and records the denial; the installation's state stands.
func TestDenyClosesThePullRequestsWithTheReason(t *testing.T) {
	st := newStack(t)
	out, aliceC := commitRowan(t, st)
	name := out.Action.Name
	if _, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: name}); !isErr || !strings.Contains(text, tools.ArgReason) {
		t.Fatalf("deny without a reason: %v %s", isErr, text)
	}
	d, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: name, tools.ArgReason: "wrong base domain"})
	if isErr || !strings.Contains(d.Message, "wrong base domain") || d.Action.Status.State != actions.StateDenied {
		t.Fatalf("deny: %v %s", isErr, text)
	}
	want := []string{alice + " " + commit.OpClose + " " + acmeConfigs + "#1", alice + " " + commit.OpClose + " " + acmeMCs + "#2"}
	if calls := st.calls(); !slices.Equal(calls, want) {
		t.Fatalf("remote calls %v, want %v", calls, want)
	}
	for _, pr := range st.remote.PullRequests() {
		if !pr.Closed || pr.Merged {
			t.Fatalf("%s#%d after the denial: %+v", pr.Repository, pr.Number, pr)
		}
	}
	got := getAction(t, aliceC, name)
	if got.Status.Approval.Decision != actions.DecisionDenied || got.Status.Approval.Reason != "wrong base domain" || got.Status.Approval.DecidedBy != alice || got.Status.Result == nil || got.Status.Result.State != actions.StateDenied ||
		got.Status.PullRequests[0].State != actions.PullRequestClosed || got.Status.PullRequests[1].State != actions.PullRequestClosed {
		t.Fatalf("after the denial: %+v", got.Status)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateNotEnabled || r.Capabilities[0].LastAction.Result != actions.StateDenied {
		t.Fatalf("after the denial: %+v", r.Capabilities[0])
	}
	for _, tool := range []string{tools.ToolApproveAction, tools.ToolDenyAction, tools.ToolMergeAction} {
		if text, isErr := call(t, aliceC, tool, map[string]any{tools.ArgAction: name, tools.ArgReason: "again"}); !isErr || !strings.Contains(text, actions.StateDenied) {
			t.Fatalf("%s on a denied action: %v %s", tool, isErr, text)
		}
	}
}

// A review the gateway no longer holds is re-posted when the actor asks to
// merge — and only then: a review the gateway holds is not posted twice.
func TestReviewIsRepostedOnlyWhenTheGatewayLostIt(t *testing.T) {
	st := newStack(t)
	out, aliceC := commitRowan(t, st)
	name, first := out.Action.Name, out.Action.Status.Approval.ReviewID
	if _, _, isErr := mergeCall(t, aliceC, name); isErr || len(st.gateway.posted()) != 1 {
		t.Fatalf("a held review was re-posted: %+v", st.gateway.posted())
	}
	st.gateway.forget(first)
	m, text, isErr := mergeCall(t, aliceC, name)
	if isErr || !strings.Contains(m.Message, "re-posted") || m.Action.Status.Approval.ReviewID == first || m.Action.Status.Approval.ReviewID == "" {
		t.Fatalf("after the gateway lost the review: %v %s", isErr, text)
	}
	if posted := st.gateway.posted(); len(posted) != 1 || posted[0].ID != m.Action.Status.Approval.ReviewID {
		t.Fatalf("re-post: %+v", posted)
	}
	if got := getAction(t, aliceC, name); got.Status.Approval.ReviewID != m.Action.Status.Approval.ReviewID || got.Status.State != actions.StatePendingApproval {
		t.Fatalf("recorded: %+v", got.Status)
	}
	if !strings.Contains(st.logs.String(), "action_review_gone") {
		t.Fatalf("the log does not record the lost review:\n%s", st.logs.String())
	}
}
