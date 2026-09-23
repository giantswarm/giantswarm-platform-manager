package e2e

// The rollout watch on the live path: after the merge the actor's — or any
// signed-in person's — watch_action reads the installation's Flux objects
// through muster as that person, keeps the action rolling out while one is
// not Ready, and once every one is Ready runs the probes and carries the
// action to enabled, waiting for the customer or failed, the report into the
// review's thread; a stage waiting for the customer flips to enabled on the
// next watch or verify; a report the gateway did not take is posted by the
// next call.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"

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
	populateStage(t, st.inst, *m.Action, rowan)
	return *m.Action, aliceC
}

// populateStage fills inst as the action's inputs on record render installation.
func populateStage(t *testing.T, inst *fakeInstallation, a actions.Action, installation string) {
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

	// A stranger to the installation: nothing read, nothing decided — and,
	// not connected to the App-pinned registration either, the pull requests
	// not re-read for them; the answer says so.
	w, text, isErr := watchCall(t, st.liveClient(t, st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour)), a.Name)
	if isErr || w.Ready || w.State != actions.StateRollingOut || len(w.Objects) == 0 || w.Objects[0].Ready != "" || !strings.Contains(w.Objects[0].Message, "not connected to the installation in muster") || !strings.Contains(w.Message, "not re-read") {
		t.Fatalf("the stranger's watch: %v %s", isErr, text)
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
	if isErr || w.State != actions.StateEnabled || w.Report != "" || len(thread(t, st)) != 2 {
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
	for _, want := range []string{"*" + rowan + "* is *" + actions.StateWaitingForCustomer + "*", "❌ live-model-configs (runtime): Accepted=False", openCustomerActions, "kagent-anthropic-key"} {
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
