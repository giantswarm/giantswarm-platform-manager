package tools

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// conditionTrue is a condition's status when it holds.
const conditionTrue = "True"

// ToolWatchAction is the second live tool: the rollout watch of an
// action, as the person calling. The manager holds no token beyond a call, so
// the watch is a call — from the portal's page, platformctl, an agent — not
// a loop: it reads the Flux objects of the installation rolling out through
// muster with the caller's token, and once every one is Ready — or one
// reports its release failed — runs the probes and decides the stage's
// state.
const ToolWatchAction = "watch_action"

// WatchResult is watch_action's answer: the picture of the stage watched,
// the state it is in after this call, and what follows.
type WatchResult struct {
	Message string          `json:"message"`
	Action  *actions.Action `json:"action,omitempty"`
	// Installation is the stage this call watched; State its state after it.
	Installation string `json:"installation,omitempty"`
	State        string `json:"state,omitempty"`
	// Objects is the rollout picture: the Flux objects the render names on
	// the installation, each with its Ready condition and revision; Ready
	// says whether every one is Ready.
	Objects []actions.RolloutObject `json:"objects"`
	Ready   bool                    `json:"ready"`
	// Verify is the live verify with the anonymous probes: the picture the
	// decision was made from.
	Verify *verify.Result `json:"verify,omitempty"`
	// Red names the dimensions that decided failed or waiting for the customer.
	Red []string `json:"red,omitempty"`
	// Planned names the live dimensions whose only differences are planned
	// changes, with the reasons: leaves a definition released after the
	// action's commit renders and the migration list names — the next
	// reconcile's business, never red.
	Planned []string `json:"planned,omitempty"`
	// Report is the text this call posted into the review's thread, when the
	// stage reached a state; Message says when it could not be posted.
	Report string `json:"report,omitempty"`
	Next   string `json:"next,omitempty"`
}

func (t *Tools) registerWatchTool(s *mcpserver.MCPServer) {
	s.AddTool(mcp.NewTool(ToolWatchAction,
		mcp.WithDescription("The rollout watch of an action whose pull requests are merged, as you — an action rolling out, and one that failed on a red probe, which the watch re-reads once the cause is fixed: green, the stage is enabled and the wave goes on from it; still red, it stays failed, the probe named. It reads the Flux objects the definition names on the installation rolling out — the HelmReleases with their Ready condition and revision — through muster's kubernetes tools with the token muster forwarded, and answers the picture. Once every one is Ready it runs the definition's probes (the live dimensions as you, the anonymous HTTP probes direct) and the stage moves to enabled (all green), waiting for the customer (the customer's own action is the only thing open) or failed (a probe is red, named); a HelmRelease whose last release Flux reports failed (Released=False after a failed install or upgrade, or Stalled=True) never becomes Ready on its own, so the stage is failed on its probe with Flux's verdict named, not rolling out; a value a definition released since the commit renders and the installation lacks is a planned change of that newer definition, listed, never red. The report — pull requests, rollout per object, each probe, the customer actions still open and the ones the installation reads done — goes into the review's thread and onto the Action. Nothing is waited for or hurried: call again while it is rolling out. On a wave the stage in flight is watched; the next stage's pull requests are merged by the actor's "+ToolMergeAction+" once it is enabled. An action waiting for the customer or enabled is re-read: the customer's action done flips it to enabled. Before it reads the installation, the watch reads with your GitHub token whether the stage's pull requests are still on the default branch — every file a merge changed back to its content before the merge is a revert (healthy objects on the previous values read as defined, so the probes cannot tell): the action moves to reverted, naming the reverting commit and its pull request, never enabled, the thread told; its actor withdraws it with "+ToolDenyAction+". A read that fails decides nothing. Anyone signed in may watch; the reads are yours."),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString(ArgAction, mcp.Required(), mcp.Description("The Action's name.")),
	), t.watchActionLive)
}

func (t *Tools) watchActionLive(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(t.watch(ctx, req.GetArguments()))
}

