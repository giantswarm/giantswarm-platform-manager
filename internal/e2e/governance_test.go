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
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
	"github.com/giantswarm/giantswarm-platform-manager/render/clustermcpservers"
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

// governedTestRowan is the stack with rowan one of the test installations,
// on the 4 line, with the governance releases cut.
func governedTestRowan(t *testing.T) *stack {
	t.Helper()
	withGovernanceReleases(t)
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	rowanAsTestInstallation(t, st.ghs)
	rowanOnLineFour(st.ghs)
	seedRemote(t, st)
	return st
}

// governedInputs make rowan governed and drop mcp-kubernetes from the
// governed servers.
func governedInputs() map[string]any {
	return minimalInputs(map[string]any{keyGovernanceInput: map[string]any{keyMode: agentplatform.GovernanceGoverned, "governedServers": map[string]any{"mcp-kubernetes": nil}}})
}

// approveAndMerge has carol approve the action and alice merge it once green.
func approveAndMerge(t *testing.T, st *stack, name string) actions.Action {
	t.Helper()
	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: name}); isErr {
		t.Fatalf("carol's approve: %s", text)
	}
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	m, text, isErr := mergeCall(t, st.mcpClient(t, aliceToken), name)
	if isErr || len(m.Merged) == 0 || m.Action == nil {
		t.Fatalf("merge: %v %s", isErr, text)
	}
	return *m.Action
}

// requireLog fails unless the server logged a line carrying every part.
func requireLog(t *testing.T, st *stack, parts ...string) {
	t.Helper()
	for _, line := range strings.Split(st.logs.String(), "\n") {
		if !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(line, p) }) {
			return
		}
	}
	t.Fatalf("no log line carries %q:\n%s", parts, st.logs.String())
}

const droppedKubernetes = "governedServers.mcp-kubernetes {role: infrastructure, 14 safe} → not governed (null)"

func TestGovernanceChangeOnATestInstallationNeedsASecondPerson(t *testing.T) {
	st := governedTestRowan(t)
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)

	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	a := out.Action
	if a.Status.State != actions.StatePendingApproval || a.Status.Approval == nil || a.Status.Approval.Decision != "" || a.Status.Approval.ReviewID == "" || strings.Contains(out.Next, "no Team review") {
		t.Fatalf("a governance change on a test installation went without the review: %+v; next %q", a.Status.Approval, out.Next)
	}
	g, ok := a.Spec.Governance[rowan]
	if !ok || len(g.Lines) != 2 || len(g.Loosens) != 1 || g.Loosens[0] != droppedKubernetes {
		t.Fatalf("the action's governance: %+v", a.Spec.Governance)
	}
	if after := a.Spec.GovernanceAfter[rowan]; !strings.Contains(after, `"governance":"governed"`) {
		t.Fatalf("the governance the action leaves: %q", after)
	}
	posted := st.gateway.posted()
	if len(posted) != 1 {
		t.Fatalf("%d review(s) posted", len(posted))
	}
	review := fmt.Sprint(posted[0].Body["text"])
	loosens := strings.Index(review, "*It loosens governance:* rowan: "+droppedKubernetes)
	if loosens < 0 || loosens > strings.Index(review, "• rowan: mode trusted → governed") || !strings.Contains(review, "*Governance:* this changes the platform's governance") {
		t.Fatalf("the review leads with what loosens, then the change:\n%s", review)
	}
	requireLog(t, st, "governance_change_committed", "action="+a.Name, "actor=alice", "loosens=true")

	// The actor cannot approve it, nor merge it before a member did.
	if _, text, isErr := decide(t, aliceC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "yours") {
		t.Fatalf("the actor's approve: %v %s", isErr, text)
	}
	requireLog(t, st, "governance_change_self_approval_refused", "action="+a.Name)
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	if m, text, isErr := mergeCall(t, aliceC, a.Name); isErr || len(m.Merged) != 0 || !strings.Contains(m.Message, "waits for the team's approval") {
		t.Fatalf("merge before the approval: %v %s", isErr, text)
	}

	// S9-05: the second person is an active member of the governance
	// reviewers' team, read as them; unreadable or not active, nothing is
	// approved.
	st.ghs.mu.Lock()
	st.ghs.teamsFailing = true
	st.ghs.mu.Unlock()
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "your membership could not be read") {
		t.Fatalf("an approve while the membership cannot be read: %v %s", isErr, text)
	}
	st.ghs.mu.Lock()
	st.ghs.teamsFailing = false
	st.ghs.teams[governanceTeam][carol] = "pending"
	st.ghs.mu.Unlock()
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, `carol is "pending" there`) {
		t.Fatalf("an approve by a pending member: %v %s", isErr, text)
	}
	st.ghs.mu.Lock()
	st.ghs.teams[governanceTeam][carol] = memberActive
	st.ghs.userTypes[carol] = "Bot"
	st.ghs.mu.Unlock()
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "carol is a GitHub Bot") {
		t.Fatalf("an approve by a bot: %v %s", isErr, text)
	}
	requireLog(t, st, "governance_change_approval_refused", "approver=carol")
	st.ghs.mu.Lock()
	delete(st.ghs.userTypes, carol)
	st.ghs.mu.Unlock()
	if d, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); isErr || d.Action == nil || d.Action.Status.Approval.DecidedBy != carol {
		t.Fatalf("carol's approve: %v %s", isErr, text)
	}
	requireLog(t, st, "governance_change_approved", "approver=carol", "membership=\"active member of "+governanceTeam)
	m, text, isErr := mergeCall(t, aliceC, a.Name)
	if isErr || len(m.Merged) == 0 {
		t.Fatalf("merge after the approval: %v %s", isErr, text)
	}
	requireLog(t, st, "governance_change_merged", "installation=rowan", "pullRequests="+m.Merged[0].Repository)
	if noticed := st.gateway.noticed(); len(noticed) != 0 {
		t.Fatalf("a reviewed action told the standup channel: %+v", noticed)
	}
}

