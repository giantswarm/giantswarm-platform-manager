package e2e

// The rollout watch, a live tool: after the merge the actor's — or any
// signed-in person's — watch_action reads the installation's Flux objects
// through muster as that person, keeps the action rolling out while one is
// not Ready, and once every one is Ready runs the probes and carries the
// action to enabled, waiting for the customer or failed, the report into the
// review's thread; a stage waiting for the customer flips to enabled on the
// next watch or verify; a report the gateway did not take is posted by the
// next call.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The HelmRelease the watch tests turn not Ready, and the anonymous probe
// they break.
const (
	helmReleaseKind = "HelmRelease"
	platformRelease = "agent-platform"
	edgeProbe       = "muster-protected-resource-metadata"
	modelConfig     = "default-model-config"
	accepted        = "Accepted"
	statusTrue      = "True"
	statusFalse     = "False"
	modelKeyMissing = "secret kagent-anthropic-key not found"
	liveDrift       = "live-drift"
	// The report's two lines on the customer's actions.
	openCustomerActions = "Open customer actions:"
	customerActionsDone = "Customer actions done: model-key."
)

// setAccepted turns the default ModelConfig's Accepted condition.
func setAccepted(inst *fakeInstallation, status, msg string) {
	inst.edit("ModelConfig", kagentNamespace, modelConfig, func(obj map[string]any) {
		obj[statusKey] = map[string]any{conditionsKey: []any{map[string]any{typeKey: accepted, statusKey: status, message: msg}}}
	})
}

// rolledOut commits the enablement of rowan as alice, has carol approve
// and alice merge, and builds the fake installation from the action's inputs
// on record: an action in rolling out with a healthy installation behind it.
func rolledOut(t *testing.T, st *stack) (actions.Action, *client.Client) {
	t.Helper()
	a, aliceC := mergedRowan(t, st)
	populateStage(t, st.inst, a, rowan)
	return a, aliceC
}

// mergedRowan commits the enablement of rowan as alice, has carol approve
// and alice merge: an action in rolling out, nothing on the installation yet.
func mergedRowan(t *testing.T, st *stack) (actions.Action, *client.Client) {
	t.Helper()
	out, aliceC := commitRowan(t, st)
	name := out.Action.Name
	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: name}); isErr {
		t.Fatalf("approve: %s", text)
	}
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	m, text, isErr := mergeCall(t, aliceC, name)
	if isErr || m.Action == nil || m.Action.Status.State != actions.StateRollingOut {
		t.Fatalf("merge: %v %s", isErr, text)
	}
	return *m.Action, aliceC
}

// populateStage fills inst as the action's inputs on record render
// installation, with the stage's merge applied by Flux (fluxApplied).
func populateStage(t *testing.T, inst *fakeInstallation, a actions.Action, installation string) {
	t.Helper()
	populateBeforeFlux(t, inst, a, installation)
	fluxApplied(inst, a, installation)
}

// fluxBehind fills inst as the action's inputs on record render
// installation, every object Ready, with Flux not yet on the stage's merge:
// the Konfiguration on the commit before the merge of the stage's configs
// pull request, the meta chart's release on the values from before it.
func fluxBehind(t *testing.T, st *stack, inst *fakeInstallation, a actions.Action, installation string) {
	t.Helper()
	populateBeforeFlux(t, inst, a, installation)
	for _, k := range a.StagePullRequests(installation) {
		pr := a.Status.PullRequests[k]
		if !strings.HasSuffix(pr.Repository, "-configs") {
			continue
		}
		parent := st.ghs.parentOf(pr.Repository, cmp.Or(pr.MergeCommit, pr.HeadSHA))
		if parent == "" {
			t.Fatalf("%s#%d: its merge has no parent on the history", pr.Repository, pr.Number)
		}
		inst.followConfigs(fluxNamespace, pr.Repository)
		inst.applyConfigs(fluxNamespace, konfiguration, parent)
	}
}

// populateBeforeFlux fills inst as the action's inputs on record render
// installation; the Konfiguration and the meta chart's release as they were
// before any merge of the tests.
func populateBeforeFlux(t *testing.T, inst *fakeInstallation, a actions.Action, installation string) {
	t.Helper()
	def, ok := installations.FindCapability(a.Spec.Capability)
	if !ok {
		t.Fatalf("capability %q", a.Spec.Capability)
	}
	values := a.InputsOnRecord(installation)
	if values == nil {
		t.Fatalf("action %s holds no inputs on record for %s", a.Name, installation)
	}
	in, err := def.Parse(values)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.Render(values, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		t.Fatal(err)
	}
	valuesFile := ""
	for _, files := range res.Files {
		for path, f := range files {
			if strings.HasSuffix(path, "/apps/"+def.Name+"/"+valuesFileKey+".patch") {
				valuesFile = string(f.Content)
			}
		}
	}
	inst.populate(t, installation, res, valuesFile)
}

// fluxApplied has inst run the merge of installation's pull requests: the
// Konfiguration applied the merge commit of the stage's configs pull
// request, and the meta chart's HelmRelease ran a release with the values
// it wrote — what the watch proves before it decides the stage.
func fluxApplied(inst *fakeInstallation, a actions.Action, installation string) {
	for _, k := range a.StagePullRequests(installation) {
		pr := a.Status.PullRequests[k]
		if !strings.HasSuffix(pr.Repository, "-configs") {
			continue
		}
		inst.followConfigs(fluxNamespace, pr.Repository)
		// The Fake fast-forwards the base: the merge commit is the head.
		inst.applyConfigs(fluxNamespace, konfiguration, cmp.Or(pr.MergeCommit, pr.HeadSHA))
	}
	inst.release(fluxNamespace, platformRelease, valuesMerged)
}

func watchCall(t *testing.T, c *client.Client, name string) (tools.WatchResult, string, bool) {
	t.Helper()
	text, isErr := call(t, c, tools.ToolWatchAction, map[string]any{tools.ArgAction: name})
	var w tools.WatchResult
	if !isErr && json.Unmarshal([]byte(text), &w) != nil {
		t.Fatalf("decode %s: %s", tools.ToolWatchAction, text)
	}
	return w, text, isErr
}

func adminLive(t *testing.T, st *stack) *client.Client {
	t.Helper()
	return st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour))
}