// watchable are the action states the watch takes: rolling out to carry on,
// waiting for the customer and enabled to re-read, failed to re-read a
// stage that failed on a probe (probeRed).
var watchable = []string{actions.StateRollingOut, actions.StateWaitingForCustomer, actions.StateEnabled, actions.StateFailed}

// probeRed opens the message of a stage that failed on a red probe — the
// one failure the watch re-reads: a transient red, an operator's fix, a
// probe an older manager misjudged. A merge refused, a head moved, a pull
// request closed unmerged or a commit that failed is over for good.
const probeRed = "a probe is red: "

// failedOnProbe says whether a stage failed on a red probe.
func failedOnProbe(st actions.InstallationRollout) bool {
	return st.State == actions.StateFailed && strings.HasPrefix(st.Message, probeRed)
}

func (t *Tools) watch(ctx context.Context, args map[string]any) (any, error) {
	token, id, err := t.forwarded(ctx, ToolWatchAction)
	if err != nil {
		return nil, err
	}
	if t.d.Actions == nil {
		return nil, fmt.Errorf("%s: the manager runs without the Action records (no in-cluster ServiceAccount); get_info reports actions.configured", ToolWatchAction)
	}
	name, _ := args[ArgAction].(string)
	if name = strings.TrimSpace(name); name == "" {
		return nil, fmt.Errorf("%s needs %s", ToolWatchAction, ArgAction)
	}
	start := time.Now()
	a, err := t.d.Actions.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolWatchAction, err)
	}
	var note string
	if a, note = t.resyncBeforeWatch(ctx, a); note != "" {
		t.d.Log.Info("action_resync_skipped", identity.LogAttr(ctx), "action", a.Name, "note", note)
	}
	if a.Status.State == actions.StateReverted {
		return nil, fmt.Errorf("%s: action %s is reverted (%s) — the watch reads no stage whose pull requests left the default branch; its actor (%s) withdraws it with %s and the reason%s", ToolWatchAction, a.Name, resultMessage(a), a.Spec.Actor.Login, ToolDenyAction, noteClause(note))
	}
	if !slices.Contains(watchable, a.Status.State) {
		return nil, fmt.Errorf("%s: action %s is %s%s — the watch follows an action rolling out and re-reads one waiting for the customer, enabled or failed on a probe%s", ToolWatchAction, a.Name, a.Status.State, decidedBy(a), noteClause(note))
	}
	def, ok := installations.FindCapability(a.Spec.Capability)
	if !ok {
		return nil, fmt.Errorf("%s: action %s names capability %q, which is not a definition of this version", ToolWatchAction, a.Name, a.Spec.Capability)
	}
	status := a.Status
	status.PullRequests = slices.Clone(status.PullRequests)
	status.Rollout = stagesOf(a)
	i, why := watchedStage(a, status.Rollout)
	if i < 0 {
		return nil, fmt.Errorf("%s: %s%s", ToolWatchAction, why, noteClause(note))
	}
	// Healthy objects on the previous values read as defined: whether the
	// stage's pull requests are still on the default branch is read first.
	if out, err := t.watchReverts(ctx, a, &status, i); out != nil || err != nil {
		if out != nil && note != "" {
			out.Message += " " + note
		}
		return out, err
	}
	st := &status.Rollout.Installations[i]
	inputs := a.InputsOnRecord(st.Name)
	if inputs == nil {
		return nil, fmt.Errorf("%s: action %s holds no inputs on record for %s: nothing to render its probes from", ToolWatchAction, a.Name, st.Name)
	}
	cluster, err := t.d.Live.Cluster(ctx, token, id, st.Name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolWatchAction, err)
	}
	res := verify.CompareLive(ctx, verify.LiveOptions{Definition: def, Installation: st.Name, State: settledState(st.State),
		Inputs: verify.Inputs{Source: "action " + a.Name, Values: inputs, Typed: a.Spec.Inputs}, Cluster: cluster, Probes: t.d.Probes, Person: id.String(), AnonymousProbes: true, Log: t.d.Log})
	res.Caller = id.String()
	objects, ready, failed := rolloutObjects(res)
	prev := st.State
	st.Objects, st.WatchedAt, st.WatchedBy = objects, now(), id.String()
	status.Probes = mergeProbes(status.Probes, st.Name, probesOf(st.Name, res))
	out := WatchResult{Installation: st.Name, Objects: objects, Ready: ready, Verify: &res, Planned: plannedDimensions(res)}
	if prev == actions.StateRollingOut && !ready && !failed {
		st.Message = rolloutMessage(objects)
		out.Next = "call " + ToolWatchAction + " again once Flux has reconciled the installation; nothing is hurried"
	} else {
		state, message, red := decideStage(prev, res)
		out.Red = red
		recovered := ""
		if state != prev {
			st.ReportedAt = nil
			if prev == actions.StateFailed {
				recovered = st.Message
			}
		}
		st.State, st.Message = state, message
		applyStage(&status, i, prev)
		if st.ReportedAt == nil {
			open, done := customerActions(def, inputs, res)
			out.Report = t.report(a, status, i, res, open, done, recovered)
			if told := t.postResult(ctx, a, out.Report); told == "" {
				st.ReportedAt = now()
			} else {
				note = strings.TrimSpace(note + " " + told)
			}
		}
		out.Next = nextAfter(a, status, i)
	}
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, status)
	if err != nil {
		return nil, fmt.Errorf("%s: %s is %s and the action could not record it: %w", ToolWatchAction, st.Name, st.State, err)
	}
	out.Action, out.State = a, st.State
	t.d.Log.Info("action_watch", identity.LogAttr(ctx), "action", a.Name, "installation", st.Name, "ready", ready, "state", st.State, "actionState", a.Status.State, "red", len(out.Red), "reported", st.ReportedAt != nil, "duration_ms", time.Since(start).Milliseconds())
	out.Message = fmt.Sprintf("%s is %s (action %s, %s): %s.", st.Name, st.State, a.Name, a.Status.State, st.Message)
	if note != "" {
		out.Message += " " + note
	}
	return out, nil
}