// S9-01: a disable that deletes a governed installation's values is a
// governance change a second person approves, on a test installation too,
// and what it records as left is the governance before it: an enable after
// it that brings the platform back trusted is a loosening change again.
func TestDisableThenEnableOfAGovernedTestInstallationNeedsASecondPerson(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	approveAndMerge(t, st, out.Action.Name)

	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(rowan), collectionsKustomization(clustermcpservers.DexAppRotation))
	st.ghs.addRepo(fleetBasesRepo, platformBaseFiles)
	d, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgMode: string(tools.ModeCommit), tools.ArgReason: commitReason})
	if isErr || d.Action == nil {
		t.Fatalf("disable: %v %s", isErr, text)
	}
	g := d.Action.Spec.Governance[rowan]
	if d.Action.Status.State != actions.StatePendingApproval || d.Action.Status.Approval.ReviewID == "" || len(g.Lines) == 0 || !strings.Contains(g.Lines[0], "deleted with the platform") || !slices.Contains(g.Loosens, "mode governed → trusted") {
		t.Fatalf("the disable of a governed test installation: %+v %+v", d.Action.Status.Approval, g)
	}
	if after := d.Action.Spec.GovernanceAfter[rowan]; !strings.Contains(after, `"governance":"governed"`) {
		t.Fatalf("the governance the disable records: %q", after)
	}
	approveAndMerge(t, st, d.Action.Name)
	if got := getAction(t, aliceC, d.Action.Name); got.Status.State != actions.StateDisabled {
		t.Fatalf("the disable after its merge: %s", got.Status.State)
	}

	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(rowan), collectionsKustomization(platformDexApp))
	again, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || again.Action == nil {
		t.Fatalf("enable again: %v %s", isErr, text)
	}
	g = again.Action.Spec.Governance[rowan]
	if again.Action.Status.Approval == nil || again.Action.Status.Approval.Decision == actions.DecisionNotRequired || !slices.Contains(g.Loosens, "mode governed → trusted") {
		t.Fatalf("an enable that brings the platform back trusted went without the review: %+v %+v", again.Action.Status.Approval, g)
	}
}

