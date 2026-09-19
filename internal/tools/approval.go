package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/giantswarm/gitops-commit/provenance"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/approvals"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The approval tools: the two buttons of the Team review, called by the
// gateway through muster as the clicking member, and the actor's merge.
const (
	ToolApproveAction = "approve_action"
	ToolDenyAction    = "deny_action"
	ToolMergeAction   = "merge_action"
)

// The approval tools' arguments.
const (
	ArgAction = "action"
	ArgReason = "reason"
)

// Decision is what approve_action and deny_action answer: the message the
// gateway shows the team, and the Action as recorded.
type Decision struct {
	Message string          `json:"message"`
	Action  *actions.Action `json:"action,omitempty"`
}

// MergeResult is merge_action's answer.
type MergeResult struct {
	Message string          `json:"message"`
	Action  *actions.Action `json:"action,omitempty"`
	// Merged are the pull requests merged by this call, in order.
	Merged []actions.PullRequest `json:"merged"`
	// Waiting names the pull request the merge stopped at and why, when it did.
	Waiting string `json:"waiting,omitempty"`
	// Stage is the installation whose pull requests this call merged.
	Stage string `json:"stage,omitempty"`
}

func (t *Tools) registerApprovalTools(s *mcpserver.MCPServer) {
	s.AddTool(mcp.NewTool(ToolApproveAction,
		mcp.WithDescription("The Approve button of an action's Team review, called as the clicking member: submits an approving review on every pull request of the action with your own GitHub token through the App "+ToolPrefix+" and records the decision on the Action. The actor of the action is refused — a second person decides. The merge follows as the actor ("+ToolMergeAction+")."),
		mcp.WithIdempotentHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString(ArgAction, mcp.Required(), mcp.Description("The Action's name (the action id of the review).")),
	), t.approveAction)
	s.AddTool(mcp.NewTool(ToolDenyAction,
		mcp.WithDescription("The Deny button of an action's Team review, called as the clicking member (the actor included: a denial withdraws the action): records the reason on the Action, closes every pull request of the action as you and moves the Action to denied. A failed action is denied too: any pull request its failure left open is closed, the reason recorded, the action stays failed."),
		mcp.WithIdempotentHintAnnotation(true), mcp.WithDestructiveHintAnnotation(true),
		mcp.WithString(ArgAction, mcp.Required(), mcp.Description("The Action's name.")),
		mcp.WithString(ArgReason, mcp.Required(), mcp.Description("Why the action is denied; recorded on the Action.")),
	), t.denyAction)
	s.AddTool(mcp.NewTool(ToolMergeAction,
		mcp.WithDescription("Merge the pull requests of an approved action as its actor, in dependency order, each once its checks are green (WRITES as you, with your own GitHub token through the App "+ToolPrefix+"); the Action moves to rolling out and the outcome is posted into the review's thread. Only the actor merges; a call before the approval answers what the action waits for, posts the review when none is up, and re-posts it when the gateway no longer holds it. A pull request whose checks are pending stops the call: call again."),
		mcp.WithIdempotentHintAnnotation(true), mcp.WithDestructiveHintAnnotation(false),
		mcp.WithString(ArgAction, mcp.Required(), mcp.Description("The Action's name.")),
	), t.mergeAction)
}

// approvalTool is the gateway's call name of one of the manager's tools through muster.
func approvalTool(name string) string { return "x_" + ToolPrefix + "_" + name }

// pendingAction loads the action args name and refuses one that is not
// pending approval, a call without a caller, and a manager without the records.
func (t *Tools) pendingAction(ctx context.Context, tool string, args map[string]any) (*actions.Action, *identity.Identity, error) {
	return t.loadAction(ctx, tool, args, actions.StatePendingApproval)
}

// loadAction loads the action args name and refuses one whose state is not
// among states, a call without a caller, and a manager without the records.
func (t *Tools) loadAction(ctx context.Context, tool string, args map[string]any, states ...string) (*actions.Action, *identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return nil, nil, fmt.Errorf("%s needs a caller: the request carried no identity to decide as", tool)
	}
	if t.d.Actions == nil || t.d.Remote == nil {
		return nil, nil, fmt.Errorf("%s: the manager runs without the Action records or a git remote (chart actions.enabled); nothing to decide on", tool)
	}
	name, _ := args[ArgAction].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, fmt.Errorf("%s needs %s", tool, ArgAction)
	}
	a, err := t.d.Actions.Get(ctx, name)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tool, err)
	}
	if !slices.Contains(states, a.Status.State) {
		return nil, nil, fmt.Errorf("%s: action %s is %s, not %s%s", tool, a.Name, a.Status.State, strings.Join(states, " or "), decidedBy(a))
	}
	return a, id, nil
}