// resyncBeforeWatch has a's record follow GitHub as the person before the
// watch decides — get_action's own read, with the GitHub token muster puts on
// the call as the bearer next to the forwarded identity — so an action whose
// pull requests were merged outside the manager is watched all the same. It
// answers the record as it is after, and the note for the answer when the
// pull requests could not be re-read: the record then stands as it was.
func (t *Tools) resyncBeforeWatch(ctx context.Context, a *actions.Action) (*actions.Action, string) {
	fresh, err := t.resyncCaller(ctx, a)
	if err != nil {
		return a, fmt.Sprintf("(The pull requests were not re-read from GitHub as you: %v; the watch read the record as it was.)", err)
	}
	return fresh, ""
}

// resultMessage is the action's result in one clause, or its state.
func resultMessage(a *actions.Action) string {
	if a.Status.Result == nil || a.Status.Result.Message == "" {
		return a.Status.State
	}
	return a.Status.Result.Message
}

// noteClause appends a note to a refusal.
func noteClause(note string) string {
	if note == "" {
		return ""
	}
	return " " + note
}

// watchedStage is the index of the stage the watch reads: the one rolling
// out with every pull request merged, one waiting for the customer, or one
// that failed on a probe (re-read: green, the wave goes on from it); with
// every stage enabled the last is re-read. -1 and why when no stage is
// watchable yet.
func watchedStage(a *actions.Action, r *actions.Rollout) (int, string) {
	last := -1
	for i, st := range r.Installations {
		switch st.State {
		case actions.StateEnabled, actions.StateDrifted:
			last = i
		case actions.StateWaitingForCustomer:
			return i, ""
		case actions.StateRollingOut:
			if allMerged(a, st.Name) {
				return i, ""
			}
			return -1, fmt.Sprintf("the pull requests of %s are not merged yet: the actor merges them with %s, and the watch follows", st.Name, ToolMergeAction)
		case actions.StateFailed:
			if failedOnProbe(st) && allMerged(a, st.Name) {
				return i, ""
			}
			return -1, fmt.Sprintf("%s failed (%s); the action is over — only a stage that failed on a probe is re-read", st.Name, st.Message)
		default:
			return -1, fmt.Sprintf("%s is %s: nothing rolls out there yet", st.Name, orNotStarted(st.State))
		}
	}
	if last < 0 {
		return -1, "the action has no stage to watch"
	}
	return last, ""
}