// thread is the first review's thread as the fake gateway holds it.
func thread(t *testing.T, st *stack) []string {
	t.Helper()
	posted := st.gateway.posted()
	if len(posted) != 1 {
		t.Fatalf("%d review(s) posted", len(posted))
	}
	var out []string
	for _, r := range posted[0].Results {
		out = append(out, fmt.Sprint(r["text"]))
	}
	return out
}

// setReady turns the Ready condition of a HelmRelease of the fake installation.
func setReady(inst *fakeInstallation, name, status, msg, revision string) {
	inst.edit(helmReleaseKind, fluxNamespace, name, func(obj map[string]any) {
		st := obj[statusKey].(map[string]any)
		c := st[conditionsKey].([]any)[0].(map[string]any)
		c[statusKey], c[message] = status, msg
		st["lastAttemptedRevision"] = revision
	})
}

func stageOf(a *actions.Action, installation string) actions.InstallationRollout {
	for _, st := range a.Status.Rollout.Installations {
		if st.Name == installation {
			return st
		}
	}
	return actions.InstallationRollout{}
}

func probeOnRecord(a *actions.Action, installation, id string) (actions.Probe, bool) {
	for _, p := range a.Status.Probes {
		if p.Installation == installation && p.ID == id {
			return p, true
		}
	}
	return actions.Probe{}, false
}

// After the merge nothing but the watch moves the action: a HelmRelease not
// Ready keeps it rolling out with the object named, a person not connected
// to the installation reads nothing and claims nothing, and once every
// object is Ready the probes run and the stage is enabled — the report in
// the review's thread, the probes on record, list_installations enabled.
func TestWatchActionCarriesTheRolloutToEnabled(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, tools.ToolWatchAction) {
		t.Fatalf("merge after the merge: %v %s", isErr, text)
	}

	// A stranger to the installation: nothing read, nothing decided — the
	// pull requests re-read with their own GitHub token all the same, no
	// note in the answer.
	w, text, isErr := watchCall(t, st.liveClient(t, st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour)), a.Name)
	if isErr || w.Ready || w.State != actions.StateRollingOut || len(w.Objects) == 0 || w.Objects[0].Ready != "" || !strings.Contains(w.Objects[0].Message, "not connected to the installation in muster") || strings.Contains(w.Message, "not re-read") {
		t.Fatalf("the stranger's watch: %v %s", isErr, text)
	}
	// Named, not counted: muster's auth_required on the installation's
	// kubernetes server, the sign-in that fixes it, the checks it left unread.
	server := rowan + "-mcp-kubernetes"
	if len(w.NotRead) == 0 || w.NotRead[0].Cause != tools.CauseAuthRequired || w.NotRead[0].Server != server || w.NotRead[0].SignInURL == "" || w.NotRead[0].Checks < len(w.Objects) ||
		!strings.Contains(w.Message, "auth_required on muster server "+server) || !strings.Contains(w.Message, "muster auth login --server "+server) {
		t.Fatalf("the stranger's watch names no sign-in: %+v %s", w.NotRead, w.Message)
	}

	setReady(st.inst, platformRelease, statusFalse, "install retries exhausted", "4.44.1")
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.Ready || w.State != actions.StateRollingOut || w.Action.Status.State != actions.StateRollingOut || w.Report != "" || !strings.Contains(w.Next, tools.ToolWatchAction) {
		t.Fatalf("not Ready: %v %s", isErr, text)
	}
	var platform *actions.RolloutObject
	for i := range w.Objects {
		if w.Objects[i].Name == platformRelease {
			platform = &w.Objects[i]
		}
	}
	if platform == nil || platform.Kind != helmReleaseKind || platform.Namespace != fluxNamespace || platform.Ready != statusFalse || platform.Revision != "4.44.1" || !strings.Contains(platform.Message, "install retries exhausted") {
		t.Fatalf("the object: %+v", w.Objects)
	}
	if st := stageOf(w.Action, rowan); st.State != actions.StateRollingOut || !strings.Contains(st.Message, fmt.Sprintf("rolling out: %d of %d Ready; not Ready: HelmRelease %s/%s (Ready=False: install retries exhausted)", len(w.Objects)-1, len(w.Objects), fluxNamespace, platformRelease)) ||
		st.WatchedBy != liveAdmin || st.WatchedAt == nil || st.ReportedAt != nil || len(st.Objects) != len(w.Objects) {
		t.Fatalf("the stage on record: %+v", st)
	}
	if n := len(thread(t, st)); n != 1 {
		t.Fatalf("the thread got %d result(s) while rolling out, want the merge's alone", n)
	}
	for _, c := range st.muster.seen() {
		if c.Person == liveAdmin && c.Args[instanceArg] != rowan+"-mcp-kubernetes" {
			t.Fatalf("a read elsewhere: %+v", c)
		}
	}

	setReady(st.inst, platformRelease, statusTrue, "ok", "4.44.1")
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || !w.Ready || w.State != actions.StateEnabled || len(w.Red) != 0 || w.Action.Status.State != actions.StateEnabled || w.Action.Status.Result == nil || w.Action.Status.Result.State != actions.StateEnabled || w.Action.Status.Rollout.FinishedAt == nil {
		t.Fatalf("Ready: %v %s", isErr, text)
	}
	if !strings.Contains(w.Action.Status.Result.Message, rowan+" is enabled") || !strings.Contains(w.Message, "enabled") {
		t.Fatalf("the result: %+v %q", w.Action.Status.Result, w.Message)
	}
	stage := stageOf(w.Action, rowan)
	if stage.State != actions.StateEnabled || stage.ReportedAt == nil || !strings.HasPrefix(stage.Message, "verified: ") {
		t.Fatalf("the stage: %+v", stage)
	}
	for _, want := range []string{"*" + rowan + "* is *" + actions.StateEnabled + "*", "watched as " + liveAdmin, "Pull requests: ", acmeMCs + "#1", acmeConfigs + "#2", "Rollout: HelmRelease " + fluxNamespace + "/" + platformRelease + " Ready=True (4.44.1)", "Probes: ✅ ", customerActionsDone, "Done: the action is enabled"} {
		if !strings.Contains(w.Report, want) {
			t.Errorf("the report lacks %q:\n%s", want, w.Report)
		}
	}
	if strings.Contains(w.Report, "❌") || strings.Contains(w.Report, openCustomerActions) {
		t.Errorf("a red mark or an open customer action in a green report:\n%s", w.Report)
	}
	if th := thread(t, st); len(th) != 2 || th[1] != w.Report {
		t.Fatalf("the thread: %q", th)
	}
	// rowan is a customer installation: the Account Engineers' channel is
	// told now, once, that the change is applied — who, what, why, the
	// account engineer and the pull requests.
	noticed := st.gateway.noticed()
	if len(noticed) != 1 || noticed[0]["channel"] != noticeChannel || stage.NoticedAt == nil {
		t.Fatalf("the applied notice: %+v (noticedAt %v)", noticed, stage.NoticedAt)
	}
	applied := fmt.Sprint(noticed[0]["text"])
	for _, want := range []string{"*" + alice + "* enabled *agent-platform* on *" + rowan + "* (account engineer Ada Example): the change is applied and verified.", ">*Why:* " + commitReason, "*What changed*\n• agent-platform: new"} {
		if !strings.Contains(applied, want) {
			t.Errorf("the applied notice lacks %q:\n%s", want, applied)
		}
	}
	if prs, _ := noticed[0]["pullRequests"].([]any); len(prs) != 2 {
		t.Errorf("the applied notice's pull requests: %v", prs)
	}
	for _, id := range []string{"live-helmreleases-ready", edgeProbe, "live-model-configs"} {
		if p, ok := probeOnRecord(w.Action, rowan, id); !ok || p.Result != string(verify.AsDefined) {
			t.Errorf("probe %s on record: %+v (%v)", id, p, ok)
		}
	}
	// list_installations reads the files' state again — enabled, the marker
	// on the default branch since the merge — with the action's result as
	// the last action.
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateEnabled || r.Capabilities[0].LastAction == nil || r.Capabilities[0].LastAction.Name != a.Name || r.Capabilities[0].LastAction.Result != actions.StateEnabled {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, actions.StateEnabled) {
		t.Fatalf("merge an enabled action: %v %s", isErr, text)
	}

	// A re-read of the enabled action: the picture, no second report.
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Report != "" || len(thread(t, st)) != 2 || len(st.gateway.noticed()) != 1 {
		t.Fatalf("re-read: %v %s", isErr, text)
	}
	assertNoLeak(t, "the report", w.Report)
	assertNoLeak(t, "the server's log", st.logs.String())
}