func decidedBy(a *actions.Action) string {
	if a.Status.Approval == nil || a.Status.Approval.Decision == "" {
		return ""
	}
	return fmt.Sprintf(" (%s by %s)", a.Status.Approval.Decision, a.Status.Approval.DecidedBy)
}

func (t *Tools) approveAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(t.approve(ctx, req.GetArguments()))
}

func (t *Tools) approve(ctx context.Context, args map[string]any) (any, error) {
	a, id, err := t.pendingAction(ctx, ToolApproveAction, args)
	if err != nil {
		return nil, err
	}
	if id.Login == a.Spec.Actor.Login {
		t.d.Log.Info("action_approval_actor_refused", identity.LogAttr(ctx), "action", a.Name)
		return nil, fmt.Errorf("%s: action %s is yours (%s): a second person of the team decides; Deny withdraws it", ToolApproveAction, a.Name, id.Login)
	}
	if a.Status.Approval != nil && a.Status.Approval.Decision != "" {
		return nil, fmt.Errorf("%s: action %s is already %s by %s", ToolApproveAction, a.Name, a.Status.Approval.Decision, a.Status.Approval.DecidedBy)
	}
	token, _ := identity.TokenFromContext(ctx)
	remote, err := t.d.Remote(token)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolApproveAction, err)
	}
	body := fmt.Sprintf("Approved through the Team review of action %s, as %s.", a.Name, id.Login)
	for _, pr := range a.Status.PullRequests {
		if pr.State != actions.PullRequestOpen {
			continue
		}
		cpr, err := commitPR(pr)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ToolApproveAction, err)
		}
		if err := remote.Approve(ctx, cpr, body); err != nil {
			return nil, fmt.Errorf("%s: approving review on %s#%d as %s: %w", ToolApproveAction, pr.Repository, pr.Number, id.Login, remoteError(pr.Repository, err))
		}
	}
	status := a.Status
	approval := *approvalOf(&status)
	approval.Decision, approval.DecidedBy, approval.At = actions.DecisionApproved, id.Login, now()
	status.Approval = &approval
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, status)
	if err != nil {
		return nil, fmt.Errorf("%s: the approving reviews are submitted and the action could not record the decision: %w", ToolApproveAction, err)
	}
	t.d.Log.Info("action_approved", identity.LogAttr(ctx), "action", a.Name, "pullRequests", len(a.Status.PullRequests))
	return Decision{Action: a, Message: fmt.Sprintf("%s approved action %s: approving reviews submitted on %d pull request(s) (%s); %s merges them as the actor with %s once the checks are green.",
		id.Login, a.Name, len(a.Status.PullRequests), prList(a.Status.PullRequests), a.Spec.Actor.Login, ToolMergeAction)}, nil
}

func (t *Tools) denyAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(t.deny(ctx, req.GetArguments()))
}