func orNotStarted(state string) string {
	if state == "" {
		return "not started"
	}
	return state
}

// rolloutObjects is the rollout picture in res: the Flux objects the render's
// readiness probes read, whether every one is Ready, and whether one failed
// — a HelmRelease whose last release Flux reports failed will not become
// Ready on its own, so its stage is decided (failed on the red probe, re-read
// once the cause is fixed) instead of rolling out for good.
func rolloutObjects(res verify.Result) (objects []actions.RolloutObject, ready, failed bool) {
	objects = []actions.RolloutObject{}
	ready = true
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Live == nil {
				continue
			}
			for _, c := range d.Live.Checks {
				kind, _, _ := strings.Cut(c.Resource, ".")
				if c.Kind != string(render.HelmReleaseReady) && (c.Kind != string(render.Condition) || !isFluxKind(c.Resource)) {
					continue
				}
				obj := actions.RolloutObject{Kind: kind, Namespace: c.Namespace, Name: c.Name, Revision: c.Revision, Message: c.Message}
				switch c.Mark {
				case verify.AsDefined:
					obj.Ready = conditionTrue
				case verify.Drifted:
					obj.Ready = conditionStatus(c.Message)
				}
				if obj.Ready != conditionTrue {
					ready = false
				}
				failed = failed || c.Failed
				objects = append(objects, obj)
			}
		}
	}
	return objects, ready, failed
}

// isFluxKind says whether a probe's resource is a Flux HelmRelease or Kustomization.
func isFluxKind(resource string) bool {
	kind, group, _ := strings.Cut(resource, ".")
	return (kind == "HelmRelease" || kind == "Kustomization") && (group == "" || strings.HasSuffix(group, "toolkit.fluxcd.io"))
}

// conditionStatus reads the status out of a condition check's message
// ("Ready=False: …" → False); "" when the message carries none.
func conditionStatus(message string) string {
	_, rest, ok := strings.Cut(message, "=")
	if !ok {
		return ""
	}
	status, _, _ := strings.Cut(rest, ":")
	return strings.TrimSpace(status)
}

// rolloutMessage is one line for a stage still rolling out.
func rolloutMessage(objects []actions.RolloutObject) string {
	var notReady []string
	for _, o := range objects {
		if o.Ready != conditionTrue {
			notReady = append(notReady, fmt.Sprintf("%s %s/%s (%s)", o.Kind, o.Namespace, o.Name, orNotRead(o.Message)))
		}
	}
	return fmt.Sprintf("rolling out: %d of %d Ready; not Ready: %s", len(objects)-len(notReady), len(objects), strings.Join(notReady, ", "))
}

func orNotRead(message string) string {
	if message == "" {
		return "not read"
	}
	return message
}

// decideStage is what the live result says about a stage whose rollout is
// done: enabled when nothing is red; waiting for the customer when every red
// dimension is one the customer's own action holds up; otherwise failed for
// a stage that was rolling out or had failed already, drifted for one that
// had reached a state.
func decideStage(prev string, res verify.Result) (state, message string, red []string) {
	red = redDimensions(res)
	switch res.State {
	case installations.StateDrifted:
		if prev == actions.StateRollingOut || prev == actions.StateFailed {
			return actions.StateFailed, probeRed + strings.Join(red, "; "), red
		}
		return actions.StateDrifted, "drifted: " + strings.Join(red, "; "), red
	case installations.StateWaitingForCustomer:
		return actions.StateWaitingForCustomer, "the rollout is done and the customer's move is open: " + strings.Join(red, "; "), red
	}
	return actions.StateEnabled, "verified: " + summaryLine(res.Summary), nil
}

