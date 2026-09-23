package e2e

// The wave: reconcile_capability in mode commit over a set of three
// lab-shaped installations, one without the capability on record. The dry run and the
// review list two targets in the wave's order and one skipped; one Action
// carries every installation's state; the pull requests are merged one
// installation after the other, each carried to enabled by the watch before
// the next is merged; a red probe on the installation rolling out stops the
// wave with the next one's pull requests open, and the summary names the stop.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

func waveArgs(extra map[string]any) map[string]any {
	args := map[string]any{tools.ArgInstallations: []string{birch, rowan, alder}, tools.ArgInputs: minimalInputs(nil)}
	for k, v := range extra {
		args[k] = v
	}
	return args
}

// waveStage1 runs the wave over birch, rowan (alder skipped) up to the first
// stage merged: the dry run and its order, the commit, carol's approval,
// alice's merge of birch's pull requests — and birch's installation behind
// the fake muster, built from the action's inputs on record.
func waveStage1(t *testing.T, st *stack) (actions.Action, *client.Client, *fakeInstallation) {
	t.Helper()
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	// rowan's platform values on record, put there by hand: a wave reconciles what is on record.
	st.ghs.addFile(acmeConfigs, installations.Capabilities()[0].EnabledMarker(rowan), "muster: {}\n")
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)

	// The dry run: two targets in the wave's order, one skipped.
	text, isErr := call(t, aliceC, tools.ToolReconcileCapability, waveArgs(map[string]any{tools.ArgDryRun: true, tools.ArgContent: true}))
	var dry tools.CapabilityResult
	if isErr || json.Unmarshal([]byte(text), &dry) != nil || strings.Join(dry.Order, ",") != birch+","+rowan || len(dry.Skipped) != 1 || dry.Skipped[0].Name != alder || dry.Skipped[0].Reason != tools.SkippedNotEnabled {
		t.Fatalf("dry run: %v %s", isErr, text)
	}
	text, isErr = call(t, aliceC, tools.ToolReconcileCapability, waveArgs(map[string]any{tools.ArgDryRun: true, tools.ArgOrder: []string{rowan, birch}}))
	var reordered tools.CapabilityResult
	if isErr || json.Unmarshal([]byte(text), &reordered) != nil || strings.Join(reordered.Order, ",") != rowan+","+birch || reordered.Installations[0].Name != rowan {
		t.Fatalf("reordered dry run: %v %.300s", isErr, text)
	}
	if _, isErr := call(t, aliceC, tools.ToolReconcileCapability, waveArgs(map[string]any{tools.ArgDryRun: true, tools.ArgOrder: []string{rowan}})); !isErr {
		t.Fatal("an order leaving a target out was accepted")
	}

	// A wave carries no supplied values: a target whose supplied secret files
	// are not on record would refuse the wave whole; a customer's installations
	// ask for none.
	for _, p := range dry.Installations {
		for _, f := range p.Files {
			if isSecretFile(f.Path) && strings.Contains(f.Content, "SUPPLIED(") {
				st.ghs.addFile(f.Repository, f.Path, "sops: on record\n")
			}
		}
	}
	seedRemote(t, st)

	// The commit: one Action for the wave, the pull requests per installation
	// in the order, the review listing the targets and the skipped.
	text, isErr = call(t, aliceC, tools.ToolReconcileCapability, waveArgs(map[string]any{tools.ArgMode: string(tools.ModeCommit)}))
	var w tools.WaveResult
	if isErr || json.Unmarshal([]byte(text), &w) != nil || w.Action == nil {
		t.Fatalf("wave commit: %v %s", isErr, text)
	}
	assertNoValue(t, "the wave's answer", text)
	a := w.Action
	if strings.Join(a.Spec.Installations, ",") != birch+","+rowan || len(a.Spec.Skipped) != 1 || a.Spec.Skipped[0].Name != alder || a.Spec.Kind != actions.KindReconcile || !a.Spec.Customer || a.Status.State != actions.StatePendingApproval {
		t.Fatalf("the wave's action: %+v", a.Spec)
	}
	if r := a.Status.Rollout; r == nil || len(r.Installations) != 2 || r.Installations[0].Name != birch || r.Installations[0].State != actions.StatePendingApproval || r.Installations[1].Name != rowan {
		t.Fatalf("the wave's stages: %+v", a.Status.Rollout)
	}
	if len(w.PullRequests) < 3 || w.PullRequests[0].Installation != birch || w.PullRequests[len(w.PullRequests)-1].Installation != rowan {
		t.Fatalf("the wave's pull requests: %+v", w.PullRequests)
	}
	for _, pr := range w.PullRequests {
		if !strings.HasPrefix(pr.Head, "platform/"+a.Name+"/"+pr.Installation) {
			t.Fatalf("pull request branch: %+v", pr)
		}
	}
	if review := fmt.Sprint(st.gateway.posted()); len(st.gateway.posted()) != 1 || !strings.Contains(review, birch+", "+rowan) || !strings.Contains(review, alder+" ("+tools.SkippedNotEnabled+")") {
		t.Fatalf("the review: %s", review)
	}

	// Approved by a second person, every check green: stage 1 merges birch's
	// pull requests and only those; both installations show rolling out.
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: a.Name}); isErr {
		t.Fatalf("approve: %s", text)
	}
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	m, text, isErr := mergeCall(t, aliceC, a.Name)
	if isErr || m.Stage != birch || len(m.Merged) == 0 || m.Action.Status.State != actions.StateRollingOut || !strings.Contains(m.Message, rowan) || !strings.Contains(m.Message, tools.ToolWatchAction) {
		t.Fatalf("stage 1: %v %s", isErr, text)
	}
	for _, pr := range m.Merged {
		if pr.Installation != birch {
			t.Fatalf("stage 1 merged %+v", pr)
		}
	}
	if r := m.Action.Status.Rollout; r.StartedAt == nil || r.Installations[0].State != actions.StateRollingOut || r.Installations[1].State != actions.StateRollingOut || !strings.Contains(r.Installations[1].Message, "queued") {
		t.Fatalf("stages after stage 1: %+v", r.Installations)
	}
	li, text, isErr := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []string{birch, rowan}})
	if isErr || find(t, li, birch).Capabilities[0].State != installations.StateRollingOut || find(t, li, rowan).Capabilities[0].State != installations.StateRollingOut {
		t.Fatalf("list_installations during the wave: %v %s", isErr, text)
	}
	// Another merge before the watch: refused, the watch decides.
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, birch+" is "+actions.StateRollingOut) || !strings.Contains(text, tools.ToolWatchAction) || !strings.Contains(text, rowan) {
		t.Fatalf("merge before the watch: %v %s", isErr, text)
	}
	birchInst := newFakeInstallation()
	st.muster.serve(birch, birchInst)
	populateStage(t, birchInst, *m.Action, birch)
	return *m.Action, aliceC, birchInst
}