// deny withdraws an action pending approval: its pull requests are closed as
// the member and it moves to denied. A failed action is denied too — a
// commit that failed after opening pull requests closes them itself, and the
// denial closes whatever it could not (the remote refused a close) and
// records the reason; the action stays failed, the failure being its result.
func (t *Tools) deny(ctx context.Context, args map[string]any) (any, error) {
	a, id, err := t.loadAction(ctx, ToolDenyAction, args, actions.StatePendingApproval, actions.StateFailed)
	if err != nil {
		return nil, err
	}
	if a.Status.Approval != nil && a.Status.Approval.Decision != "" {
		return nil, fmt.Errorf("%s: action %s is already %s by %s", ToolDenyAction, a.Name, a.Status.Approval.Decision, a.Status.Approval.DecidedBy)
	}
	reason, _ := args[ArgReason].(string)
	if reason = strings.TrimSpace(reason); reason == "" {
		return nil, fmt.Errorf("%s needs %s: the denial is recorded with it", ToolDenyAction, ArgReason)
	}
	token, _ := identity.TokenFromContext(ctx)
	remote, err := t.d.Remote(token)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolDenyAction, err)
	}
	status := a.Status
	open := len(openPRs(status.PullRequests))
	if left := closeOpen(ctx, remote, status.PullRequests); len(left) > 0 {
		return nil, fmt.Errorf("%s: closing as %s: %s", ToolDenyAction, id.Login, strings.Join(left, "; "))
	}
	approval := *approvalOf(&status)
	approval.Decision, approval.DecidedBy, approval.Reason, approval.At = actions.DecisionDenied, id.Login, reason, now()
	status.Approval = &approval
	if status.State == actions.StateFailed {
		status.Result.Message += fmt.Sprintf("; denied by %s: %s (%d pull request(s) closed)", id.Login, reason, open)
	} else {
		status.State = actions.StateDenied
		status.Result = &actions.Result{State: actions.StateDenied, Message: fmt.Sprintf("denied by %s: %s", id.Login, reason), At: now()}
	}
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, status)
	if err != nil {
		return nil, fmt.Errorf("%s: the pull requests are closed and the action could not record the denial: %w", ToolDenyAction, err)
	}
	t.d.Log.Info("action_denied", identity.LogAttr(ctx), "action", a.Name, "state", a.Status.State, "pullRequests", len(a.Status.PullRequests), "closed", open)
	return Decision{Action: a, Message: fmt.Sprintf("%s denied action %s (%s): %s — %d pull request(s) closed (%s).", id.Login, a.Name, a.Status.State, reason, open, prList(a.Status.PullRequests))}, nil
}

func (t *Tools) mergeAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(t.merge(ctx, req.GetArguments()))
}