// verifyMoves says whether verify_installation's result moves a stage in
// state: a stage that has reached one. Rolling out is the watch's to carry,
// failed is over.
func verifyMoves(state string) bool {
	return state == actions.StateEnabled || state == actions.StateWaitingForCustomer || state == actions.StateDrifted
}

// settledState is the state a live read of a stage in state starts from —
// what a clean read answers: waiting for the customer and drifted are what a
// previous read saw, and a clean one says enabled.
func settledState(state string) installations.State {
	if verifyMoves(state) {
		return installations.StateEnabled
	}
	return installations.State(state)
}

// applyStage draws the action's state and result from the stages after
// stage i moved from prev: a failed stage stops the wave with the stages
// after it not started; a stage waiting for the customer holds it; every
// stage enabled ends the action; a stage that had failed on a probe and
// reads green again is recovered — the stages after it are queued again,
// the action rolls out once more, the failed result no longer stands. The
// result is the rollout's final word: written when the rollout ends (the
// stage was rolling out), rewritten when a stage waiting for the customer
// flips to enabled or a failed one recovers.
func applyStage(status *actions.Status, i int, prev string) {
	stages := status.Rollout.Installations
	st := stages[i]
	n := len(stages)
	recovering := prev == actions.StateFailed && st.State != actions.StateFailed
	if recovering {
		for j := i + 1; j < n; j++ {
			stages[j].State, stages[j].Message = actions.StateRollingOut, stageQueued
		}
		status.Result, status.Rollout.FinishedAt = nil, nil
	}
	switch st.State {
	case actions.StateFailed:
		if prev == actions.StateFailed {
			// Re-read and still red: the stop stands as recorded.
			return
		}
		for j := i + 1; j < n; j++ {
			stages[j].State, stages[j].Message = "", "not started: the wave stopped at "+st.Name
		}
		msg := fmt.Sprintf("%s failed: %s", st.Name, st.Message)
		if n > 1 {
			msg = fmt.Sprintf("the wave stopped at %s (stage %d of %d): %s", st.Name, i+1, n, st.Message)
		}
		if open := openPRs(status.PullRequests); len(open) > 0 {
			msg += fmt.Sprintf("; %d pull request(s) stay open (%s)", len(open), prList(open))
		}
		status.State, status.Rollout.FinishedAt = actions.StateFailed, now()
		status.Result = &actions.Result{State: actions.StateFailed, Message: msg, At: now()}
	case actions.StateWaitingForCustomer:
		status.State = actions.StateWaitingForCustomer
		if prev == actions.StateRollingOut || recovering {
			msg := fmt.Sprintf("%s waits for the customer: %s", st.Name, st.Message)
			if i+1 < n {
				msg += fmt.Sprintf("; the wave continues once it is enabled (%d stage(s) queued)", n-i-1)
			} else if status.Rollout.FinishedAt == nil {
				status.Rollout.FinishedAt = now()
			}
			status.Result = &actions.Result{State: actions.StateWaitingForCustomer, Message: msg, At: now()}
		}
	case actions.StateEnabled:
		done := true
		for _, s := range stages {
			if s.State != actions.StateEnabled {
				done = false
			}
		}
		flipped := status.Result != nil && status.Result.State == actions.StateWaitingForCustomer
		switch {
		case !done:
			status.State = actions.StateRollingOut
			if flipped {
				status.Result = nil
			}
		case prev == actions.StateRollingOut || recovering || flipped || status.Result == nil:
			msg := fmt.Sprintf("%s is enabled: %s", st.Name, st.Message)
			if n > 1 {
				msg = "every installation of the wave is verified: " + strings.Join(names(stages), ", ")
			}
			status.State = actions.StateEnabled
			status.Result = &actions.Result{State: actions.StateEnabled, Message: msg, At: now()}
			if status.Rollout.FinishedAt == nil {
				status.Rollout.FinishedAt = now()
			}
		default:
			status.State = actions.StateEnabled
		}
	case actions.StateDrifted:
		if n == 1 {
			status.State = actions.StateDrifted
		}
	}
}