// The model key is the customer's: the default ModelConfig not Accepted is
// the one red dimension, so the stage — and the action — wait for the
// customer, the report names the open action, the next stage of a wave would
// wait too (merge_action says so). The key in, the next watch flips it to
// enabled; a report the gateway refused is posted by the call after.
func TestWatchActionWaitsForTheCustomerThenFlips(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	setAccepted(st.inst, statusFalse, modelKeyMissing)

	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || !w.Ready || w.State != actions.StateWaitingForCustomer || len(w.Red) != 1 || !strings.Contains(w.Red[0], "live-model-configs") ||
		w.Action.Status.State != actions.StateWaitingForCustomer || w.Action.Status.Result == nil || w.Action.Status.Result.State != actions.StateWaitingForCustomer || w.Action.Status.Rollout.FinishedAt == nil {
		t.Fatalf("waiting: %v %s", isErr, text)
	}
	for _, want := range []string{"*" + rowan + "* is *" + actions.StateWaitingForCustomer + "*", "❌ live-model-configs (runtime): default-model-config: Accepted=False", openCustomerActions, "kagent-anthropic-key"} {
		if !strings.Contains(w.Report, want) {
			t.Errorf("the report lacks %q:\n%s", want, w.Report)
		}
	}
	if strings.Contains(w.Report, customerActionsDone) {
		t.Errorf("the open action listed done:\n%s", w.Report)
	}
	if th := thread(t, st); len(th) != 2 || th[1] != w.Report {
		t.Fatalf("the thread: %q", th)
	}
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, "waits for the customer") {
		t.Fatalf("merge while waiting: %v %s", isErr, text)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateWaitingForCustomer {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}

	// The key is in; the gateway refuses the report: the state moves, the
	// answer says the thread was not told, the next call posts it.
	setAccepted(st.inst, statusTrue, "ok")
	st.gateway.setDown(true)
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || w.Action.Status.Result.State != actions.StateEnabled || !strings.Contains(w.Message, "could not be told") || stageOf(w.Action, rowan).ReportedAt != nil {
		t.Fatalf("flipped with the gateway down: %v %s", isErr, text)
	}
	if len(thread(t, st)) != 2 {
		t.Fatal("a report landed while the gateway was down")
	}
	st.gateway.setDown(false)
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Report == "" || stageOf(w.Action, rowan).ReportedAt == nil {
		t.Fatalf("the re-post: %v %s", isErr, text)
	}
	if th := thread(t, st); len(th) != 3 || !strings.Contains(th[2], "*"+rowan+"* is *"+actions.StateEnabled+"*") || !strings.Contains(th[2], customerActionsDone) || strings.Contains(th[2], openCustomerActions) {
		t.Fatalf("the thread: %q", th)
	}
	if _, text, isErr := watchCall(t, admin, a.Name); isErr || len(thread(t, st)) != 3 {
		t.Fatalf("a further re-read posted again: %v %s", isErr, text)
	}
}

// verify_installation flips a stage waiting for the customer the same way:
// the action's result becomes enabled.
func TestVerifyInstallationFlipsWaitingToEnabled(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	setAccepted(st.inst, statusFalse, modelKeyMissing)
	if w, text, isErr := watchCall(t, admin, a.Name); isErr || w.State != actions.StateWaitingForCustomer {
		t.Fatalf("waiting: %v %s", isErr, text)
	}
	if res := verifyLive(t, admin, rowan); res.State != installations.StateWaitingForCustomer {
		t.Fatalf("verify while waiting: %q", res.State)
	}
	setAccepted(st.inst, statusTrue, "ok")
	if res := verifyLive(t, admin, rowan); res.State != installations.StateEnabled {
		t.Fatalf("verify with the key in: %q", res.State)
	}
	got := getAction(t, aliceC, a.Name)
	if got.Status.State != actions.StateEnabled || got.Status.Result == nil || got.Status.Result.State != actions.StateEnabled || stageOf(&got, rowan).State != actions.StateEnabled {
		t.Fatalf("after the verify: %+v", got.Status)
	}
}

