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
	"time"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
	"github.com/giantswarm/giantswarm-platform-manager/render/clustermcpservers"
)

// The governance input's keys.
const (
	keyGovernanceInput = "governance"
	keyMode            = "mode"
	keyIdleTTL         = "idleTTL"
	keySessions        = "sessions"
	keyGovernedServers = "governedServers"
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
	return minimalInputs(map[string]any{keyGovernanceInput: map[string]any{keyMode: agentplatform.GovernanceGoverned, keyGovernedServers: map[string]any{"mcp-kubernetes": nil}}})
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

// platformOnRecord is rowan's agent-platform values with Giant Swarm's own
// governance parts on record, as an earlier commit wrote them.
const platformOnRecord = "muster: {}\ngovernance:\n  roleFloors:\n    platformManager: [approve_action, merge_action]\ngovernedServers:\n  giantswarm-platform-manager/giantswarm-platform-manager:\n    role: platformManager\n    safe: [get_info, list_installations, list_actions, get_action, verify_capability]\n"

// seedRecord writes rowan's reviewed-governance record as the governance
// values carries, as a reviewed merge through the manager would.
func seedRecord(t *testing.T, st *stack, values string) {
	t.Helper()
	snap, err := plan.GovernanceSnapshotOf(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := actions.New(st.dyn, actionsNamespace).PutRecord(t.Context(), actions.GovernanceRecord{Installation: rowan, Governance: snap.JSON(), Action: "seeded", ApprovedBy: carol}, actions.RecordNone); err != nil {
		t.Fatal(err)
	}
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
	if !ok || len(g.Lines) != 5 || !strings.HasPrefix(g.Lines[0], "no reviewed governance is on record for rowan") || g.Lines[1] != "mode trusted → governed" || len(g.Loosens) != 2 || g.Loosens[1] != droppedKubernetes {
		t.Fatalf("the action's governance: %+v", a.Spec.Governance)
	}
	if after := a.Spec.GovernanceAfter[rowan]; !strings.Contains(after, `"mode":"governed"`) {
		t.Fatalf("the governance the action leaves: %q", after)
	}
	posted := st.gateway.posted()
	if len(posted) != 1 {
		t.Fatalf("%d review(s) posted", len(posted))
	}
	review := fmt.Sprint(posted[0].Body["text"])
	loosens := strings.Index(review, "*It loosens governance:* rowan: no reviewed governance is on record for rowan")
	if loosens < 0 || !strings.Contains(review, droppedKubernetes) || loosens > strings.Index(review, "• rowan: no reviewed governance") || !strings.Contains(review, "*Governance:* this changes the platform's governance") {
		t.Fatalf("the review leads with what loosens, then the change:\n%s", review)
	}
	requireLog(t, st, "governance_change_committed", "action="+a.Name, "actor=alice", "loosens=true")

	// The actor cannot approve it, nor merge it before a member did.
	if _, text, isErr := decide(t, aliceC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "yours") {
		t.Fatalf("the actor's approve: %v %s", isErr, text)
	}
	requireLog(t, st, "governance_change_self_approval_refused", "action="+a.Name)
	// S9-16: the actor under a new login is still the actor: GitHub's id
	// stays.
	const renamedToken = "alicia-token"
	st.ghs.mu.Lock()
	st.ghs.logins[renamedToken] = "alicia"
	st.ghs.ids["alicia"] = userID(alice)
	st.ghs.teams[governanceTeam]["alicia"] = memberActive
	st.ghs.mu.Unlock()
	if _, text, isErr := decide(t, st.mcpClient(t, renamedToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "yours") {
		t.Fatalf("the actor's approve under a new login: %v %s", isErr, text)
	}
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
	if after := d.Action.Spec.GovernanceAfter[rowan]; !strings.Contains(after, `"mode":"governed"`) {
		t.Fatalf("the governance the disable records: %q", after)
	}
	requireLog(t, st, "governance_change_committed", "action="+d.Action.Name)
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
	if !ok || !strings.Contains(values, "\n  mode: governed\n") {
		t.Fatalf("the governed values on record: %v\n%s", ok, values)
	}
	// Someone takes governed out in the repository, past the manager.
	st.ghs.addFile(acmeConfigs, marker, strings.Replace(values, "\n  mode: governed\n", "\n", 1))

	// Typing governed again changes no governance against the reviewed
	// action, yet the record moved: the review stands, the outside change
	// named first.
	again, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || again.Action == nil {
		t.Fatalf("reconcile: %v %s", isErr, text)
	}
	g := again.Action.Spec.Governance[rowan]
	if again.Action.Status.Approval.Decision == actions.DecisionNotRequired || len(g.Lines) == 0 || !strings.HasPrefix(g.Lines[0], "the governance on record differs from what the team reviewed in action "+merged.Name) {
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
	st.ghs.addFile(acmeConfigs, marker, platformOnRecord)
	seedRecord(t, st, platformOnRecord)
	moved := strings.Replace(platformOnRecord, "governance:\n", "governance:\n  mode: governed\n", 1)
	st.beforeOpen = func() { st.ghs.addFile(acmeConfigs, marker, moved) }
	out, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	if v, _ := st.ghs.file(acmeConfigs, marker); st.beforeOpen != nil || v != moved {
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
	st.ghs.addFile(acmeConfigs, installations.Capabilities()[0].EnabledMarker(rowan), platformOnRecord)
	seedRecord(t, st, platformOnRecord)
	aliceC := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keyGovernanceInput: map[string]any{keyMode: agentplatform.GovernanceTrusted}})})
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
	st.ghs.addFile(acmeConfigs, installations.Capabilities()[0].EnabledMarker(rowan), "muster: {}\ngovernance:\n  mode: governed\n")
	seedRecord(t, st, "muster: {}\ngovernance:\n  mode: governed\n")
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
	requireLog(t, st, "governance_change_committed", "action="+a.Name)
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

// governedThenTrustedOutside commits rowan governed, has it approved and
// merged, then takes governed out of the values in the repository directly.
func governedThenTrustedOutside(t *testing.T, st *stack) actions.Action {
	t.Helper()
	out, text, isErr := commitCall(t, st.mcpClient(t, aliceToken), tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	merged := approveAndMerge(t, st, out.Action.Name)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	values, _ := st.ghs.file(acmeConfigs, marker)
	st.ghs.addFile(acmeConfigs, marker, strings.Replace(values, "\n  mode: governed\n", "\n", 1))
	return merged
}

// D-GO-01: an outside edit is seen even when the next plan writes the
// values file as it is on record: the dry run and the commit name it
// against the last reviewed action, and it is logged; nothing is adopted.
func TestGovernanceChangedOutsideIsSeenWhenTheValuesStayAsTheyAre(t *testing.T) {
	st := governedTestRowan(t)
	merged := governedThenTrustedOutside(t, st)
	again, text, isErr := commitCall(t, st.mcpClient(t, aliceToken), tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatalf("reconcile: %s", text)
	}
	if again.Action != nil || !strings.Contains(again.Next, "is not what the team reviewed") || again.Plan.Governance == nil || !strings.HasPrefix(again.Plan.Governance.Lines[0], "the governance on record differs from what the team reviewed in action "+merged.Name) {
		t.Fatalf("an outside edit with the values unchanged: action %v, next %q, governance %+v", again.Action != nil, again.Next, again.Plan.Governance)
	}
	requireLog(t, st, "governance_on_record_changed_outside", "action="+merged.Name)
}

// D-GO-02: an action that recorded no governance snapshot is no baseline:
// the one before it stands, so after an outside edit a later commit still
// asks the review.
func TestAnActionWithoutASnapshotErasesNoBaseline(t *testing.T) {
	st := governedTestRowan(t)
	merged := governedThenTrustedOutside(t, st)
	// A newer merged action that needed no review and recorded no snapshot.
	store := actions.New(st.dyn, actionsNamespace)
	later := merged
	later.Name, later.ResourceVersion, later.CreatedAt = merged.Name+"-later", "", merged.CreatedAt.Add(time.Minute)
	later.Spec.Governance, later.Spec.GovernanceAfter = nil, nil
	stored, err := store.Create(t.Context(), later)
	if err != nil {
		t.Fatal(err)
	}
	status := merged.Status
	status.Approval = &actions.Approval{Decision: actions.DecisionNotRequired}
	if _, err := store.UpdateStatus(t.Context(), stored, status); err != nil {
		t.Fatal(err)
	}
	last, text, isErr := commitCall(t, st.mcpClient(t, aliceToken), tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keySessions: map[string]any{keyIdleTTL: "2h"}})})
	if isErr || last.Action == nil {
		t.Fatalf("reconcile: %v %s", isErr, text)
	}
	g := last.Action.Spec.Governance[rowan]
	if last.Action.Status.Approval.Decision == actions.DecisionNotRequired || len(g.Lines) == 0 || !strings.Contains(g.Lines[0], "what the team reviewed in action "+merged.Name) {
		t.Fatalf("a commit after a snapshot-less action: %+v %+v", last.Action.Status.Approval, g)
	}
}