func (t *Tools) merge(ctx context.Context, args map[string]any) (any, error) {
	a, id, err := t.loadAction(ctx, ToolMergeAction, args, actions.StatePendingApproval, actions.StateRollingOut, actions.StateWaitingForCustomer)
	if err != nil {
		return nil, err
	}
	if id.Login != a.Spec.Actor.Login {
		return nil, fmt.Errorf("%s: the pull requests of action %s are merged as its actor (%s), and you are %s", ToolMergeAction, a.Name, a.Spec.Actor.Login, id.Login)
	}
	if a.Status.Approval == nil || a.Status.Approval.Decision != actions.DecisionApproved {
		return t.awaitApproval(ctx, a)
	}
	token, _ := identity.TokenFromContext(ctx)
	remote, err := t.d.Remote(token)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ToolMergeAction, err)
	}
	status := a.Status
	status.Rollout = stagesOf(a)
	res := MergeResult{Merged: []actions.PullRequest{}}
	stages := status.Rollout.Installations
	for i := range stages {
		st := &stages[i]
		if st.State == actions.StateEnabled {
			continue
		}
		// The stage in flight: merged, and not enabled yet — the watch
		// carries it there; the next stage's pull requests wait (a red probe
		// stops the wave with them open).
		if allMerged(a, st.Name) {
			return nil, stageNotEnabled(a, *st)
		}
		// This stage's pull requests are merged now, in order, each once green.
		for _, k := range a.StagePullRequests(st.Name) {
			pr := status.PullRequests[k]
			if pr.State == actions.PullRequestMerged {
				continue
			}
			cpr, err := commitPR(pr)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", ToolMergeAction, err)
			}
			err = commit.Merge(ctx, remote, cpr, true)
			switch {
			case err == nil:
				status.PullRequests[k].State = actions.PullRequestMerged
				res.Merged = append(res.Merged, status.PullRequests[k])
				continue
			case errors.Is(err, commit.ErrChecksPending):
				res.Waiting = fmt.Sprintf("%s#%d: checks pending", pr.Repository, pr.Number)
			case errors.Is(err, commit.ErrChecksFailed):
				res.Waiting = fmt.Sprintf("%s#%d: a check failed", pr.Repository, pr.Number)
			case errors.Is(err, commit.ErrHeadMoved):
				return nil, t.fail(ctx, ToolMergeAction, a, nil, status.PullRequests, fmt.Errorf("%s#%d: %w — the approval covered the head the pull request was opened with; open a new action", pr.Repository, pr.Number, err))
			default:
				if _, err2 := t.d.Actions.UpdateStatus(ctx, a.Name, status); err2 != nil {
					return nil, fmt.Errorf("%s: %w; and the action could not record the pull requests merged so far: %v", ToolMergeAction, remoteError(pr.Repository, err), err2)
				}
				return nil, fmt.Errorf("%s: merging %s#%d as %s: %w", ToolMergeAction, pr.Repository, pr.Number, id.Login, remoteError(pr.Repository, err))
			}
			break
		}
		if res.Waiting == "" {
			st.State, st.Message = actions.StateRollingOut, "the pull requests are merged; Flux reconciles the installation — "+ToolWatchAction+" reads its rollout and runs the probes"
			for j := i + 1; j < len(stages); j++ {
				stages[j].State, stages[j].Message = actions.StateRollingOut, stageQueued
			}
			status.State = actions.StateRollingOut
			if status.Rollout.StartedAt == nil {
				status.Rollout.StartedAt = now()
			}
			res.Stage = st.Name
		}
		break
	}
	if res.Waiting == "" && res.Stage == "" {
		return nil, fmt.Errorf("%s: every stage of action %s is enabled; nothing is left to merge", ToolMergeAction, a.Name)
	}
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, status)
	if err != nil {
		return nil, fmt.Errorf("%s: %d pull request(s) merged (%s) and the action could not record it: %w", ToolMergeAction, len(res.Merged), prList(res.Merged), err)
	}
	res.Action = a
	t.d.Log.Info("action_merge", identity.LogAttr(ctx), "action", a.Name, "state", a.Status.State, "merged", len(res.Merged), "waiting", res.Waiting, "stage", res.Stage)
	if res.Waiting != "" {
		res.Message = fmt.Sprintf("%d pull request(s) merged as %s (%s); the merge stopped at %s — call %s again once it is green.", len(res.Merged), id.Login, prList(res.Merged), res.Waiting, ToolMergeAction)
		return res, nil
	}
	res.Message = fmt.Sprintf("the pull requests of %s are merged as %s (%s); %s is rolling out — %s on the live registration (%s) reads its rollout and runs the probes as you, and carries it to enabled%s.", res.Stage, id.Login, prList(res.Merged), res.Stage, ToolWatchAction, LiveToolPrefix, nextStage(a, res.Stage))
	if note := t.postResult(ctx, a, fmt.Sprintf("Merged as %s: %s. *%s* is rolling out — the rollout and the probes follow here.", id.Login, prLinks(res.Merged), res.Stage)); note != "" {
		res.Message += " " + note
	}
	return res, nil
}

// stageNotEnabled is merge_action's refusal at a stage whose pull requests
// are merged and whose watch has not said enabled: the next stage waits.
func stageNotEnabled(a *actions.Action, st actions.InstallationRollout) error {
	next := ""
	if i := stageIndex(a.Status.Rollout, st.Name); i >= 0 && i+1 < len(a.Spec.Installations) {
		next = fmt.Sprintf("; the pull requests of %s are merged once it is", a.Spec.Installations[i+1])
	}
	switch st.State {
	case actions.StateWaitingForCustomer:
		return fmt.Errorf("%s: %s waits for the customer (%s) — %s flips it to enabled once the customer's action is done%s", ToolMergeAction, st.Name, st.Message, ToolWatchAction, next)
	default:
		return fmt.Errorf("%s: %s is %s — its pull requests are merged, and %s on the live registration (%s) reads its rollout and runs the probes as you, carrying it to enabled%s", ToolMergeAction, st.Name, st.State, ToolWatchAction, LiveToolPrefix, next)
	}
}

// stagesOf is the action's rollout with one entry per installation of the
// wave, in order — the entries as recorded, the missing ones in the action's
// state (pending approval before the first merge).
func stagesOf(a *actions.Action) *actions.Rollout {
	r := &actions.Rollout{}
	if a.Status.Rollout != nil {
		r.StartedAt, r.FinishedAt = a.Status.Rollout.StartedAt, a.Status.Rollout.FinishedAt
	}
	for _, name := range a.Spec.Installations {
		r.Installations = append(r.Installations, actions.InstallationRollout{Name: name, State: a.Status.State})
	}
	if a.Status.Rollout != nil {
		for _, rec := range a.Status.Rollout.Installations {
			for i := range r.Installations {
				if r.Installations[i].Name == rec.Name {
					r.Installations[i] = rec
				}
			}
		}
	}
	return r
}

