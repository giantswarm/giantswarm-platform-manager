package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The fixtures of the watch tests.
const (
	helmRelease = "HelmRelease"
	fluxNS      = "flux-giantswarm"
	notReady    = "Ready=False: install retries exhausted"
	isReady     = "Ready=True"
	birch       = "birch"
	rowan       = "rowan"
)

func liveResult(state installations.State, dims ...verify.Dimension) verify.Result {
	return verify.Result{State: state, Features: []verify.Feature{{ID: "runtime", Dimensions: dims}}, Summary: map[verify.Mark]int{verify.AsDefined: 3}}
}

// The rollout picture is the readiness checks of the live result: a
// HelmRelease Ready is True with its revision, one not Ready carries the
// condition's status and message, one the person could not read claims
// nothing — and only every one Ready is ready.
func TestRolloutObjectsReadTheFluxChecks(t *testing.T) {
	res := liveResult(installations.StateEnabled, verify.Dimension{ID: "live-helmreleases-ready", Kind: definitions.KindLive, Live: &verify.LiveResult{Checks: []verify.Check{
		{Kind: string(render.HelmReleaseReady), Namespace: fluxNS, Resource: helmRelease, Name: installations.AgentPlatform, Mark: verify.AsDefined, Message: isReady, Revision: "4.44.1"},
		{Kind: string(render.HelmReleaseReady), Namespace: fluxNS, Resource: helmRelease, Name: "muster", Mark: verify.Drifted, Message: notReady},
		{Kind: string(render.Condition), Namespace: fluxNS, Resource: "Kustomization.kustomize.toolkit.fluxcd.io", Name: "extras", Mark: verify.NotChecked, Message: "forbidden for viewer: kustomizations is forbidden", Revision: "main@sha1:abc"},
		{Kind: string(render.Condition), Namespace: "kagent", Resource: "Deployment", Name: "kagent-ui", Mark: verify.AsDefined, Message: "Available=True"},
	}}})
	objects, ready, failed := rolloutObjects(res)
	want := []actions.RolloutObject{
		{Kind: helmRelease, Namespace: fluxNS, Name: installations.AgentPlatform, Ready: "True", Revision: "4.44.1", Message: isReady},
		{Kind: helmRelease, Namespace: fluxNS, Name: "muster", Ready: "False", Message: notReady},
		{Kind: "Kustomization", Namespace: fluxNS, Name: "extras", Revision: "main@sha1:abc", Message: "forbidden for viewer: kustomizations is forbidden"},
	}
	if ready || failed || !reflect.DeepEqual(objects, want) {
		t.Errorf("ready %v failed %v objects %+v", ready, failed, objects)
	}
	if _, ready, failed := rolloutObjects(liveResult(installations.StateEnabled, verify.Dimension{ID: "live-helmreleases-ready", Kind: definitions.KindLive, Live: &verify.LiveResult{Checks: []verify.Check{
		{Kind: string(render.HelmReleaseReady), Resource: helmRelease, Name: installations.AgentPlatform, Mark: verify.AsDefined, Message: isReady}}}})); !ready || failed {
		t.Errorf("one HelmRelease Ready is ready, not failed: ready %v failed %v", ready, failed)
	}
	// A HelmRelease whose last release failed is not Ready and failed: the
	// stage is decided, not rolling out.
	if _, ready, failed := rolloutObjects(liveResult(installations.StateDrifted, verify.Dimension{ID: "live-helmrelease-ready", Kind: definitions.KindLive, Live: &verify.LiveResult{Checks: []verify.Check{
		{Kind: string(render.HelmReleaseReady), Resource: helmRelease, Name: "backstage", Mark: verify.Drifted, Failed: true,
			Message: "Ready=False: Helm rollback succeeded; Released=False (UpgradeFailed): context deadline exceeded"}}}})); ready || !failed {
		t.Errorf("a failed release: ready %v failed %v", ready, failed)
	}
}