// D-GO-11: an action merged without the team's decision is no baseline:
// what it left counts as changed outside.
func TestAnActionMergedWithoutApprovalIsNoBaseline(t *testing.T) {
	st := governedTestRowan(t)
	merged := governedThenTrustedOutside(t, st)
	store := actions.New(st.dyn, actionsNamespace)
	later := merged
	later.Name, later.ResourceVersion, later.CreatedAt = merged.Name+"-outside", "", merged.CreatedAt.Add(time.Minute)
	later.Spec.GovernanceAfter = map[string]string{rowan: `{"governance":{"roleFloors":{"platformManager":["approve_action","merge_action"]}}}`}
	stored, err := store.Create(t.Context(), later)
	if err != nil {
		t.Fatal(err)
	}
	status := merged.Status
	status.Approval = &actions.Approval{Decision: actions.DecisionMergedWithoutApproval}
	if _, err := store.UpdateStatus(t.Context(), stored, status); err != nil {
		t.Fatal(err)
	}
	again, text, isErr := commitCall(t, st.mcpClient(t, aliceToken), tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || again.Plan.Governance == nil || !strings.Contains(again.Plan.Governance.Lines[0], "what the team reviewed in action "+merged.Name) {
		t.Fatalf("an action merged without approval as the baseline: %v %s", isErr, text)
	}
}

// D-GO-07, D-GO-11: a governance merged between a wave's plan and its pull
// requests is a governance change of the action: the review shows it and
// only an active member of the reviewers' team approves it.
func TestGovernanceMovedUnderAWaveIsAGovernanceChange(t *testing.T) {
	st := governedTestRowan(t)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	st.ghs.addFile(acmeConfigs, marker, platformOnRecord)
	seedRecord(t, st, platformOnRecord)
	st.beforeOpen = func() {
		st.ghs.addFile(acmeConfigs, marker, strings.Replace(platformOnRecord, "governance:\n", "governance:\n  mode: governed\n", 1))
	}
	aliceC := st.mcpClient(t, aliceToken)
	text, isErr := call(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{rowan}, tools.ArgInputs: minimalInputs(nil), tools.ArgReason: commitReason, tools.ArgMode: string(tools.ModeCommit)})
	var w tools.WaveResult
	if isErr || json.Unmarshal([]byte(text), &w) != nil || w.Action == nil {
		t.Fatalf("wave commit: %v %s", isErr, text)
	}
	a := w.Action
	g := a.Status.Governance[rowan]
	if a.Status.Approval == nil || a.Status.Approval.ReviewID == "" || len(g.Loosens) != 1 || !strings.Contains(g.Loosens[0], "the governance on the default branch changed after the plan read it") {
		t.Fatalf("a wave whose record moved: %+v %+v", a.Status.Approval, a.Status.Governance)
	}
	if posted := st.gateway.posted(); len(posted) != 1 || !strings.Contains(fmt.Sprint(posted[0].Body["text"]), "*It loosens governance:* rowan: the governance on the default branch changed") {
		t.Fatalf("the review: %+v", posted)
	}
	requireLog(t, st, "governance_record_moved", "installation=rowan")
	st.ghs.mu.Lock()
	st.ghs.teams[governanceTeam][carol] = "pending"
	st.ghs.mu.Unlock()
	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); !isErr || !strings.Contains(text, "pending") {
		t.Fatalf("an approve by a pending member: %v %s", isErr, text)
	}
}