// birch's probe answers wrong: the watch that gates stage 2 is red, the wave
// stops before rowan with rowan's pull requests open, the summary and the
// thread name where and why; nothing merges or watches a stopped wave.
func TestWaveOverASetStopsAtARedProbe(t *testing.T) {
	st := newStack(t)
	a, aliceC, _ := waveStage1(t, st)
	text, isErr := call(t, aliceC, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: birch})
	var v verify.Result
	if isErr || json.Unmarshal([]byte(text), &v) != nil {
		t.Fatalf("verify birch: %v %s", isErr, text)
	}
	u, err := url.Parse(dimension(t, feature(t, v, "tool-access"), edgeProbe).Probe.Requests[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	st.probes.answer(u.Host+u.Path, http.StatusNotFound)

	w, text, isErr := watchCall(t, adminLive(t, st), a.Name)
	if isErr || w.Installation != birch || !w.Ready || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed || w.Action.Status.Result == nil ||
		!strings.Contains(w.Action.Status.Result.Message, "stopped at "+birch+" (stage 1 of 2)") || !strings.Contains(w.Action.Status.Result.Message, edgeProbe) || !strings.Contains(w.Action.Status.Result.Message, "stay open") {
		t.Fatalf("the stop: %v %s", isErr, text)
	}
	if r := w.Action.Status.Rollout; r.FinishedAt == nil || r.Installations[0].State != actions.StateFailed || r.Installations[1].State != "" || !strings.Contains(r.Installations[1].Message, "stopped at "+birch) {
		t.Fatalf("stages after the stop: %+v", r.Installations)
	}
	for _, pr := range w.Action.Status.PullRequests {
		if (pr.Installation == rowan) != (pr.State == actions.PullRequestOpen) {
			t.Fatalf("after the stop: %+v", pr)
		}
	}
	for _, pr := range st.remote.PullRequests() {
		if strings.HasPrefix(pr.Head, "platform/"+a.Name+"/"+rowan) && (pr.Merged || pr.Closed) {
			t.Fatalf("rowan's pull request touched: %+v", pr)
		}
	}
	if p, ok := probeOnRecord(w.Action, birch, edgeProbe); !ok || p.Result != string(verify.Drifted) {
		t.Fatalf("probes on record: %+v", w.Action.Status.Probes)
	}
	if th := thread(t, st); len(th) != 2 || !strings.Contains(th[1], "*"+birch+"* is *"+actions.StateFailed+"* (stage 1 of 2)") || !strings.Contains(th[1], "stay open") || !strings.Contains(th[1], "❌ "+edgeProbe) {
		t.Fatalf("the thread: %q", th)
	}
	li, text, isErr := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []string{birch, rowan}})
	if isErr || find(t, li, birch).Capabilities[0].State != installations.StateFailed || find(t, li, rowan).Capabilities[0].State != installations.StateEnabled {
		t.Fatalf("list_installations after the stop: %v %s", isErr, text)
	}
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, actions.StateFailed) {
		t.Fatalf("merge a stopped wave: %v %s", isErr, text)
	}
	if w, text, isErr := watchCall(t, adminLive(t, st), a.Name); isErr || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed || w.Report != "" {
		t.Fatalf("watch a stopped wave while red: %v %s", isErr, text)
	}
}