// allMerged says whether every pull request of the stage is merged.
func allMerged(a *actions.Action, installation string) bool {
	for _, k := range a.StagePullRequests(installation) {
		if a.Status.PullRequests[k].State != actions.PullRequestMerged {
			return false
		}
	}
	return true
}

func openPRs(prs []actions.PullRequest) []actions.PullRequest {
	var open []actions.PullRequest
	for _, pr := range prs {
		if pr.State == actions.PullRequestOpen {
			open = append(open, pr)
		}
	}
	return open
}

// nextStage names the stage after installation, or "".
func nextStage(a *actions.Action, installation string) string {
	for i, n := range a.Spec.Installations {
		if n == installation && i+1 < len(a.Spec.Installations) {
			return "; enabled, the pull requests of " + a.Spec.Installations[i+1] + " are merged next"
		}
	}
	return ""
}

// awaitApproval is merge_action before the decision: the review is posted
// when none is up, re-posted when the gateway no longer holds it, and the
// answer says what the action waits for.
func (t *Tools) awaitApproval(ctx context.Context, a *actions.Action) (any, error) {
	res := MergeResult{Action: a, Merged: []actions.PullRequest{}}
	if a.Status.Approval == nil || a.Status.Approval.ReviewID == "" {
		a, err := t.askApproval(ctx, a, ToolMergeAction)
		if err != nil {
			return nil, err
		}
		res.Action = a
		res.Message = fmt.Sprintf("action %s waits for the team's approval: the review is posted now (%s in %s); nothing is merged before it.", a.Name, a.Status.Approval.ReviewID, a.Status.Approval.Channel)
		return res, nil
	}
	err := t.approvals.Result(ctx, a.Status.Approval.ReviewID, fmt.Sprintf("%s asked to merge; the action still waits for the team's decision.", a.Spec.Actor.Login), "")
	switch {
	case err == nil:
		res.Message = fmt.Sprintf("action %s waits for the team's approval (review %s in %s); nothing is merged before it.", a.Name, a.Status.Approval.ReviewID, a.Status.Approval.Channel)
	case errors.Is(err, approvals.ErrGone):
		t.d.Log.Info("action_review_gone", identity.LogAttr(ctx), "action", a.Name, "review", a.Status.Approval.ReviewID)
		a, err := t.askApproval(ctx, a, ToolMergeAction)
		if err != nil {
			return nil, err
		}
		res.Action = a
		res.Message = fmt.Sprintf("action %s waits for the team's approval: the gateway no longer held its review, so it is re-posted (%s in %s).", a.Name, a.Status.Approval.ReviewID, a.Status.Approval.Channel)
	default:
		return nil, fmt.Errorf("%s: action %s waits for the team's approval, and the gateway could not be reached to confirm its review: %w", ToolMergeAction, a.Name, err)
	}
	return res, nil
}

// askApproval posts the action's review to the team's channel (and the notice
// channel for a customer installation) and records the receipt on the Action.
func (t *Tools) askApproval(ctx context.Context, a *actions.Action, tool string) (*actions.Action, error) {
	if t.approvals == nil {
		return nil, fmt.Errorf("%s: no approval channel is configured (chart approvals.gatewayURL); action %s cannot be approved and nothing is merged", tool, a.Name)
	}
	review := approvals.Review{Actor: a.Spec.Actor.Email, Text: reviewText(a),
		Approve: approvals.Tool{Tool: approvalTool(ToolApproveAction), Arguments: map[string]any{ArgAction: a.Name}},
		Deny:    &approvals.Tool{Tool: approvalTool(ToolDenyAction), Arguments: map[string]any{ArgAction: a.Name}}}
	for _, pr := range a.Status.PullRequests {
		review.PullRequests = append(review.PullRequests, pr.URL)
	}
	if a.Spec.Customer {
		review.NoticeChannel = t.approvals.Config().NoticeChannel
	}
	receipt, err := t.approvals.Post(ctx, review)
	if err != nil {
		return nil, fmt.Errorf("%s: the review of action %s could not be posted: %w", tool, a.Name, err)
	}
	status := a.Status
	approval := *approvalOf(&status)
	approval.Channel, approval.ReviewID, approval.PostedAt = receipt.Channel, receipt.ID, now()
	if receipt.NoticeTS != "" {
		approval.NoticeChannel = review.NoticeChannel
	}
	status.Approval = &approval
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, status)
	if err != nil {
		return nil, fmt.Errorf("%s: the review of action %s is posted (%s) and the action could not record it: %w", tool, a.Name, receipt.ID, err)
	}
	t.d.Log.Info("action_review_posted", identity.LogAttr(ctx), "action", a.Name, "review", receipt.ID, "channel", receipt.Channel, "notice", receipt.NoticeTS != "")
	return a, nil
}