// The decision from a live result: clean is enabled; drift held by the
// customer's action is waiting for the customer; other drift fails a stage
// rolling out and marks a stage that had reached a state drifted.
func TestDecideStage(t *testing.T) {
	red := verify.Dimension{ID: "live-model-configs", Kind: definitions.KindLive, Mark: verify.Drifted, Live: &verify.LiveResult{Checks: []verify.Check{{Mark: verify.Drifted, Message: "Accepted=False: secret not found", Note: "the customer's key"}}}}
	probe := verify.Dimension{ID: "muster-protected-resource-metadata", Kind: definitions.KindProbe, Mark: verify.Drifted, Probe: &verify.ProbeResult{Requests: []verify.Request{{URL: "https://agentgateway.example/.well-known/oauth-protected-resource", Status: 404}}}}
	for _, tc := range []struct {
		prev  string
		res   verify.Result
		state string
		red   int
	}{
		{actions.StateRollingOut, liveResult(installations.StateEnabled), actions.StateEnabled, 0},
		{actions.StateRollingOut, liveResult(installations.StateWaitingForCustomer, red), actions.StateWaitingForCustomer, 1},
		{actions.StateRollingOut, liveResult(installations.StateDrifted, probe), actions.StateFailed, 1},
		{actions.StateEnabled, liveResult(installations.StateDrifted, probe, red), actions.StateDrifted, 2},
		{actions.StateWaitingForCustomer, liveResult(installations.StateEnabled), actions.StateEnabled, 0},
	} {
		state, message, reds := decideStage(tc.prev, tc.res)
		if state != tc.state || len(reds) != tc.red || message == "" {
			t.Errorf("%s + %s: %s %q %v", tc.prev, tc.res.State, state, message, reds)
		}
	}
	_, message, reds := decideStage(actions.StateRollingOut, liveResult(installations.StateDrifted, probe))
	if reds[0] != "muster-protected-resource-metadata (runtime): https://agentgateway.example/.well-known/oauth-protected-resource answered 404" || message != "a probe is red: "+reds[0] {
		t.Errorf("the red probe: %q %q", message, reds[0])
	}
	_, message, _ = decideStage(actions.StateRollingOut, liveResult(installations.StateWaitingForCustomer, red))
	if message != "the rollout is done and the customer's move is open: live-model-configs (runtime): Accepted=False: secret not found — the customer's key" {
		t.Errorf("the held drift: %q", message)
	}
}

// The action follows its stages: a failed stage stops the wave with the
// stages after it not started and the open pull requests named; every
// stage enabled ends the action; a stage waiting for the customer holds the
// action and, flipped, hands it back to the wave.
func TestApplyStage(t *testing.T) {
	prs := []actions.PullRequest{{Installation: birch, Repository: "o/r", Number: 1, State: actions.PullRequestMerged}, {Installation: rowan, Repository: "o/r", Number: 2, State: actions.PullRequestOpen}}
	stages := func(states ...string) *actions.Rollout {
		r := &actions.Rollout{}
		for i, s := range states {
			r.Installations = append(r.Installations, actions.InstallationRollout{Name: []string{birch, rowan}[i], State: s, Message: "m"})
		}
		return r
	}
	status := actions.Status{State: actions.StateRollingOut, PullRequests: prs, Rollout: stages(actions.StateFailed, actions.StateRollingOut)}
	applyStage(&status, 0, actions.StateRollingOut)
	if status.State != actions.StateFailed || status.Rollout.Installations[1].State != "" || status.Rollout.FinishedAt == nil || status.Result == nil ||
		status.Result.Message != "the wave stopped at birch (stage 1 of 2): m; 1 pull request(s) stay open (o/r#2)" {
		t.Errorf("the stop: %+v %+v", status.State, status.Result)
	}

	status = actions.Status{State: actions.StateRollingOut, PullRequests: prs, Rollout: stages(actions.StateEnabled, actions.StateRollingOut)}
	applyStage(&status, 0, actions.StateRollingOut)
	if status.State != actions.StateRollingOut || status.Result != nil || status.Rollout.FinishedAt != nil {
		t.Errorf("stage 1 enabled: %+v %+v", status.State, status.Result)
	}
	status.Rollout.Installations[1].State = actions.StateEnabled
	applyStage(&status, 1, actions.StateRollingOut)
	if status.State != actions.StateEnabled || status.Result == nil || status.Result.Message != "every installation of the wave is verified: birch, rowan" || status.Rollout.FinishedAt == nil {
		t.Errorf("both enabled: %+v %+v", status.State, status.Result)
	}

	status = actions.Status{State: actions.StateRollingOut, Rollout: stages(actions.StateWaitingForCustomer)}
	applyStage(&status, 0, actions.StateRollingOut)
	if status.State != actions.StateWaitingForCustomer || status.Result == nil || status.Result.State != actions.StateWaitingForCustomer || status.Rollout.FinishedAt == nil {
		t.Errorf("waiting: %+v %+v", status.State, status.Result)
	}
	status.Rollout.Installations[0].State = actions.StateEnabled
	applyStage(&status, 0, actions.StateWaitingForCustomer)
	if status.State != actions.StateEnabled || status.Result.State != actions.StateEnabled || status.Result.Message != "birch is enabled: m" {
		t.Errorf("flipped: %+v %+v", status.State, status.Result)
	}

	// An enabled action re-read drifted, then back: the result stands.
	final := &actions.Result{State: actions.StateEnabled, Message: "was enabled"}
	status = actions.Status{State: actions.StateEnabled, Result: final, Rollout: stages(actions.StateDrifted)}
	applyStage(&status, 0, actions.StateEnabled)
	if status.State != actions.StateDrifted || status.Result != final {
		t.Errorf("drifted: %+v %+v", status.State, status.Result)
	}
	status.Rollout.Installations[0].State = actions.StateEnabled
	applyStage(&status, 0, actions.StateDrifted)
	if status.State != actions.StateEnabled || status.Result != final {
		t.Errorf("back: %+v %+v", status.State, status.Result)
	}
}