func names(stages []actions.InstallationRollout) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		out = append(out, s.Name)
	}
	return out
}

// nextAfter says what follows the stage i's state.
func nextAfter(a *actions.Action, status actions.Status, i int) string {
	st := status.Rollout.Installations[i]
	switch st.State {
	case actions.StateEnabled:
		if i+1 < len(status.Rollout.Installations) {
			return fmt.Sprintf("%s merges the pull requests of %s as the actor (%s); the watch follows", ToolMergeAction, status.Rollout.Installations[i+1].Name, a.Spec.Actor.Login)
		}
		return "nothing: the action is " + status.State
	case actions.StateWaitingForCustomer:
		return "the customer's action; " + ToolWatchAction + " or " + ToolVerifyInstallation + " flips the stage to enabled once it is done"
	case actions.StateFailed:
		return "the action is failed on a probe: fix what it names and " + ToolWatchAction + " re-reads the stage — green, the wave goes on from it; or " + ToolDenyAction + " as the actor withdraws the action with the reason, closing the pull requests left open"
	case actions.StateDrifted:
		return "the installation is off its definition: reconcile it, or " + ToolVerifyInstallation + " once it is back"
	}
	return ""
}

// redDimensions names the drifted dimensions of res — live ones and
// anonymous probes — each with what was seen.
func redDimensions(res verify.Result) []string {
	var red []string
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Mark != verify.Drifted {
				continue
			}
			red = append(red, fmt.Sprintf("%s (%s): %s", d.ID, f.ID, detailOf(d)))
		}
	}
	return red
}

// newerDefinition opens what the watch says of a planned change on the live
// half: the definition renders a leaf the action's commit never wrote and
// the installation never had, which the migration list names.
const newerDefinition = "newer definition, not this action's: "

// plannedDimensions names the dimensions of res whose only differences are
// planned changes — a leaf a definition released after the action's commit
// renders, named by the migration list — each with the reasons.
func plannedDimensions(res verify.Result) []string {
	var out []string
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Mark == verify.Planned {
				out = append(out, fmt.Sprintf("%s (%s): %s%s", d.ID, f.ID, newerDefinition, plannedReasons(d)))
			}
		}
	}
	return out
}

// plannedReasons are the reasons of a dimension's planned changes, each
// once, in the order of its differences.
func plannedReasons(d verify.Dimension) string {
	var reasons []string
	for _, diff := range d.Differences {
		if diff.Planned != "" && !slices.Contains(reasons, diff.Planned) {
			reasons = append(reasons, diff.Planned)
		}
	}
	return strings.Join(reasons, "; ")
}

// detailOf is one line of what a dimension saw: its reason, its planned
// changes with their reasons, its first difference, its first red check
// with the definition's note, or a probe's failed request.
func detailOf(d verify.Dimension) string {
	switch {
	case d.Reason != "":
		return d.Reason
	case d.Mark == verify.Planned && len(d.Differences) > 0:
		return fmt.Sprintf("%d planned change(s): %s", len(d.Differences), plannedReasons(d))
	case len(d.Differences) > 0:
		return fmt.Sprintf("%d difference(s), the first at %s %s", len(d.Differences), d.Differences[0].Object, d.Differences[0].Path)
	case d.Live != nil:
		for _, c := range d.Live.Checks {
			if c.Mark == verify.AsDefined {
				continue
			}
			msg := c.Message
			if c.Note != "" {
				msg += " — " + c.Note
			}
			return msg
		}
		if len(d.Live.Checks) > 0 {
			return d.Live.Checks[0].Message
		}
	case d.Probe != nil:
		for _, r := range d.Probe.Requests {
			if !r.OK {
				return r.URL + " answered " + r.Answer()
			}
		}
	}
	return string(d.Mark)
}