// postResult posts text into the review's thread; the answer's note when it
// could not (the record stands, the thread is telemetry).
func (t *Tools) postResult(ctx context.Context, a *actions.Action, text string) string {
	if t.approvals == nil || a.Status.Approval == nil || a.Status.Approval.ReviewID == "" {
		return ""
	}
	if err := t.approvals.Result(ctx, a.Status.Approval.ReviewID, text, ""); err != nil {
		t.d.Log.Info("action_result_not_posted", "action", a.Name, "review", a.Status.Approval.ReviewID, "error", err.Error())
		return fmt.Sprintf("(The review's thread could not be told: %v.)", err)
	}
	t.d.Log.Info("action_result_posted", "action", a.Name, "review", a.Status.Approval.ReviewID)
	return ""
}

// reviewText is the review's mrkdwn: the actor, the target, the capability
// and the change by count and name — never a value.
func reviewText(a *actions.Action) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s* asks to %s *%s* on *%s*", a.Spec.Actor.Login, a.Spec.Kind, a.Spec.Capability, strings.Join(a.Spec.Installations, ", "))
	if a.Spec.Customer {
		b.WriteString(" (a customer installation)")
	}
	if len(a.Spec.Installations) > 1 {
		b.WriteString(" (a wave, in this order)")
	}
	fmt.Fprintf(&b, ": %d pull request(s) in dependency order", len(a.Status.PullRequests))
	b.WriteString(skippedClause(a.Spec.Skipped))
	if a.Spec.Change != "" {
		b.WriteString(", " + a.Spec.Change)
	}
	b.WriteString(". Approve submits your approving review on every pull request; Deny closes them with your reason.")
	return b.String()
}

// changeSummary is the plan's change in one clause: files by change and the
// generated secrets by name.
func changeSummary(p plan.Installation) string {
	parts := []string{}
	for _, c := range []plan.Change{plan.ChangeCreate, plan.ChangeUpdate} {
		if n := p.Diff[c]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d file(s) to %s", n, c))
		}
	}
	if len(p.GeneratedSecrets) > 0 {
		names := make([]string, 0, len(p.GeneratedSecrets))
		for _, g := range p.GeneratedSecrets {
			names = append(names, g.Name)
		}
		parts = append(parts, "generated secrets "+strings.Join(names, ", "))
	}
	if rotating := p.Rotating(); len(rotating) > 0 {
		parts = append(parts, "rotates "+strings.Join(rotating, ", ")+" (a new value over the one on record; both sides roll)")
	}
	return strings.Join(parts, ", ")
}

func approvalOf(s *actions.Status) *actions.Approval {
	if s.Approval == nil {
		return &actions.Approval{}
	}
	return s.Approval
}

// commitPR is the pull request as gitops-commit addresses it, from the record.
func commitPR(pr actions.PullRequest) (commit.PullRequest, error) {
	owner, repo, err := gh.SplitRepo(pr.Repository)
	if err != nil {
		return commit.PullRequest{}, err
	}
	return commit.PullRequest{Repository: provenance.Repository{Owner: owner, Name: repo}, Number: pr.Number, URL: pr.URL, Head: pr.Head, HeadSHA: pr.HeadSHA}, nil
}

func prLinks(prs []actions.PullRequest) string {
	names := make([]string, 0, len(prs))
	for _, pr := range prs {
		names = append(names, fmt.Sprintf("<%s|%s#%d>", pr.URL, pr.Repository, pr.Number))
	}
	return strings.Join(names, ", ")
}
