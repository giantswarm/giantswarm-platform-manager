package e2e

// The wave: reconcile_capability in mode commit over a set of three
// lab-shaped installations, one without the opt-in. The dry run and the
// review list two targets in the wave's order and one skipped; one Action
// carries every installation's state; the pull requests are merged one
// installation after the other, each verified before the next; a red probe
// on the installation rolling out stops the wave with the next one's pull
// requests open, and the summary names the stop.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"

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

func TestWaveOverASetStopsAtARedProbe(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)

	// The dry run: two targets in the wave's order, one skipped.
	text, isErr := call(t, aliceC, tools.ToolReconcileCapability, waveArgs(map[string]any{tools.ArgDryRun: true}))
	var dry tools.CapabilityResult
	if isErr || json.Unmarshal([]byte(text), &dry) != nil || strings.Join(dry.Order, ",") != birch+","+rowan || len(dry.Skipped) != 1 || dry.Skipped[0].Name != alder || dry.Skipped[0].Reason != tools.SkippedNotOptedIn {
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
	if review := fmt.Sprint(st.gateway.posted()); len(st.gateway.posted()) != 1 || !strings.Contains(review, birch+", "+rowan) || !strings.Contains(review, alder+" ("+tools.SkippedNotOptedIn+")") {
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
	if isErr || m.Stage != birch || len(m.Merged) == 0 || m.Action.Status.State != actions.StateRollingOut || !strings.Contains(m.Message, rowan) {
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

	// birch's probe answers wrong: the verify that gates stage 2 is red, the
	// wave stops before rowan with rowan's pull requests open, the summary
	// names where and why.
	text, isErr = call(t, aliceC, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: birch})
	var v verify.Result
	if isErr || json.Unmarshal([]byte(text), &v) != nil {
		t.Fatalf("verify birch: %v %s", isErr, text)
	}
	probe := dimension(t, feature(t, v, "tool-access"), "muster-protected-resource-metadata")
	u, err := url.Parse(probe.Probe.Requests[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	st.probes.answer(u.Host+u.Path, http.StatusNotFound)
	m, text, isErr = mergeCall(t, aliceC, a.Name)
	if isErr || m.Stopped != birch || len(m.Merged) != 0 || m.Action.Status.State != actions.StateFailed || m.Action.Status.Result == nil ||
		!strings.Contains(m.Action.Status.Result.Message, "stopped at "+birch) || !strings.Contains(m.Action.Status.Result.Message, "muster-protected-resource-metadata") || !strings.Contains(m.Action.Status.Result.Message, "stay open") {
		t.Fatalf("the stop: %v %s", isErr, text)
	}
	if r := m.Action.Status.Rollout; r.FinishedAt == nil || r.Installations[0].State != actions.StateFailed || r.Installations[1].State != "" || !strings.Contains(r.Installations[1].Message, "stopped at "+birch) {
		t.Fatalf("stages after the stop: %+v", r.Installations)
	}
	for _, pr := range m.Action.Status.PullRequests {
		if (pr.Installation == rowan) != (pr.State == actions.PullRequestOpen) {
			t.Fatalf("after the stop: %+v", pr)
		}
	}
	for _, pr := range st.remote.PullRequests() {
		if strings.HasPrefix(pr.Head, "platform/"+a.Name+"/"+rowan) && (pr.Merged || pr.Closed) {
			t.Fatalf("rowan's pull request touched: %+v", pr)
		}
	}
	if n := len(m.Action.Status.Probes); n == 0 || m.Action.Status.Probes[0].Installation != birch {
		t.Fatalf("probes on record: %+v", m.Action.Status.Probes)
	}
	li, text, isErr = listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []string{birch, rowan}})
	if isErr || find(t, li, birch).Capabilities[0].State != installations.StateFailed || find(t, li, rowan).Capabilities[0].State != installations.StateNotEnabled {
		t.Fatalf("list_installations after the stop: %v %s", isErr, text)
	}
	if _, text, isErr := mergeCall(t, aliceC, a.Name); !isErr || !strings.Contains(text, actions.StateFailed) {
		t.Fatalf("merge a stopped wave: %v %s", isErr, text)
	}
}