func TestConditionStatus(t *testing.T) {
	for in, want := range map[string]string{notReady: "False", "Ready=Unknown: reconciling": "Unknown", "no Ready condition": "", "": ""} {
		if got := conditionStatus(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

// A customer action is open while the live dimension it holds up is red or
// was not read, and done once that dimension read as defined: the model
// key's action is done when the ModelConfig reads Accepted, whatever the
// definition lists; an action that holds up no dimension stays open.
func TestSortActionsFollowTheLiveRead(t *testing.T) {
	const modelKey, modelConfigs, signOff = "model-key", "live-model-configs", "sign-off"
	key := render.Action{ID: modelKey, State: render.WaitingForCustomer, Dimension: modelConfigs, Note: "create the Secret"}
	unread := render.Action{ID: signOff, State: render.WaitingForCustomer, Note: "no dimension reads it"}
	settled := render.Action{ID: "settled", State: "Done", Dimension: modelConfigs}
	ids := func(as []render.Action) []string {
		var out []string
		for _, a := range as {
			out = append(out, a.ID)
		}
		return out
	}
	for _, tc := range []struct {
		name       string
		mark       verify.Mark
		open, done []string
	}{
		{"accepted", verify.AsDefined, []string{signOff}, []string{modelKey}},
		{"planned beside it", verify.Planned, []string{signOff}, []string{modelKey}},
		{"not accepted", verify.Drifted, []string{modelKey, signOff}, nil},
		{"not read", verify.NotChecked, []string{modelKey, signOff}, nil},
	} {
		res := liveResult(installations.StateEnabled, verify.Dimension{ID: modelConfigs, Kind: definitions.KindLive, Mark: tc.mark})
		open, done := sortActions([]render.Action{key, unread, settled}, res)
		if !reflect.DeepEqual(ids(open), tc.open) || !reflect.DeepEqual(ids(done), tc.done) {
			t.Errorf("%s: open %v done %v, want %v and %v", tc.name, ids(open), ids(done), tc.open, tc.done)
		}
	}
	if open, done := sortActions([]render.Action{key}, liveResult(installations.StateEnabled)); len(open) != 1 || len(done) != 0 {
		t.Errorf("a dimension the result lacks: open %v done %v", ids(open), ids(done))
	}
}

// A stage that failed on a probe is re-read: still red it stays failed with
// the probe named, clean it is enabled. A stage that failed otherwise, or
// whose pull requests are not merged, is over for the watch.
func TestFailedStageIsReReadOnlyAfterAProbe(t *testing.T) {
	probe := verify.Dimension{ID: "muster-protected-resource-metadata", Kind: definitions.KindProbe, Mark: verify.Drifted, Probe: &verify.ProbeResult{Requests: []verify.Request{{URL: "https://agentgateway.example/x", Status: 404}}}}
	if state, message, _ := decideStage(actions.StateFailed, liveResult(installations.StateDrifted, probe)); state != actions.StateFailed || !strings.HasPrefix(message, probeRed) {
		t.Errorf("still red: %s %q", state, message)
	}
	if state, _, _ := decideStage(actions.StateFailed, liveResult(installations.StateEnabled)); state != actions.StateEnabled {
		t.Errorf("clean again: %s", state)
	}
	const repo = "acme/configs"
	merged := &actions.Action{Spec: actions.Spec{Installations: []string{birch, rowan}}, Status: actions.Status{PullRequests: []actions.PullRequest{{Installation: birch, Repository: repo, Number: 1, State: actions.PullRequestMerged}, {Installation: rowan, Repository: repo, Number: 2, State: actions.PullRequestOpen}}}}
	onProbe := &actions.Rollout{Installations: []actions.InstallationRollout{{Name: birch, State: actions.StateFailed, Message: probeRed + "x (runtime): 404"}, {Name: rowan, Message: "not started"}}}
	if i, why := watchedStage(merged, onProbe); i != 0 || why != "" {
		t.Errorf("failed on a probe, merged: %d %q", i, why)
	}
	otherwise := &actions.Rollout{Installations: []actions.InstallationRollout{{Name: birch, State: actions.StateFailed, Message: "withdrawn by alice: over; " + probeRed + "x"}, {Name: rowan}}}
	if i, why := watchedStage(merged, otherwise); i != -1 || !strings.Contains(why, "only a stage that failed on a probe is re-read") {
		t.Errorf("failed otherwise: %d %q", i, why)
	}
	if !failedOnProbe(onProbe.Installations[0]) || failedOnProbe(otherwise.Installations[0]) || failedOnProbe(actions.InstallationRollout{State: actions.StateEnabled, Message: probeRed}) {
		t.Error("failedOnProbe reads the state and the message")
	}
}

// A recovered stage queues the stages after it again and hands the action
// back to the wave — the failed result and the end of the rollout no longer
// stand; the last stage recovered ends the action enabled; a re-read that
// is still red changes nothing.
func TestApplyStageRecoversAFailedStage(t *testing.T) {
	failed := &actions.Result{State: actions.StateFailed, Message: "the wave stopped at birch"}
	stages := func(states ...string) *actions.Rollout {
		r := &actions.Rollout{FinishedAt: now()}
		for i, s := range states {
			r.Installations = append(r.Installations, actions.InstallationRollout{Name: []string{birch, rowan}[i], State: s, Message: "m"})
		}
		return r
	}
	status := actions.Status{State: actions.StateFailed, Result: failed, Rollout: stages(actions.StateEnabled, "")}
	applyStage(&status, 0, actions.StateFailed)
	if status.State != actions.StateRollingOut || status.Result != nil || status.Rollout.FinishedAt != nil || status.Rollout.Installations[1].State != actions.StateRollingOut || status.Rollout.Installations[1].Message != stageQueued {
		t.Errorf("recovered with a stage left: %+v %+v", status.State, status.Rollout.Installations)
	}
	status = actions.Status{State: actions.StateFailed, Result: failed, Rollout: stages(actions.StateEnabled)}
	applyStage(&status, 0, actions.StateFailed)
	if status.State != actions.StateEnabled || status.Result == nil || status.Result.State != actions.StateEnabled || status.Result.Message != birch+" is enabled: m" || status.Rollout.FinishedAt == nil {
		t.Errorf("the last stage recovered: %+v %+v", status.State, status.Result)
	}
	status = actions.Status{State: actions.StateFailed, Result: failed, Rollout: stages(actions.StateFailed, "")}
	applyStage(&status, 0, actions.StateFailed)
	if status.State != actions.StateFailed || status.Result != failed || status.Rollout.FinishedAt == nil || status.Rollout.Installations[1].State != "" {
		t.Errorf("still red: %+v %+v", status.State, status.Rollout.Installations)
	}
}

// The watch re-reads the pull requests with the call's own GitHub token: a
// call without one reads the record as it was and says why in the answer's
// note, and a record fresh enough is not re-read and needs no note.
func TestResyncBeforeWatchNamesWhatItCouldNotReRead(t *testing.T) {
	tl := New(Deps{Actions: struct{ actions.Store }{}})
	a := &actions.Action{Status: actions.Status{State: actions.StateRollingOut}}
	got, note := tl.resyncBeforeWatch(context.Background(), a)
	if got != a || !strings.Contains(note, "not re-read from GitHub") || !strings.Contains(note, errNoGitHubToken.Error()) {
		t.Errorf("without a GitHub token: %v %q", got == a, note)
	}
	synced := time.Now()
	a.Status.SyncedAt = &synced
	if got, note := tl.resyncBeforeWatch(context.Background(), a); got != a || note != "" {
		t.Errorf("a fresh record: %v %q", got == a, note)
	}
}
