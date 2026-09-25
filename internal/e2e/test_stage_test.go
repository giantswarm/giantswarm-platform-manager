package e2e

// The review follows the stage: an action on Giant Swarm's test
// installations alone — the hub's customer's, not the hub — posts no Team
// review, merges as the actor once green and tells the team's standup
// channel in one sentence; an action on a customer installation keeps the
// review, and merge_action waits for it without telling the standup channel.

import (
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// rowanAsTestInstallation relabels rowan in the catalog as the hub's
// customer's, its repositories unchanged: one of Giant Swarm's test
// installations, whose platform joins the hub's portal tree — on record in
// rowan's management-clusters repository as in the hub's.
func rowanAsTestInstallation(t *testing.T, g *fakeGitHub) {
	t.Helper()
	catalog, ok := g.file(registryRepo, registryPath)
	customer := resource(rowan, acme, "capa", "acme.test")
	if !ok || !strings.Contains(catalog, customer) {
		t.Fatal("the catalog carries no customer rowan")
	}
	test := strings.NewReplacer("giantswarm.io/customer: "+acme, "giantswarm.io/customer: example", "owner: "+acme, "owner: example").Replace(customer)
	g.addFile(registryRepo, registryPath, strings.Replace(catalog, customer, test, 1))
	for _, p := range []string{"management-clusters/" + hub + "/extras/backstage/kustomization.yaml", "management-clusters/" + hub + "/extras/backstage/backstage/kustomization.yaml"} {
		content, ok := g.file(hubMCs, p)
		if !ok {
			t.Fatalf("the hub's portal tree carries no %s", p)
		}
		g.addFile(acmeMCs, p, content)
	}
}

func TestTestInstallationNeedsNoReviewAndTellsTheStandupChannel(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	rowanAsTestInstallation(t, st.ghs)
	seedRemote(t, st)
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)

	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || out.Action == nil || len(out.PullRequests) != 2 {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	a := out.Action
	if a.Spec.Customer || a.Status.State != actions.StatePendingApproval || a.Status.Approval == nil || a.Status.Approval.Decision != actions.DecisionNotRequired || a.Status.Approval.ReviewID != "" || !strings.Contains(out.Next, "no Team review") {
		t.Fatalf("the action on a test installation: %+v %+v; next %q", a.Spec, a.Status.Approval, out.Next)
	}
	if posted, noticed := st.gateway.posted(), st.gateway.noticed(); len(posted) != 0 || len(noticed) != 0 {
		t.Fatalf("the commit posted %d review(s), %d notice(s)", len(posted), len(noticed))
	}
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "needs no review") {
		t.Fatalf("an approval of an action that needs none: %v %s", isErr, text)
	}

	// Green: alice merges without anyone's approval; one sentence reaches
	// the standup channel with both pull requests, and no review.
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	m, text, isErr := mergeCall(t, aliceC, a.Name)
	if isErr || m.Stage != rowan || len(m.Merged) != 2 || m.Action.Status.State != actions.StateRollingOut {
		t.Fatalf("merge: %v %s", isErr, text)
	}
	noticed := st.gateway.noticed()
	if len(noticed) != 1 || len(st.gateway.posted()) != 0 {
		t.Fatalf("after the merge: %d notice(s) %+v, %d review(s)", len(noticed), noticed, len(st.gateway.posted()))
	}
	n := noticed[0]
	prs, _ := n["pullRequests"].([]any)
	if n["team"] != reviewTeam || n["channel"] != standupChannel || n["text"] != "*"+alice+"* enabled *agent-platform* on *"+rowan+"*." || len(prs) != 2 || prs[0] != m.Merged[0].URL {
		t.Fatalf("the standup notice: %+v", n)
	}
}

func TestCustomerInstallationKeepsTheReview(t *testing.T) {
	st := newStack(t)
	out, aliceC := commitRowan(t, st)
	if out.Action.Status.Approval == nil || out.Action.Status.Approval.ReviewID == "" || out.Action.Status.Approval.Decision != "" {
		t.Fatalf("the action on a customer installation: %+v", out.Action.Status.Approval)
	}
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	if m, text, isErr := mergeCall(t, aliceC, out.Action.Name); isErr || !strings.Contains(m.Message, "waits for the team's approval") || len(m.Merged) != 0 {
		t.Fatalf("merge before the approval: %v %s", isErr, text)
	}
	if posted, noticed := st.gateway.posted(), st.gateway.noticed(); len(posted) != 1 || len(noticed) != 0 {
		t.Fatalf("%d review(s), %d standup notice(s)", len(posted), len(noticed))
	}
}