// record is rowan's reviewed-governance record, nil without one.
func record(t *testing.T, st *stack) *actions.GovernanceRecord {
	t.Helper()
	r, err := actions.New(st.dyn, actionsNamespace).GetRecord(t.Context(), rowan)
	if err != nil {
		return nil
	}
	return r
}

// Decision #99: the reviewed-governance record is written by merge_action
// when an approved governance change is merged through the manager, with
// the approver and the configs pull request's head; an action that needs
// no review writes none.
func TestTheReviewedGovernanceIsRecordedAtTheMerge(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	if record(t, st) != nil {
		t.Fatal("a record before the merge")
	}
	merged := approveAndMerge(t, st, out.Action.Name)
	r := record(t, st)
	var head string
	for _, pr := range merged.Status.PullRequests {
		if pr.Repository == acmeConfigs {
			head = pr.HeadSHA
		}
	}
	if r == nil || r.Action != merged.Name || r.ApprovedBy != carol || r.HeadSHA == "" || r.HeadSHA != head || r.Governance != merged.Spec.GovernanceAfter[rowan] {
		t.Fatalf("the record after the reviewed merge: %+v (head %s)", r, head)
	}
	requireLog(t, st, "governance_record_written", "action="+merged.Name)

	// A later change that needs no review leaves the record as it is.
	next, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keyGovernanceInput: map[string]any{keyMode: agentplatform.GovernanceGoverned, keyGovernedServers: map[string]any{"mcp-kubernetes": nil}}, keySessions: map[string]any{keyIdleTTL: "2h"}})})
	if isErr || next.Action == nil || next.Action.Status.Approval.Decision != actions.DecisionNotRequired {
		t.Fatalf("a change without governance: %v %s", isErr, text)
	}
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	if _, text, isErr := mergeCall(t, aliceC, next.Action.Name); isErr {
		t.Fatalf("merge: %s", text)
	}
	if again := record(t, st); again == nil || again.Action != merged.Name {
		t.Fatalf("a merge needing no review rewrote the record: %+v", again)
	}
}

