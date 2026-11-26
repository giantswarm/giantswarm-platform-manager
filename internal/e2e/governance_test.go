package e2e

// Governing tool calls (giantswarm/giantswarm#38902): one person never
// changes an installation's governance alone. A commit that changes the
// governance is an Action a second person approves, on Giant Swarm's test
// installations too, where any other commit needs no review; the review
// reads the governance old → new and what loosens; the actor cannot approve
// it, and nothing merges before a member did.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// The governance input's keys.
const (
	keyGovernanceInput = "governance"
	keyMode            = "mode"
)

// withGovernanceReleases takes the meta chart and muster releases that
// render and read the governance as cut, for the test. Not parallel.
func withGovernanceReleases(t *testing.T) {
	t.Helper()
	chart, muster := agentplatform.GovernanceChart, agentplatform.GovernanceMuster
	agentplatform.GovernanceChart, agentplatform.GovernanceMuster = "4.0.0", "5.0.0"
	t.Cleanup(func() { agentplatform.GovernanceChart, agentplatform.GovernanceMuster = chart, muster })
}

// rowanOnLineFour puts rowan's record on the 4 chart line, the line that
// takes the governance, on a cluster that serves what kagent needs there.
func rowanOnLineFour(g *fakeGitHub) {
	g.addFile(acmeConfigs, installations.ConfigPatchPath(rowan), "codename: rowan\nbase: acme.test\nservices:\n  muster:\n    clientId: muster-rowan\ngatewayApi:\n  clusterIssuer: private-example\nagentPlatform:\n  kagentApiV2: true\n")
	g.addFile(acmeMCs, installations.ClusterAppManifestPath(rowan), clusterAppManifest(rowan, "cluster-aws", "10.3.0", true))
}

func TestGovernanceChangeOnATestInstallationNeedsASecondPerson(t *testing.T) {
	withGovernanceReleases(t)
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	rowanAsTestInstallation(t, st.ghs)
	rowanOnLineFour(st.ghs)
	seedRemote(t, st)
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)

	governance := map[string]any{keyMode: agentplatform.GovernanceGoverned, "governedServers": map[string]any{"mcp-kubernetes": nil}}
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keyGovernanceInput: governance})})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	a := out.Action
	if a.Status.State != actions.StatePendingApproval || a.Status.Approval == nil || a.Status.Approval.Decision != "" || a.Status.Approval.ReviewID == "" || strings.Contains(out.Next, "no Team review") {
		t.Fatalf("a governance change on a test installation went without the review: %+v; next %q", a.Status.Approval, out.Next)
	}
	g, ok := a.Spec.Governance[rowan]
	if !ok || len(g.Lines) != 2 || len(g.Loosens) != 1 || !strings.Contains(g.Loosens[0], "mcp-kubernetes") {
		t.Fatalf("the action's governance: %+v", a.Spec.Governance)
	}
	posted := st.gateway.posted()
	if len(posted) != 1 {
		t.Fatalf("%d review(s) posted", len(posted))
	}
	review := fmt.Sprint(posted[0].Body["text"])
	for _, want := range []string{"*Governance:* this changes the platform's governance", "mode trusted (the chart's default) → governed", "governedServers.mcp-kubernetes the chart's entry → null (not governed)", "*It loosens governance:*"} {
		if !strings.Contains(review, want) {
			t.Fatalf("the review carries no %q:\n%s", want, review)
		}
	}

	// The actor cannot approve it, nor merge it before a member did.
	if _, text, isErr := decide(t, aliceC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "yours") {
		t.Fatalf("the actor's approve: %v %s", isErr, text)
	}
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	if m, text, isErr := mergeCall(t, aliceC, a.Name); isErr || len(m.Merged) != 0 || !strings.Contains(m.Message, "waits for the team's approval") {
		t.Fatalf("merge before the approval: %v %s", isErr, text)
	}
	if d, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); isErr || d.Action == nil || d.Action.Status.Approval.DecidedBy != carol {
		t.Fatalf("carol's approve: %v %s", isErr, text)
	}
	if m, text, isErr := mergeCall(t, aliceC, a.Name); isErr || len(m.Merged) == 0 {
		t.Fatalf("merge after the approval: %v %s", isErr, text)
	}
	if noticed := st.gateway.noticed(); len(noticed) != 0 {
		t.Fatalf("a reviewed action told the standup channel: %+v", noticed)
	}
}

// A commit on a test installation that leaves the governance as it is still
// needs no review.
func TestTestInstallationWithoutGovernanceChangeNeedsNoReview(t *testing.T) {
	withGovernanceReleases(t)
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	rowanAsTestInstallation(t, st.ghs)
	rowanOnLineFour(st.ghs)
	seedRemote(t, st)
	aliceC := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keyGovernanceInput: map[string]any{keyMode: agentplatform.GovernanceTrusted}})})
	if isErr || out.Action == nil || out.Action.Status.Approval == nil || out.Action.Status.Approval.Decision != actions.DecisionNotRequired || len(out.Action.Spec.Governance) != 0 {
		t.Fatalf("commit: %v %s", isErr, text)
	}
}

// A wave that takes a test installation from governed back to trusted is
// the loosening the rule is for: it pends the team's review like any other
// governance change, the review naming what loosens, and the actor cannot
// approve it.
func TestWaveLooseningGovernanceOnATestInstallationNeedsASecondPerson(t *testing.T) {
	withGovernanceReleases(t)
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	rowanAsTestInstallation(t, st.ghs)
	rowanOnLineFour(st.ghs)
	st.ghs.addFile(acmeConfigs, installations.Capabilities()[0].EnabledMarker(rowan), "muster: {}\ngovernance: governed\n")
	seedRemote(t, st)
	aliceC := st.mcpClient(t, aliceToken)
	inputs := minimalInputs(map[string]any{keyGovernanceInput: map[string]any{keyMode: agentplatform.GovernanceTrusted}})
	text, isErr := call(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{rowan}, tools.ArgInputs: inputs, tools.ArgReason: commitReason, tools.ArgMode: string(tools.ModeCommit)})
	var w tools.WaveResult
	if isErr || json.Unmarshal([]byte(text), &w) != nil || w.Action == nil {
		t.Fatalf("wave commit: %v %s", isErr, text)
	}
	a := w.Action
	if a.Status.State != actions.StatePendingApproval || a.Status.Approval == nil || a.Status.Approval.ReviewID == "" || a.Status.Approval.Decision != "" {
		t.Fatalf("a loosening wave on a test installation went without the review: %s %+v", a.Status.State, a.Status.Approval)
	}
	if g := a.Spec.Governance[rowan]; len(g.Loosens) != 1 || g.Loosens[0] != "mode governed → trusted (the chart's default)" {
		t.Fatalf("the wave's governance: %+v", a.Spec.Governance)
	}
	if posted := st.gateway.posted(); len(posted) != 1 || !strings.Contains(fmt.Sprint(posted[0].Body["text"]), "*It loosens governance:* rowan: mode governed → trusted") {
		t.Fatalf("the review: %+v", posted)
	}
	if _, text, isErr := decide(t, aliceC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "yours") {
		t.Fatalf("the actor's approve: %v %s", isErr, text)
	}
}