// summaryLine is a result's marks in one clause, drifted first.
func summaryLine(summary map[verify.Mark]int) string {
	var parts []string
	for _, m := range []verify.Mark{verify.Drifted, verify.DiffersByInput, verify.Planned, verify.AsDefined, verify.NotChecked} {
		if n := summary[m]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, m))
		}
	}
	return strings.Join(parts, ", ")
}

// probesOf records the checked dimensions of res — the live ones and the
// anonymous probes — as the action's probe entries for installation.
func probesOf(installation string, res verify.Result) []actions.Probe {
	var out []actions.Probe
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Kind != definitions.KindLive && d.Kind != definitions.KindProbe {
				continue
			}
			if d.Kind == definitions.KindProbe && d.Mark == verify.NotChecked && d.Reason == verify.ReasonRepositorySide {
				continue
			}
			p := actions.Probe{ID: d.ID, Installation: installation, Result: string(d.Mark), At: now()}
			if d.Mark != verify.AsDefined || d.Reason != "" {
				p.Message = detailOf(d)
			}
			out = append(out, p)
		}
	}
	return out
}

// mergeProbes replaces installation's entries of the ids fresh carries with
// fresh, keeping every other entry.
func mergeProbes(existing []actions.Probe, installation string, fresh []actions.Probe) []actions.Probe {
	seen := map[string]bool{}
	for _, p := range fresh {
		seen[p.ID] = true
	}
	kept := existing[:0:0]
	for _, p := range existing {
		if p.Installation != installation || !seen[p.ID] {
			kept = append(kept, p)
		}
	}
	return append(kept, fresh...)
}

// customerActions renders the customer actions of the inputs on record and
// sorts them by what the live half read (sortActions).
func customerActions(def installations.Capability, inputs map[string]any, res verify.Result) (open, done []render.Action) {
	in, err := def.Parse(inputs)
	if err != nil {
		return nil, nil
	}
	rendered, err := def.Render(inputs, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		return nil, nil
	}
	return sortActions(rendered.Actions, res)
}

// sortActions sorts the customer's actions by what the live half read: an
// action is done once the live dimension it holds up read as defined — the
// model key's Secret in place and the ModelConfig Accepted — and open while
// that dimension is red or could not be read; an action that holds up no
// dimension stays open, nothing reads it done. The definition lists the
// actions; the installation says which are done.
func sortActions(rendered []render.Action, res verify.Result) (open, done []render.Action) {
	marks := map[string]verify.Mark{}
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			marks[d.ID] = d.Mark
		}
	}
	for _, a := range rendered {
		if a.State != render.WaitingForCustomer {
			continue
		}
		if m, ok := marks[a.Dimension]; ok && a.Dimension != "" && m != verify.Drifted && m != verify.NotChecked {
			done = append(done, a)
			continue
		}
		open = append(open, a)
	}
	return open, done
}

// reportLimit is what the gateway takes in one result (mrkdwn ≤ 3000).
const reportLimit = 2900