// D-GO-25: an action planned while the installation was trusted, merged
// after the team made it governed, is no baseline: the record is what the
// team reviewed, so a direct push back to trusted is seen and asks the
// review.
func TestAStaleActionMergedLateDoesNotDisplaceTheReviewedGovernance(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	first, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || first.Action == nil {
		t.Fatalf("first: %v %s", isErr, text)
	}
	approveAndMerge(t, st, first.Action.Name)
	stale, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keySessions: map[string]any{keyIdleTTL: "2h"}})})
	if isErr || stale.Action == nil || stale.Action.Status.Approval.Decision != actions.DecisionNotRequired {
		t.Fatalf("stale: %v %s", isErr, text)
	}
	gov, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || gov.Action == nil {
		t.Fatalf("governed: %v %s", isErr, text)
	}
	reviewed := approveAndMerge(t, st, gov.Action.Name)
	governed, _ := st.ghs.file(acmeConfigs, marker)
	if _, text, isErr := mergeCall(t, aliceC, stale.Action.Name); isErr {
		t.Fatalf("merge stale: %s", text)
	}
	afterStale, _ := st.ghs.file(acmeConfigs, marker)
	// git's three-way merge keeps the governed lines beside the stale change;
	// then someone pushes the stale action's values directly.
	st.ghs.addFile(acmeConfigs, marker, governed)
	if r := record(t, st); r == nil || r.Action != reviewed.Name {
		t.Fatalf("the stale merge displaced the record: %+v", r)
	}
	st.ghs.addFile(acmeConfigs, marker, afterStale)
	again, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keySessions: map[string]any{keyIdleTTL: "3h"}})})
	if isErr || again.Action == nil {
		t.Fatalf("again: %v %s", isErr, text)
	}
	g := again.Action.Spec.Governance[rowan]
	if again.Action.Status.Approval.Decision == actions.DecisionNotRequired || len(g.Lines) == 0 || !strings.Contains(g.Lines[0], "what the team reviewed in action "+reviewed.Name) || !slices.Contains(g.Loosens, "mode governed → trusted") {
		t.Fatalf("a push back to trusted after a stale merge: %+v %+v", again.Action.Status.Approval, g)
	}
	requireLog(t, st, "governance_on_record_changed_outside", "action="+reviewed.Name)
}