// birch's watch says enabled: its report names the next stage, the actor's
// merge takes rowan's pull requests, rowan's watch ends the wave enabled.
func TestWaveAdvancesOnceTheWatchSaysEnabled(t *testing.T) {
	st := newStack(t)
	a, aliceC, _ := waveStage1(t, st)
	admin := adminLive(t, st)

	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.Installation != birch || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateRollingOut || w.Action.Status.Result != nil || !strings.Contains(w.Next, tools.ToolMergeAction) || !strings.Contains(w.Next, rowan) {
		t.Fatalf("birch's watch: %v %s", isErr, text)
	}
	if r := w.Action.Status.Rollout; r.Installations[0].State != actions.StateEnabled || r.Installations[1].State != actions.StateRollingOut || r.FinishedAt != nil {
		t.Fatalf("stages after birch: %+v", r.Installations)
	}
	if th := thread(t, st); len(th) != 2 || !strings.Contains(th[1], "*"+birch+"* is *"+actions.StateEnabled+"* (stage 1 of 2)") || !strings.Contains(th[1], "Next: "+alice+" merges the pull requests of *"+rowan+"*") {
		t.Fatalf("the thread: %q", th)
	}

	m, text, isErr := mergeCall(t, aliceC, a.Name)
	if isErr || m.Stage != rowan || len(m.Merged) == 0 || m.Action.Status.State != actions.StateRollingOut {
		t.Fatalf("stage 2: %v %s", isErr, text)
	}
	for _, pr := range m.Action.Status.PullRequests {
		if pr.State != actions.PullRequestMerged {
			t.Fatalf("after stage 2: %+v", pr)
		}
	}
	populateStage(t, st.inst, *m.Action, rowan)
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.Installation != rowan || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || w.Action.Status.Result == nil ||
		w.Action.Status.Result.Message != "every installation of the wave is verified: "+birch+", "+rowan || w.Action.Status.Rollout.FinishedAt == nil {
		t.Fatalf("rowan's watch: %v %s", isErr, text)
	}
	if th := thread(t, st); len(th) != 4 || !strings.Contains(th[3], "(stage 2 of 2)") || !strings.Contains(th[3], "Done: the action is "+actions.StateEnabled) {
		t.Fatalf("the thread: %q", th)
	}
	// list_installations reads the files' state again — both markers on the
	// default branch since the merges — with the wave's result as the last action.
	li, text, isErr := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []string{birch, rowan}})
	if isErr || find(t, li, birch).Capabilities[0].State != installations.StateEnabled || find(t, li, rowan).Capabilities[0].State != installations.StateEnabled || find(t, li, rowan).Capabilities[0].LastAction.Result != actions.StateEnabled {
		t.Fatalf("list_installations after the wave: %v %s", isErr, text)
	}
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, actions.StateEnabled) {
		t.Fatalf("merge a done wave: %v %s", isErr, text)
	}
}