// S9-06: a governance changed in the configs repository directly is not
// adopted unseen: the next commit compares against what the last reviewed
// action left, logs the record changed outside and asks the review, even
// where the commit types the governance back.
func TestGovernanceChangedOutsideTheManagerNeedsTheReview(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	merged := approveAndMerge(t, st, out.Action.Name)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	values, ok := st.ghs.file(acmeConfigs, marker)
	if !ok || !strings.Contains(values, "\ngovernance: governed\n") {
		t.Fatalf("the governed values on record: %v\n%s", ok, values)
	}
	// Someone takes governed out in the repository, past the manager.
	st.ghs.addFile(acmeConfigs, marker, strings.Replace(values, "\ngovernance: governed\n", "\n", 1))

	// Typing governed again changes no governance against the reviewed
	// action, yet the record moved: the review stands, the outside change
	// named first.
	again, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || again.Action == nil {
		t.Fatalf("reconcile: %v %s", isErr, text)
	}
	g := again.Action.Spec.Governance[rowan]
	if again.Action.Status.Approval.Decision == actions.DecisionNotRequired || len(g.Lines) == 0 || !strings.HasPrefix(g.Lines[0], "the governance on record differs from what action "+merged.Name+" left") {
		t.Fatalf("a reconcile over an outside change went without the review: %+v %+v", again.Action.Status.Approval, g)
	}
	requireLog(t, st, "governance_on_record_changed_outside", "installation=rowan", "action="+merged.Name)
}

// S9-08: a governance merged between the plan and the pull requests is
// written over by them unseen; the commit reads the record again and asks
// the review.
func TestGovernanceMergedBetweenThePlanAndTheBranchNeedsTheReview(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	st.beforeOpen = func() { st.ghs.addFile(acmeConfigs, marker, "governance: governed\n") }
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	if v, _ := st.ghs.file(acmeConfigs, marker); st.beforeOpen != nil || v != "governance: governed\n" {
		t.Fatalf("the hook did not run: %q", v)
	}
	if out.Action.Status.Approval == nil || out.Action.Status.Approval.Decision == actions.DecisionNotRequired || out.Action.Status.Approval.ReviewID == "" {
		t.Fatalf("a commit whose record moved went without the review: %+v\n%s", out.Action.Status.Approval, st.logs.String())
	}
	requireLog(t, st, "governance_record_moved", "installation=rowan")
}

// A commit on a test installation that leaves the governance as it is still
// needs no review.
func TestTestInstallationWithoutGovernanceChangeNeedsNoReview(t *testing.T) {
	st := governedTestRowan(t)
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
	if g := a.Spec.Governance[rowan]; len(g.Loosens) != 1 || g.Loosens[0] != "mode governed → trusted" {
		t.Fatalf("the wave's governance: %+v", a.Spec.Governance)
	}
	if posted := st.gateway.posted(); len(posted) != 1 || !strings.Contains(fmt.Sprint(posted[0].Body["text"]), "*It loosens governance:* rowan: mode governed → trusted") {
		t.Fatalf("the review: %+v", posted)
	}
	if _, text, isErr := decide(t, aliceC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "yours") {
		t.Fatalf("the actor's approve: %v %s", isErr, text)
	}
}

// S9-07: a pull request of a governance change merged outside the manager
// before the team decided is audited at the next read of the action, even
// when the rest of the stage is still open; it decides nothing.
func TestGovernanceMergedOutsideTheManagerIsAudited(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	var configs commit.PullRequest
	for _, pr := range st.remote.PullRequests() {
		if pr.Repository.String() == acmeConfigs {
			configs = pr.PullRequest
		}
	}
	if err := st.merge(t.Context(), st.remote, configs, alice); err != nil {
		t.Fatal(err)
	}
	got := getAction(t, aliceC, out.Action.Name)
	if got.Status.Approval != nil && got.Status.Approval.Decision != "" {
		t.Fatalf("a merge outside the manager decided the governance change: %+v", got.Status.Approval)
	}
	requireLog(t, st, "governance_change_merged_without_approval", "action="+out.Action.Name, "pullRequests="+acmeConfigs)
}