// A red probe that is not the customer's fails the action, naming it (the
// anonymous metadata probe and the live one read the same URL, so both are
// red); the
// report says so in the thread — and when the gateway no longer holds the
// review, the answer says the thread could not be told and the record
// stands. A failed action is over for the watch and the merge.
func TestWatchActionFailsNamingTheProbe(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	text, isErr := call(t, aliceC, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: rowan})
	var v verify.Result
	if isErr || json.Unmarshal([]byte(text), &v) != nil {
		t.Fatalf("verify rowan: %v %s", isErr, text)
	}
	u, err := url.Parse(dimension(t, feature(t, v, "tool-access"), edgeProbe).Probe.Requests[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	st.probes.answer(u.Host+u.Path, http.StatusNotFound)
	st.gateway.forget(a.Status.Approval.ReviewID)

	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || !w.Ready || w.State != actions.StateFailed || len(w.Red) != 2 || !strings.Contains(strings.Join(w.Red, "\n"), edgeProbe) || !strings.Contains(strings.Join(w.Red, "\n"), "404") ||
		w.Action.Status.State != actions.StateFailed || w.Action.Status.Result == nil || w.Action.Status.Result.State != actions.StateFailed || w.Action.Status.Rollout.FinishedAt == nil {
		t.Fatalf("failed: %v %s", isErr, text)
	}
	if msg := w.Action.Status.Result.Message; !strings.HasPrefix(msg, rowan+" failed: a probe is red: ") || !strings.Contains(msg, edgeProbe) || strings.Contains(msg, "stay open") {
		t.Fatalf("the result: %q", msg)
	}
	for _, want := range []string{"*" + rowan + "* is *" + actions.StateFailed + "*", "❌ " + edgeProbe + " (tool-access): ", "answered 404", "The action is failed."} {
		if !strings.Contains(w.Report, want) {
			t.Errorf("the report lacks %q:\n%s", want, w.Report)
		}
	}
	if !strings.Contains(w.Message, "could not be told") || !strings.Contains(w.Message, "no record of the review") || stageOf(w.Action, rowan).ReportedAt != nil {
		t.Fatalf("the gateway lost the review: %q", w.Message)
	}
	if p, ok := probeOnRecord(w.Action, rowan, edgeProbe); !ok || p.Result != string(verify.Drifted) || !strings.Contains(p.Message, "404") {
		t.Fatalf("the probe on record: %+v", p)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateFailed {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}
	// A failed stage is re-read: still red, the answer is failed with the
	// probe named; the report the gateway did not take is offered again.
	if w, text, isErr := watchCall(t, admin, a.Name); isErr || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed || len(w.Red) == 0 || !strings.Contains(w.Report, "❌ "+edgeProbe) {
		t.Fatalf("watch a failed action: %v %s", isErr, text)
	}
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, actions.StateFailed) {
		t.Fatalf("merge a failed action: %v %s", isErr, text)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// withoutMaxTokens takes M34's leaf out of rowan the way an action committed
// on the definition before M34 left it: the values file on record and the
// live values — the meta chart's ConfigMap and kagent's HelmRelease — lack
// kagent.providers.anthropic.config.maxTokens, which the definition of this
// version renders.
func withoutMaxTokens(t *testing.T, st *stack, a actions.Action) {
	t.Helper()
	const line = "        maxTokens: 32000\n"
	path := "installations/" + rowan + "/apps/" + a.Spec.Capability + "/" + valuesFileKey + ".patch"
	found := false
	for _, pr := range a.Status.PullRequests {
		content, ok := st.ghs.file(pr.Repository, path)
		if !ok {
			continue
		}
		without := strings.Replace(content, line, "", 1)
		if without == content {
			t.Fatalf("the record %s:%s carries no %q", pr.Repository, path, line)
		}
		st.ghs.addFiles(pr.Repository, map[string]string{path: without})
		st.inst.edit("ConfigMap", fluxNamespace, konfiguration, func(obj map[string]any) {
			obj["data"].(map[string]any)[valuesFileKey] = without
		})
		found = true
	}
	if !found {
		t.Fatalf("no repository of action %s holds %s", a.Name, path)
	}
	st.inst.edit(helmReleaseKind, fluxNamespace, "kagent", func(obj map[string]any) {
		config := obj["spec"].(map[string]any)["values"].(map[string]any)["providers"].(map[string]any)["anthropic"].(map[string]any)["config"].(map[string]any)
		delete(config, "maxTokens")
	})
}

// A definition released between the action's commit and its watch renders a
// leaf the commit never wrote (M34: kagent's maxTokens): the installation
// lacks it as the record does, and the watch judges the rollout by what the
// action committed — the stage is enabled, the leaf in the report as a
// planned change of the newer definition, never red — while verify_capability
// and verify_installation give the one difference one mark, planned, on the
// record and on the live half alike.
func TestWatchActionReadsALaterMigrationAsPlanned(t *testing.T) {
	const leaf, tag = "kagent.providers.anthropic.config.maxTokens", "M34"
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	withoutMaxTokens(t, st, a)

	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || !w.Ready || w.State != actions.StateEnabled || len(w.Red) != 0 || len(w.Planned) != 2 ||
		w.Action.Status.State != actions.StateEnabled || w.Action.Status.Result == nil || w.Action.Status.Result.State != actions.StateEnabled {
		t.Fatalf("enabled with the planned leaf: %v %s", isErr, text)
	}
	for _, p := range w.Planned {
		if !strings.Contains(p, "newer definition, not this action's: Added:") || !strings.Contains(p, tag) {
			t.Errorf("the planned dimension: %q", p)
		}
	}
	if w.Verify.Summary[verify.Planned] != 2 || w.Verify.Summary[verify.Drifted] != 0 {
		t.Errorf("the live result: %v", w.Verify.Summary)
	}
	for _, want := range []string{"*" + rowan + "* is *" + actions.StateEnabled + "*", "🔵 " + liveDrift + " (runtime): newer definition, not this action's: Added:", "🔵 live-kagent-provider-values (runtime): ", tag, "Done: the action is enabled."} {
		if !strings.Contains(w.Report, want) {
			t.Errorf("the report lacks %q:\n%s", want, w.Report)
		}
	}
	if strings.Contains(w.Report, "❌") {
		t.Errorf("the report is red:\n%s", w.Report)
	}
	if p, ok := probeOnRecord(w.Action, rowan, liveDrift); !ok || p.Result != string(verify.Planned) || !strings.Contains(p.Message, "1 planned change(s): Added:") || !strings.Contains(p.Message, tag) {
		t.Errorf("the probe on record: %+v", p)
	}

	text, isErr = call(t, aliceC, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: rowan})
	var repo verify.Result
	if isErr || json.Unmarshal([]byte(text), &repo) != nil {
		t.Fatalf("verify rowan: %v %s", isErr, text)
	}
	live := verifyLive(t, admin, rowan)
	merged := verify.Merge(repo, live)
	if merged.State != installations.StateEnabled || merged.Summary[verify.Drifted] != 0 || merged.Summary[verify.Planned] != 3 {
		t.Errorf("merged: %q %v", merged.State, merged.Summary)
	}
	for _, id := range []string{"kagent-providers", liveDrift, "live-kagent-provider-values"} {
		d := anyDimension(t, merged, id)
		if d.Mark != verify.Planned || len(d.Differences) != 1 || d.Differences[0].Path != leaf || !strings.Contains(d.Differences[0].Planned, tag) || d.Differences[0].Rendered != "32000" {
			t.Errorf("%s: %+v", id, d)
		}
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateEnabled {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}
}

// A HelmRelease whose upgrade Flux gave up on — Helm's wait timed out on a
// pod that never started, Released=False, the release rolled back — will
// not become Ready on its own: the watch fails the stage on the release's
// probe, naming Flux's verdict, instead of reading it rolling out for good.
// The release upgraded and Ready again, the re-read recovers the stage.
func TestWatchActionFailsAReleaseFluxGaveUpOn(t *testing.T) {
	const upgradeFailed = "Helm upgrade failed for release agent-platform/agent-platform with chart agent-platform@4.44.1: context deadline exceeded"
	st := newStack(t)
	a, _ := rolledOut(t, st)
	admin := adminLive(t, st)
	conditions := func(ready, readyMessage, released, releasedReason, releasedMessage string) {
		st.inst.edit(helmReleaseKind, fluxNamespace, platformRelease, func(obj map[string]any) {
			obj[statusKey].(map[string]any)[conditionsKey] = []any{
				map[string]any{typeKey: "Ready", statusKey: ready, message: readyMessage},
				map[string]any{typeKey: "Released", statusKey: released, "reason": releasedReason, message: releasedMessage},
			}
		})
	}
	conditions(statusFalse, "Helm rollback to previous release agent-platform/agent-platform.v3 succeeded", statusFalse, "UpgradeFailed", upgradeFailed)
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.Ready || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed || w.Action.Status.Result == nil ||
		!strings.Contains(strings.Join(w.Red, "\n"), "Released=False (UpgradeFailed): "+upgradeFailed) {
		t.Fatalf("the failed release: %v %s", isErr, text)
	}
	if msg := stageOf(w.Action, rowan).Message; !strings.HasPrefix(msg, "a probe is red: ") || !strings.Contains(msg, platformRelease) {
		t.Fatalf("the stage names the release: %q", msg)
	}
	conditions(statusTrue, "Helm upgrade succeeded", statusTrue, "UpgradeSucceeded", "upgraded")
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || !strings.Contains(w.Report, "Recovered: the stage had failed (a probe is red: ") {
		t.Fatalf("the recovery: %v %s", isErr, text)
	}
}

// configsMerge is the merge commit of stage installation's pull request on
// its configs repository, as the action records it.
func configsMerge(t *testing.T, a *actions.Action, installation string) (actions.PullRequest, string) {
	t.Helper()
	for _, k := range a.StagePullRequests(installation) {
		if pr := a.Status.PullRequests[k]; strings.HasSuffix(pr.Repository, "-configs") {
			return pr, cmp.Or(pr.MergeCommit, pr.HeadSHA)
		}
	}
	t.Fatalf("%s has no configs pull request", installation)
	return actions.PullRequest{}, ""
}

// Every HelmRelease Ready is not yet the merged change (giantswarm-platform-
// manager#888): after the merge the releases stay Ready on the values from
// before until Flux reconciles them. The watch keeps the stage rolling out,
// naming what it waits for, while the Konfiguration has not applied a commit
// that contains the merge, and while the meta chart's release runs a release
// from before the merge or one with the values from before it (a chart-only
// upgrade); a release with the merged values carries it to enabled.
func TestWatchActionWaitsForFluxToApplyTheMerge(t *testing.T) {
	st := newStack(t)
	a, aliceC := mergedRowan(t, st)
	admin := adminLive(t, st)
	fluxBehind(t, st, st.inst, a, rowan)
	pr, merge := configsMerge(t, &a, rowan)
	release := helmReleaseKind + " " + fluxNamespace + "/" + platformRelease
	rollingOut := func(step string, want ...string) {
		t.Helper()
		w, text, isErr := watchCall(t, admin, a.Name)
		if isErr || !w.Ready || w.State != actions.StateRollingOut || w.Action.Status.State != actions.StateRollingOut || w.Report != "" {
			t.Fatalf("%s: %v %s", step, isErr, text)
		}
		msg := stageOf(w.Action, rowan).Message
		for _, s := range want {
			if !strings.Contains(msg, s) {
				t.Errorf("%s: the stage's message lacks %q: %q", step, s, msg)
			}
		}
	}

	// Every object Ready, Flux on the commit before the merge.
	behind := st.ghs.parentOf(pr.Repository, merge)
	rollingOut("before Flux",
		fmt.Sprintf("Konfiguration %s/%s applied %s, not yet %s#%d's merge %s", fluxNamespace, konfiguration, behind[:12], pr.Repository, pr.Number, merge[:12]),
		release+" runs release v10, deployed at 2026-09-23T15:00:00Z, before "+fmt.Sprintf("%s#%d's merge", pr.Repository, pr.Number))
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, rowan+" is "+actions.StateRollingOut) {
		t.Fatalf("merge before the merged revision runs: %v %s", isErr, text)
	}

	// The Konfiguration on a commit after the merge, the release not
	// reconciled yet: the release is what the stage waits for.
	later := st.ghs.commitByHand(pr.Repository, "README.md", "a later change\n")
	st.inst.applyConfigs(fluxNamespace, konfiguration, later)
	rollingOut("the Konfiguration applied", release+" runs release v10")
	if got := getAction(t, aliceC, a.Name); strings.Contains(stageOf(&got, rowan).Message, "Konfiguration") {
		t.Errorf("the Konfiguration on a later commit is still waited for: %q", stageOf(&got, rowan).Message)
	}

	// Two releases after the merge with the values from before: Flux's
	// history keeps v12 and v11 alone, and the stage's record of v10, the
	// release that ran at the merge, still tells the values are old.
	st.inst.release(fluxNamespace, platformRelease, valuesBefore)
	st.inst.release(fluxNamespace, platformRelease, valuesBefore)
	rollingOut("two chart-only releases", release+" runs release v12 with the values of release v10 from before")

	st.inst.release(fluxNamespace, platformRelease, valuesMerged)
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || w.Report == "" {
		t.Fatalf("the merged values released: %v %s", isErr, text)
	}
}

// A release history that keeps no release from before the merge proves
// nothing: two chart-only upgrades after the merge, before any watch, leave
// Flux's history with v12 and v11 alone, both on the values from before, and
// no release that ran at the merge is on record. The stage stays rolling
// out, saying what is not known — even once a release with other values
// runs, since nothing tells what it is compared with.
func TestWatchActionNeedsTheReleaseThatRanAtTheMerge(t *testing.T) {
	st := newStack(t)
	a, _ := mergedRowan(t, st)
	admin := adminLive(t, st)
	fluxBehind(t, st, st.inst, a, rowan)
	_, merge := configsMerge(t, &a, rowan)
	st.inst.applyConfigs(fluxNamespace, konfiguration, merge)
	st.inst.release(fluxNamespace, platformRelease, valuesBefore)
	st.inst.release(fluxNamespace, platformRelease, valuesBefore)
	unknown := helmReleaseKind + " " + fluxNamespace + "/" + platformRelease + " runs release v%d since "
	for _, step := range []struct {
		name    string
		digest  string
		version int
	}{{"two chart-only releases", "", 12}, {"a release with other values", valuesMerged, 13}} {
		if step.digest != "" {
			st.inst.release(fluxNamespace, platformRelease, step.digest)
		}
		w, text, isErr := watchCall(t, admin, a.Name)
		if isErr || !w.Ready || w.State != actions.StateRollingOut || w.Action.Status.State != actions.StateRollingOut {
			t.Fatalf("%s: %v %s", step.name, isErr, text)
		}
		if msg := stageOf(w.Action, rowan).Message; !strings.Contains(msg, fmt.Sprintf(unknown, step.version)) || !strings.Contains(msg, "neither in its history nor on record") {
			t.Fatalf("%s: the stage's message: %q", step.name, msg)
		}
	}
}

// The release that ran at the merge is recorded on the first read after the
// merge, Ready or not: a meta chart release not Ready yet, then two releases
// with the values from before that leave Flux's history without it — the
// record still tells their values are old.
func TestWatchActionRecordsTheReleaseThatRanAtTheMergeWhileNotReady(t *testing.T) {
	st := newStack(t)
	a, _ := mergedRowan(t, st)
	admin := adminLive(t, st)
	fluxBehind(t, st, st.inst, a, rowan)
	setReady(st.inst, platformRelease, statusFalse, "upgrading", "4.44.1")
	if w, text, isErr := watchCall(t, admin, a.Name); isErr || w.Ready || w.State != actions.StateRollingOut {
		t.Fatalf("not Ready: %v %s", isErr, text)
	}
	_, merge := configsMerge(t, &a, rowan)
	st.inst.applyConfigs(fluxNamespace, konfiguration, merge)
	st.inst.release(fluxNamespace, platformRelease, valuesBefore)
	st.inst.release(fluxNamespace, platformRelease, valuesBefore)
	setReady(st.inst, platformRelease, statusTrue, "upgraded", "4.50.0")
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || !w.Ready || w.State != actions.StateRollingOut {
		t.Fatalf("two chart-only releases: %v %s", isErr, text)
	}
	if msg := stageOf(w.Action, rowan).Message; !strings.Contains(msg, "runs release v12 with the values of release v10 from before") {
		t.Fatalf("the stage's message: %q", msg)
	}
}

// Flux's release times are whole seconds: a release with other values in
// the merge's own second ran before Flux could apply the merge, and proves
// nothing.
func TestWatchActionCountsAReleaseInTheMergesSecondBeforeIt(t *testing.T) {
	st := newStack(t)
	a, _ := mergedRowan(t, st)
	admin := adminLive(t, st)
	fluxBehind(t, st, st.inst, a, rowan)
	pr, merge := configsMerge(t, &a, rowan)
	if pr.MergedAt == nil {
		t.Fatalf("%s#%d records no merge time", pr.Repository, pr.Number)
	}
	st.inst.releaseAt(fluxNamespace, platformRelease, valuesMerged, pr.MergedAt.Truncate(time.Second))
	st.inst.applyConfigs(fluxNamespace, konfiguration, merge)
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || !w.Ready || w.State != actions.StateRollingOut {
		t.Fatalf("a release in the merge's second: %v %s", isErr, text)
	}
	if msg := stageOf(w.Action, rowan).Message; !strings.Contains(msg, "runs release v11, deployed at "+pr.MergedAt.UTC().Truncate(time.Second).Format(time.RFC3339)+", before") {
		t.Fatalf("the stage's message: %q", msg)
	}
}

// A merge that rewrites the values file with the values it had — a comment,
// the layout — composes the same values, and Flux runs no release for it:
// the stage asks no new release of the meta chart's HelmRelease and is
// enabled once the Konfiguration applied the merge.
func TestWatchActionAsksNoReleaseOfAMergeThatKeepsTheValues(t *testing.T) {
	st := newStack(t)
	a, _ := mergedRowan(t, st)
	admin := adminLive(t, st)
	pr, merge := configsMerge(t, &a, rowan)
	path := "installations/" + rowan + "/apps/" + installations.AgentPlatform + "/" + valuesFileKey + ".patch"
	after := st.ghs.fileAt(pr.Repository, merge, path)
	if after == "" {
		t.Fatalf("the merge wrote no %s", path)
	}
	st.ghs.rewriteBefore(pr.Repository, merge, path, "# the values before the merge, as the merge left them\n"+after)
	fluxBehind(t, st, st.inst, a, rowan)
	st.inst.applyConfigs(fluxNamespace, konfiguration, merge)

	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled {
		t.Fatalf("the values kept: %v %s", isErr, text)
	}
}

// A merge Flux never applies does not keep the stage rolling out for good:
// past the bound after the last merge, the stage fails on what it waits for,
// as on a release Flux gave up on, the report naming it; the re-read once
// Flux applied the merge recovers it.
func TestWatchActionFailsAMergeFluxNeverApplies(t *testing.T) {
	st := newStack(t)
	a, _ := mergedRowan(t, st)
	admin := adminLive(t, st)
	fluxBehind(t, st, st.inst, a, rowan)
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateRollingOut {
		t.Fatalf("within the bound: %v %s", isErr, text)
	}
	status := w.Action.Status
	hourAgo := time.Now().Add(-time.Hour)
	for k := range status.PullRequests {
		status.PullRequests[k].MergedAt = &hourAgo
	}
	if _, err := actions.New(st.dyn, actionsNamespace).UpdateStatus(t.Context(), w.Action, status); err != nil {
		t.Fatal(err)
	}

	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateFailed || w.Action.Status.State != actions.StateFailed || len(w.Red) != 2 {
		t.Fatalf("past the bound: %v %s", isErr, text)
	}
	if msg := stageOf(w.Action, rowan).Message; !strings.HasPrefix(msg, "a probe is red: the installation is not proven to run the merged revision 30m0s after the last merge: Konfiguration ") {
		t.Fatalf("the stage's message: %q", msg)
	}
	if !strings.Contains(w.Report, "❌ not yet on the merged revision: Konfiguration "+fluxNamespace+"/"+konfiguration) || !strings.Contains(w.Report, "❌ not yet on the merged revision: "+helmReleaseKind+" "+fluxNamespace+"/"+platformRelease) {
		t.Fatalf("the report:\n%s", w.Report)
	}

	fluxApplied(st.inst, *w.Action, rowan)
	w, text, isErr = watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || !strings.Contains(w.Report, "Recovered: the stage had failed (a probe is red: the installation is not proven to run the merged revision") {
		t.Fatalf("Flux applied the merge: %v %s", isErr, text)
	}
}

// A one-installation action that failed on a probe reads enabled after a
// green re-read: the result is rewritten, the report names the recovery.
func TestWatchActionRecoversAFailedStage(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	restore := redEdgeProbe(t, st, aliceC, rowan)
	if w, text, isErr := watchCall(t, admin, a.Name); isErr || w.State != actions.StateFailed || w.Action.Status.Result.State != actions.StateFailed {
		t.Fatalf("the stop: %v %s", isErr, text)
	}
	restore()
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || w.Action.Status.State != actions.StateEnabled || w.Action.Status.Result == nil || w.Action.Status.Result.State != actions.StateEnabled ||
		!strings.HasPrefix(w.Action.Status.Result.Message, rowan+" is enabled: verified: ") || !strings.Contains(w.Report, "Recovered: the stage had failed (a probe is red: ") {
		t.Fatalf("the recovery: %v %s", isErr, text)
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateEnabled {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}
}

// The first call after the merges by hand is the watch: it re-reads the pull
// requests as the person with the GitHub token muster puts on the call as the
// bearer — get_action's own read, nothing looped back through muster — and
// watches the action that is rolling out now, no note in the answer.
func TestWatchActionReadsThePullRequestsAsThePerson(t *testing.T) {
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
	// The re-read ran as the admin's GitHub token: alice's.
	if w.Action.Status.SyncedBy != alice || !strings.Contains(st.logs.String(), "action_resynced") || strings.Contains(st.logs.String(), "action_resync_skipped") {
		t.Fatalf("synced by %q; log:\n%s", w.Action.Status.SyncedBy, st.logs.String())
	}
	if th := thread(t, st); len(th) != 2 || !strings.Contains(th[0], "Merged outside the manager by "+dave) || !strings.Contains(th[1], "*"+rowan+"* is *"+actions.StateEnabled+"*") {
		t.Fatalf("the thread: %q", th)
	}
}

// olderCommitMode is rowan's merged document in the organizations' commit
// mode as a manager before installation.repositories stored it — without
// the fact — and the same document with the repository that manager wrote,
// giantswarm/<customer>-management-clusters, to render the installation's
// objects from (the render's name is pinned by
// TestOrganizationsCommitReplaysADocumentWithoutTheRepositories).
func olderCommitMode(t *testing.T, st *stack) (stored, rendered map[string]any) {
	t.Helper()
	out, text, isErr := dryRun(t, st.mcpClient(t, aliceToken), tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: kagentEnabled()})
	if isErr {
		t.Fatalf("dry run: %s", text)
	}
	copyOf := func() map[string]any {
		b, err := json.Marshal(findPlan(t, out, rowan).Inputs)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		inst := m["installation"].(map[string]any)
		inst["chartLine"], inst["podCertificateRequest"] = "4", true
		org := m["organizations"].(map[string]any)
		org[enabledKey] = true
		org[organizationsCommit] = map[string]any{enabledKey: true}
		return m
	}
	stored, rendered = copyOf(), copyOf()
	delete(stored["installation"].(map[string]any), "repositories")
	rendered["installation"].(map[string]any)["repositories"] = map[string]any{"managementClusters": "giantswarm/acme-management-clusters"}
	return stored, rendered
}

// An Action an older manager stored in the organizations' commit mode
// carries no installation.repositories: the watch's render is not refused,
// it reads the rollout's Flux objects and judges the stage by them —
// enabled with nothing red.
func TestWatchActionReplaysCommitModeAnOlderManagerStored(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	stored, rendered := olderCommitMode(t, st)
	seedRowanLive(t, st, rendered, stored, map[string]any{"organizations": map[string]any{enabledKey: true, organizationsCommit: map[string]any{enabledKey: true}}}, "")

	w, text, isErr := watchCall(t, adminLive(t, st), liveAction)
	if isErr || w.Verify == nil {
		t.Fatalf("the replay of the stored inputs: %v %s", isErr, text)
	}
	if w.Verify.Refused != "" || len(w.Objects) == 0 || !w.Ready || w.State != actions.StateEnabled || len(w.Red) != 0 {
		t.Fatalf("the replay of the stored inputs: refused %q, %d Flux object(s), ready %v, state %q, red %v", w.Verify.Refused, len(w.Objects), w.Ready, w.State, w.Red)
	}
}

// storedAsRefused has installation's inputs on record of action name turn
// the organizations' switch on, which the 3 chart line of the fixtures
// refuses: a document the render of this manager refuses, as a later
// definition may refuse one an older manager stored.
func storedAsRefused(t *testing.T, st *stack, name, installation string) {
	t.Helper()
	storeOrganizationsSwitch(t, st, name, installation, true)
}

// storeOrganizationsSwitch sets the organizations' switch in installation's
// inputs on record of action name.
func storeOrganizationsSwitch(t *testing.T, st *stack, name, installation string, on bool) {
	t.Helper()
	a, err := actions.New(st.dyn, actionsNamespace).Get(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	in := a.InputsOnRecord(installation)
	if in == nil {
		t.Fatalf("action %s holds no inputs on record for %s", name, installation)
	}
	in["organizations"].(map[string]any)[enabledKey] = on
	if _, err := st.dyn.Resource(actions.GVR).Namespace(actionsNamespace).Update(t.Context(), actions.Unstructured(*a), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// refusedSwitch is what the render's refusal of storedAsRefused names.
const refusedSwitch = "organizations.enabled"

// A render the inputs on record are refused by reads no Flux object, and
// nothing it reads moves the stage: the action stays rolling out, and the
// stage's message on the Action says why, once however often it is watched,
// until a read of the inputs renders.
func TestARefusedRenderMovesNoStage(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	storedAsRefused(t, st, a.Name, rowan)

	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.Verify == nil || !strings.Contains(w.Verify.Refused, refusedSwitch) || w.State != actions.StateRollingOut || w.Ready || len(w.Objects) != 0 {
		t.Fatalf("the watch of a refused render: %v state %q, %d Flux object(s): %s", isErr, w.State, len(w.Objects), text)
	}
	got := getAction(t, aliceC, a.Name)
	stage := stageOf(&got, rowan)
	if got.Status.State != actions.StateRollingOut || stage.State != actions.StateRollingOut || !strings.Contains(stage.Message, refusedSwitch) {
		t.Fatalf("after the refused watch: %s, stage %s: %q", got.Status.State, stage.State, stage.Message)
	}
	if _, text, isErr := watchCall(t, admin, a.Name); isErr {
		t.Fatalf("the second watch of a refused render: %s", text)
	}
	if again := getAction(t, aliceC, a.Name); stageOf(&again, rowan).Message != stage.Message {
		t.Fatalf("the second refused watch changed the stage's message:\n%q\n%q", stage.Message, stageOf(&again, rowan).Message)
	}

	storeOrganizationsSwitch(t, st, a.Name, rowan, false)
	if res := verifyLive(t, admin, rowan); res.Refused != "" {
		t.Fatalf("verify of the inputs restored: refused %q", res.Refused)
	}
	read := getAction(t, aliceC, a.Name)
	if stage := stageOf(&read, rowan); stage.State != actions.StateRollingOut || strings.Contains(stage.Message, refusedSwitch) {
		t.Fatalf("after a verify that renders: stage %s: %q", stage.State, stage.Message)
	}
}

// probesOf are the probe entries of installation on a's record.
func probesOf(a actions.Action, installation string) []actions.Probe {
	var out []actions.Probe
	for _, p := range a.Status.Probes {
		if p.Installation == installation {
			out = append(out, p)
		}
	}
	return out
}

// verify_installation of a render the inputs on record are refused by
// answers the refusal, leaves a stage waiting for the customer waiting, keeps
// the probe results the last read recorded and says on the stage why; the
// refusal of inputs the caller hands over writes nothing on the Action.
func TestVerifyInstallationRefusedRenderKeepsTheWait(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	setAccepted(st.inst, statusFalse, modelKeyMissing)
	if w, text, isErr := watchCall(t, admin, a.Name); isErr || w.State != actions.StateWaitingForCustomer {
		t.Fatalf("waiting: %v %s", isErr, text)
	}
	waiting := getAction(t, aliceC, a.Name)
	probes, stage := probesOf(waiting, rowan), stageOf(&waiting, rowan)
	if len(probes) == 0 {
		t.Fatal("the watch recorded no probe")
	}
	setAccepted(st.inst, statusTrue, "ok")

	refused := waiting.InputsOnRecord(rowan)
	refused["organizations"].(map[string]any)[enabledKey] = true
	if res := verifyLiveWith(t, admin, rowan, verify.Inputs{Source: "handed over", Values: refused}); !strings.Contains(res.Refused, refusedSwitch) {
		t.Fatalf("verify of refused inputs handed over: refused %q", res.Refused)
	}
	if after := getAction(t, aliceC, a.Name); !reflect.DeepEqual(probesOf(after, rowan), probes) || !reflect.DeepEqual(stageOf(&after, rowan), stage) {
		t.Fatalf("the refusal of inputs handed over changed the Action: probes %+v, stage %+v", probesOf(after, rowan), stageOf(&after, rowan))
	}

	storedAsRefused(t, st, a.Name, rowan)
	if res := verifyLive(t, admin, rowan); !strings.Contains(res.Refused, refusedSwitch) {
		t.Fatalf("verify of a refused render: refused %q, state %q", res.Refused, res.State)
	}
	got := getAction(t, aliceC, a.Name)
	if stage := stageOf(&got, rowan); got.Status.State != actions.StateWaitingForCustomer || stage.State != actions.StateWaitingForCustomer || !strings.Contains(stage.Message, refusedSwitch) {
		t.Fatalf("after the verify: %s, stage %s: %q", got.Status.State, stage.State, stage.Message)
	}
	if kept := probesOf(got, rowan); !reflect.DeepEqual(kept, probes) {
		t.Fatalf("the refused verify replaced the probes on record:\n%+v\n%+v", probes, kept)
	}
}

// A stage failed on a red probe keeps the probe's cause in its message when
// a refused render is noted after it, so the watch still re-reads it; once
// the inputs render and the probe is green, the recovery names the red
// probe, not the refusal.
func TestARefusedRenderKeepsAProbeFailureReRead(t *testing.T) {
	st := newStack(t)
	a, aliceC := rolledOut(t, st)
	admin := adminLive(t, st)
	restore := redEdgeProbe(t, st, aliceC, rowan)
	if w, text, isErr := watchCall(t, admin, a.Name); isErr || w.State != actions.StateFailed {
		t.Fatalf("the red probe: %v %s", isErr, text)
	}
	storedAsRefused(t, st, a.Name, rowan)
	for n := range 2 {
		w, text, isErr := watchCall(t, admin, a.Name)
		if isErr || w.State != actions.StateFailed || w.Action == nil || !strings.Contains(stageOf(w.Action, rowan).Message, refusedSwitch) {
			t.Fatalf("refused watch %d of a stage failed on a probe: %v %s", n+1, isErr, text)
		}
	}

	storeOrganizationsSwitch(t, st, a.Name, rowan, false)
	restore()
	w, text, isErr := watchCall(t, admin, a.Name)
	if isErr || w.State != actions.StateEnabled || !strings.Contains(w.Report, "Recovered") || !strings.Contains(w.Report, edgeProbe) || strings.Contains(w.Report, refusedSwitch) {
		t.Fatalf("the recovery: %v %s", isErr, text)
	}
}