// redEdgeProbe makes the installation's edge metadata probe answer 404 and
// hands back what restores it.
func redEdgeProbe(t *testing.T, st *stack, c *client.Client, installation string) func() {
	t.Helper()
	text, isErr := call(t, c, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: installation})
	var v verify.Result
	if isErr || json.Unmarshal([]byte(text), &v) != nil {
		t.Fatalf("verify %s: %v %s", installation, isErr, text)
	}
	u, err := url.Parse(dimension(t, feature(t, v, "tool-access"), edgeProbe).Probe.Requests[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	st.probes.answer(u.Host+u.Path, http.StatusNotFound)
	return func() { st.probes.restore(u.Host + u.Path) }
}

// A wave stopped by a red probe is re-read once the cause is fixed: while
// the probe is red a watch answers failed and changes nothing; green again,
// birch is enabled with the report naming the stop and the recovery, rowan
// is queued again, the actor's merge takes rowan's pull requests and rowan's
// watch ends the wave enabled.
func TestWaveRecoversFromARedProbe(t *testing.T) {
	st := newStack(t)
	a, aliceC, _ := waveStage1(t, st)
	admin := adminLive(t, st)
	restore := redEdgeProbe(t, st, aliceC, birch)
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed {
		t.Fatalf("the stop: %v %s", isErr, text)
	}
	stopped := w.Action.Status.Result.Message

	// Still red: failed, the probe named, the record as it was.
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed || w.Action.Status.Result.Message != stopped || w.Report != "" || len(w.Red) == 0 || w.Action.Status.Rollout.Installations[1].State != "" {
		t.Fatalf("re-read while red: %v %s", isErr, text)
	}
	if len(thread(t, st)) != 2 {
		t.Fatal("a re-read while red posted again")
	}

	restore()
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.Installation != birch || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateRollingOut || w.Action.Status.Result != nil || w.Action.Status.Rollout.FinishedAt != nil ||
		w.Action.Status.Rollout.Installations[1].State != actions.StateRollingOut || !strings.Contains(w.Next, tools.ToolMergeAction) {
		t.Fatalf("the recovery: %v %s", isErr, text)
	}
	for _, want := range []string{"*" + birch + "* is *" + actions.StateEnabled + "* (stage 1 of 2)", "Recovered: the stage had failed (a probe is red: ", edgeProbe, "the wave goes on from here", "Next: " + alice + " merges the pull requests of *" + rowan + "*"} {
		if !strings.Contains(w.Report, want) {
			t.Errorf("the report lacks %q:\n%s", want, w.Report)
		}
	}
	if th := thread(t, st); len(th) != 3 || th[2] != w.Report {
		t.Fatalf("the thread: %q", th)
	}
	m, text, isErr := mergeCall(t, aliceC, a.Name)
	if isErr || m.Stage != rowan || len(m.Merged) == 0 || m.Action.Status.State != actions.StateRollingOut {
		t.Fatalf("stage 2 after the recovery: %v %s", isErr, text)
	}
	populateStage(t, st.inst, *m.Action, rowan)
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.Installation != rowan || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || w.Action.Status.Result == nil || w.Action.Status.Result.State != actions.StateEnabled || w.Action.Status.Rollout.FinishedAt == nil {
		t.Fatalf("rowan's watch: %v %s", isErr, text)
	}
	li, text, isErr := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []string{birch, rowan}})
	if isErr || find(t, li, birch).Capabilities[0].State != installations.StateEnabled || find(t, li, rowan).Capabilities[0].State != installations.StateEnabled {
		t.Fatalf("list_installations after the recovery: %v %s", isErr, text)
	}
}