// report is the stage's report for the review's thread: the state, the pull
// requests, the rollout per object, each probe by mark, the customer actions
// still open and the ones the installation reads done, and what follows;
// recovered, when the stage had failed on a probe, is the stop it read
// itself out of. Never a value.
func (t *Tools) report(a *actions.Action, status actions.Status, i int, res verify.Result, open, done []render.Action, recovered string) string {
	stages := status.Rollout.Installations
	st := stages[i]
	var b strings.Builder
	fmt.Fprintf(&b, "*%s* is *%s*", st.Name, st.State)
	if len(stages) > 1 {
		fmt.Fprintf(&b, " (stage %d of %d)", i+1, len(stages))
	}
	fmt.Fprintf(&b, " — watched as %s.", st.WatchedBy)
	if recovered != "" {
		fmt.Fprintf(&b, "\nRecovered: the stage had failed (%s); the re-read finds it as it should be, and the wave goes on from here.", recovered)
	}
	var prs []actions.PullRequest
	for _, k := range a.StagePullRequests(st.Name) {
		prs = append(prs, a.Status.PullRequests[k])
	}
	if len(prs) > 0 {
		fmt.Fprintf(&b, "\nPull requests: %s.", prLinks(prs))
	}
	if len(st.Objects) > 0 {
		b.WriteString("\nRollout: ")
		var lines []string
		for _, o := range st.Objects {
			line := fmt.Sprintf("%s %s/%s Ready=%s", o.Kind, o.Namespace, o.Name, orUnknown(o.Ready))
			if o.Revision != "" {
				line += " (" + o.Revision + ")"
			}
			if o.Ready != conditionTrue && o.Message != "" {
				line += ": " + o.Message
			}
			lines = append(lines, line)
		}
		b.WriteString(strings.Join(lines, " · "))
	}
	green, notChecked := 0, []string{}
	var red, planned []string
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Kind != definitions.KindLive && d.Kind != definitions.KindProbe {
				continue
			}
			switch d.Mark {
			case verify.AsDefined:
				green++
			case verify.NotChecked:
				if d.Reason != verify.ReasonRepositorySide {
					notChecked = append(notChecked, d.ID)
				}
			case verify.Planned:
				planned = append(planned, fmt.Sprintf("🔵 %s (%s): %s%s", d.ID, f.ID, newerDefinition, plannedReasons(d)))
			default:
				red = append(red, fmt.Sprintf("❌ %s (%s): %s", d.ID, f.ID, detailOf(d)))
			}
		}
	}
	fmt.Fprintf(&b, "\nProbes: ✅ %d as defined", green)
	for _, r := range red {
		b.WriteString("\n" + r)
	}
	for _, p := range planned {
		b.WriteString("\n" + p)
	}
	if len(notChecked) > 0 {
		sort.Strings(notChecked)
		fmt.Fprintf(&b, "\n⚪ %d not checked: %s", len(notChecked), strings.Join(notChecked, ", "))
	}
	if len(open) > 0 {
		b.WriteString("\nOpen customer actions:")
		for _, ca := range open {
			fmt.Fprintf(&b, "\n• %s (%s)", ca.Note, ca.ID)
		}
	}
	if len(done) > 0 {
		ids := make([]string, 0, len(done))
		for _, ca := range done {
			ids = append(ids, ca.ID)
		}
		fmt.Fprintf(&b, "\nCustomer actions done: %s.", strings.Join(ids, ", "))
	}
	switch st.State {
	case actions.StateFailed:
		if openPRs := openPRs(status.PullRequests); len(openPRs) > 0 {
			fmt.Fprintf(&b, "\nThe action is failed; %d pull request(s) stay open: %s.", len(openPRs), prLinks(openPRs))
		} else {
			b.WriteString("\nThe action is failed.")
		}
	case actions.StateEnabled:
		if i+1 < len(stages) {
			fmt.Fprintf(&b, "\nNext: %s merges the pull requests of *%s*.", a.Spec.Actor.Login, stages[i+1].Name)
		} else {
			fmt.Fprintf(&b, "\nDone: the action is %s.", status.State)
		}
	}
	text := b.String()
	if len(text) > reportLimit {
		text = text[:reportLimit] + "…"
	}
	return text
}

func orUnknown(ready string) string {
	if ready == "" {
		return "?"
	}
	return ready
}

// stageIndex is the index of installation's stage in r, or -1.
func stageIndex(r *actions.Rollout, installation string) int {
	for i, st := range r.Installations {
		if st.Name == installation {
			return i
		}
	}
	return -1
}