// D-GO-26: the reviewed governance change reverted by hand and read by a
// watch keeps the record; the revert is audited and the next commit shows
// it against what the team reviewed and asks the review.
func TestAHandRevertOfAReviewedGovernanceChangeAsksTheReview(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	first, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || first.Action == nil {
		t.Fatalf("first: %v %s", isErr, text)
	}
	approveAndMerge(t, st, first.Action.Name)
	gov, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || gov.Action == nil {
		t.Fatalf("governed: %v %s", isErr, text)
	}
	reviewed := approveAndMerge(t, st, gov.Action.Name)
	for _, pr := range st.remote.PullRequests() {
		for _, mine := range reviewed.Status.PullRequests {
			if pr.Merged && pr.URL == mine.URL {
				st.revertOutside(t, pr.PullRequest, dave)
			}
		}
	}
	if v, _ := st.ghs.file(acmeConfigs, marker); strings.Contains(v, "mode: governed") {
		t.Fatal("setup: still governed after the revert")
	}
	if w, text, isErr := watchCall(t, adminLive(t, st), reviewed.Name); isErr || w.State != actions.StateReverted {
		t.Fatalf("watch: %v %s %s", isErr, w.State, text)
	}
	requireLog(t, st, "governance_change_reverted", "action="+reviewed.Name)
	if r := record(t, st); r == nil || r.Action != reviewed.Name {
		t.Fatalf("the revert changed the record: %+v", r)
	}
	again, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{keySessions: map[string]any{keyIdleTTL: "3h"}})})
	if isErr || again.Action == nil {
		t.Fatalf("again: %v %s", isErr, text)
	}
	g := again.Action.Spec.Governance[rowan]
	if again.Action.Status.Approval.Decision == actions.DecisionNotRequired || len(g.Lines) == 0 || !strings.Contains(g.Lines[0], "what the team reviewed in action "+reviewed.Name) {
		t.Fatalf("a commit after a hand revert: %+v %+v", again.Action.Status.Approval, g)
	}
}

// D-GO-33: an approved governance action parked while the team reviewed and
// merged another one is not merged: its plan compared with a record that is
// no longer the installation's, so its approval does not cover it. The
// record stays the newer review's.
func TestAStaleApprovedGovernanceActionIsNotMergedOverANewerRecord(t *testing.T) {
	st := governedTestRowan(t)
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)
	first, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || first.Action == nil {
		t.Fatalf("first: %v %s", isErr, text)
	}
	approveAndMerge(t, st, first.Action.Name)
	safe := map[string]any{keyGovernanceInput: map[string]any{keyGovernedServers: map[string]any{"skill-manager": map[string]any{"safe": []any{"list_skills"}}}}}
	parked, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(safe)})
	if isErr || parked.Action == nil || len(parked.Action.Spec.Governance) == 0 {
		t.Fatalf("parked: %v %s", isErr, text)
	}
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: parked.Action.Name}); isErr {
		t.Fatalf("approve parked: %s", text)
	}
	gov, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || gov.Action == nil {
		t.Fatalf("governed: %v %s", isErr, text)
	}
	newer := approveAndMerge(t, st, gov.Action.Name)
	if _, text, isErr := mergeCall(t, aliceC, parked.Action.Name); !isErr || !strings.Contains(text, "changed since action "+parked.Action.Name+" was planned") {
		t.Fatalf("the parked action's merge: %v %s", isErr, text)
	}
	requireLog(t, st, "governance_record_moved_since_plan", "action="+parked.Action.Name)
	if r := record(t, st); r == nil || r.Action != newer.Name {
		t.Fatalf("the record after the refused merge: %+v", r)
	}
}

// D-GO-36: after a reviewed disable the record holds the governance from
// before it; the enable that brings the platform back is compared with it
// under its own line, and nothing is logged as changed outside.
func TestAnEnableAfterAReviewedDisableIsNoOutsideChange(t *testing.T) {
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
	disabled := approveAndMerge(t, st, d.Action.Name)
	if r := record(t, st); r == nil || !r.Disabled || r.Action != disabled.Name {
		t.Fatalf("the record after the reviewed disable: %+v", r)
	}
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(rowan), collectionsKustomization(platformDexApp))
	logs := len(st.logs.String())
	again, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || again.Action == nil {
		t.Fatalf("enable again: %v %s", isErr, text)
	}
	g := again.Action.Spec.Governance[rowan]
	if len(g.Lines) != 1 || !strings.HasPrefix(g.Lines[0], "the platform comes back after the reviewed disable "+disabled.Name) || again.Action.Status.Approval.Decision == actions.DecisionNotRequired {
		t.Fatalf("the enable after the disable: %+v %+v", again.Action.Status.Approval, g)
	}
	if strings.Contains(st.logs.String()[logs:], "governance_on_record_changed_outside") {
		t.Fatal("the enable after a reviewed disable logged an outside change")
	}
}