// A rotated Dex client secret Dex has not read: mcp-capi's Secret in Dex's
// namespace changes after Dex's container started, so Dex holds the old
// secret and the server's sign-ins fail. The watch that gates stage 2 reads
// birch failed, the stage naming the client and its Secret, and the wave
// stops before rowan; Dex restarted, the stage is enabled again and the wave
// goes on.
func TestWaveStopsAtADexHoldingAnOldClientSecret(t *testing.T) {
	st := newStack(t)
	a, _, birchInst := waveStage1(t, st)
	admin := adminLive(t, st)
	const rotated, restarted = "2026-09-23T17:00:00Z", "2026-09-23T17:01:30Z"
	birchInst.edit("Secret", "giantswarm", "dex-client-mcp-capi", func(obj map[string]any) {
		obj[metadataKey].(map[string]any)[managedFieldsKey] = []any{dataWrite(rotated)}
	})
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.Installation != birch || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed || w.Action.Status.Rollout.Installations[1].State != "" {
		t.Fatalf("the stop: %v %s", isErr, text)
	}
	stage := w.Action.Status.Rollout.Installations[0].Message
	for _, want := range []string{"a probe is red: live-dex-client-secrets-loaded (identity)", "Secret giantswarm/dex-client-mcp-capi changed its data at " + rotated, "it holds the value from before", "client mcpCapi"} {
		if !strings.Contains(stage, want) {
			t.Errorf("the stage's message lacks %q: %s", want, stage)
		}
	}
	if len(w.Red) != 1 || strings.Contains(stage, "dex-client-mcp-kubernetes") {
		t.Errorf("only mcp-capi's client is red: %q", w.Red)
	}

	birchInst.edit("Pod", "giantswarm", "dex-0", func(obj map[string]any) {
		status := obj[statusKey].(map[string]any)["containerStatuses"].([]any)[0].(map[string]any)
		status["state"] = map[string]any{"running": map[string]any{startedAtKey: restarted}}
	})
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateRollingOut || w.Action.Status.Rollout.Installations[1].State != actions.StateRollingOut ||
		!strings.Contains(w.Report, "Recovered: the stage had failed (a probe is red: live-dex-client-secrets-loaded") {
		t.Fatalf("after Dex restarted: %v %s", isErr, text)
	}
}

// The actor withdraws a wave that stopped on a red probe: deny_action closes
// the next stage's open pull requests and records who withdrew them and why;
// the action stays failed, the approval as decided, and the withdrawn stage
// is not re-read any more.
func TestDenyWithdrawsAStoppedWave(t *testing.T) {
	st := newStack(t)
	a, aliceC, _ := waveStage1(t, st)
	redEdgeProbe(t, st, aliceC, birch)
	if w, text, isErr := watchCall(t, adminLive(t, st), a.Name); isErr || w.State != actions.StateFailed {
		t.Fatalf("the stop: %v %s", isErr, text)
	}
	const reason = "the definition is fixed forward by a new action"
	d, text, isErr := decide(t, aliceC, tools.ToolDenyAction, map[string]any{tools.ArgAction: a.Name, tools.ArgReason: reason})
	if isErr || d.Action.Status.State != actions.StateFailed || d.Action.Status.Approval.Decision != actions.DecisionApproved || d.Action.Status.Approval.DecidedBy != carol ||
		!strings.Contains(d.Action.Status.Result.Message, "withdrawn by "+alice+": "+reason) || !strings.Contains(d.Message, "closed") {
		t.Fatalf("the withdrawal: %v %s", isErr, text)
	}
	for _, pr := range d.Action.Status.PullRequests {
		if pr.Installation == rowan && pr.State != actions.PullRequestClosed {
			t.Errorf("rowan's pull request: %+v", pr)
		}
	}
	for _, pr := range st.remote.PullRequests() {
		if strings.HasPrefix(pr.Head, "platform/"+a.Name+"/"+rowan) && !pr.Closed {
			t.Errorf("rowan's pull request on the remote: %+v", pr)
		}
	}
	if st := d.Action.Status.Rollout.Installations; !strings.HasPrefix(st[0].Message, "withdrawn by "+alice) || !strings.Contains(st[1].Message, "closed by "+alice) {
		t.Errorf("the stages: %+v", st)
	}
	if _, text, isErr := watchCall(t, adminLive(t, st), a.Name); !isErr || !strings.Contains(text, "only a stage that failed on a probe is re-read") {
		t.Fatalf("a watch after the withdrawal: %v %s", isErr, text)
	}
}