// D-GO-40: a disable of values that differ from the record shows the
// outside change and deletes the governance the team reviewed, not the
// values on record.
func TestADisableAfterAnOutsideEditIsComparedWithTheRecord(t *testing.T) {
	st := governedTestRowan(t)
	merged := governedThenTrustedOutside(t, st)
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(rowan), collectionsKustomization(clustermcpservers.DexAppRotation))
	st.ghs.addRepo(fleetBasesRepo, platformBaseFiles)
	d, text, isErr := disableCall(t, st.mcpClient(t, aliceToken), map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgMode: string(tools.ModeCommit), tools.ArgReason: commitReason})
	if isErr || d.Action == nil {
		t.Fatalf("disable: %v %s", isErr, text)
	}
	g := d.Action.Spec.Governance[rowan]
	if len(g.Lines) == 0 || !strings.Contains(g.Lines[0], "what the team reviewed in action "+merged.Name) || !slices.Contains(g.Loosens, "mode governed → trusted") || !strings.Contains(d.Action.Spec.GovernanceAfter[rowan], `"mode":"governed"`) {
		t.Fatalf("the disable after an outside edit: %+v after %q", g, d.Action.Spec.GovernanceAfter[rowan])
	}
	// What the disable deletes is the reviewed governance: governed goes
	// with it, after the line that says the values are deleted.
	deleted := slices.IndexFunc(g.Lines, func(l string) bool { return strings.Contains(l, "deleted with the platform") })
	if deleted < 0 || !slices.Contains(g.Lines[deleted:], "mode governed → trusted") {
		t.Fatalf("the disable deletes the values on record, not the reviewed governance: %+v", g.Lines)
	}
}

// D-GO-40: the record is written when the stage is merged, not while the
// merge waits on a check.
func TestTheRecordWaitsForTheWholeStage(t *testing.T) {
	st := governedTestRowan(t)
	aliceC := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: out.Action.Name}); isErr {
		t.Fatalf("approve: %s", text)
	}
	prs := st.remote.PullRequests()
	st.remote.SetChecks(prs[0].PullRequest, commit.ChecksSuccess)
	st.remote.SetChecks(prs[1].PullRequest, commit.ChecksPending)
	if m, text, isErr := mergeCall(t, aliceC, out.Action.Name); isErr || m.Waiting == "" {
		t.Fatalf("merge with a pending check: %v %s", isErr, text)
	}
	if r := record(t, st); r != nil {
		t.Fatalf("a record while the stage waits: %+v", r)
	}
	st.remote.SetChecks(prs[1].PullRequest, commit.ChecksSuccess)
	if _, text, isErr := mergeCall(t, aliceC, out.Action.Name); isErr {
		t.Fatalf("merge: %s", text)
	}
	if r := record(t, st); r == nil || r.Action != out.Action.Name {
		t.Fatalf("no record after the stage: %+v", r)
	}
}

// D-GO-40: a reviewed governance change taken back by hand is audited
// when its actor withdraws it, and when a read finds its fileset gone.
func TestTheRevertOfAGovernanceChangeIsAuditedOnEveryPath(t *testing.T) {
	t.Run("withdrawn", func(t *testing.T) {
		st := governedTestRowan(t)
		aliceC := st.mcpClient(t, aliceToken)
		first, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
		if isErr || first.Action == nil {
			t.Fatalf("first: %v %s", isErr, text)
		}
		approveAndMerge(t, st, first.Action.Name)
		gov, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
		if isErr || gov.Action == nil {
			t.Fatalf("governed: %v %s", isErr, text)
		}
		reviewed := approveAndMerge(t, st, gov.Action.Name)
		for _, pr := range st.remote.PullRequests() {
			for _, mine := range reviewed.Status.PullRequests {
				if pr.Merged && pr.URL == mine.URL {
					st.revertOutside(t, pr.PullRequest, dave)
				}
			}
		}
		if _, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: reviewed.Name, tools.ArgReason: "taken back"}); isErr {
			t.Fatalf("withdraw: %s", text)
		}
		requireLog(t, st, "governance_change_reverted", "action="+reviewed.Name)
	})
	t.Run("fileset gone", func(t *testing.T) {
		st := governedTestRowan(t)
		aliceC := st.mcpClient(t, aliceToken)
		out, text, isErr := commitCall(t, aliceC, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: governedInputs()})
		if isErr || out.Action == nil {
			t.Fatalf("commit: %v %s", isErr, text)
		}
		merged := approveAndMerge(t, st, out.Action.Name)
		for _, pr := range st.remote.PullRequests() {
			if pr.Merged {
				st.revertOutside(t, pr.PullRequest, dave)
			}
		}
		if got := getAction(t, aliceC, merged.Name); got.Status.State != actions.StateRemoved {
			t.Fatalf("the action after its fileset left: %s", got.Status.State)
		}
		requireLog(t, st, "governance_change_reverted", "action="+merged.Name)
	})
}
