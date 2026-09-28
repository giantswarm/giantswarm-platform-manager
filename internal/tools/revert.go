package tools

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
)

// The revert check: whether the pull requests an action merged are still on
// their repositories' default branch. A revert leaves every object healthy
// on the previous values, so the live half cannot tell it; the watch runs the
// check before it decides a stage, and deny_action before the actor
// withdraws an action that reads its change live. A pull request is reverted
// when every file its merge commit changed is back to its blob before the
// merge — not when a later change rewrote a file, which leaves it neither as
// the merge wrote it nor as it was. The merge's change is read once per
// commit (gh.ChangeOf); each check costs the repository's tree at HEAD, as
// the person, and a revert found two requests more to name it.

// gitHubAs is the caller's GitHub client: the token muster puts on the call
// as the bearer. The revert check reads with it; without one it is refused.
func (t *Tools) gitHubAs(ctx context.Context, tool string) (*gh.Client, error) {
	token, ok := identity.TokenFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("%s: %w: whether the pull requests are still on the default branch is read as you", tool, errNoGitHubToken)
	}
	return t.person(token)
}

// revertsOf reads, as the person c stands for, the merged pull requests at
// idx of status that carry no revert on record, and answers the reverted ones
// by index. A merge commit not on record yet (the merge's own record, before
// the next resync) is read from the pull request first and recorded on
// status.
func revertsOf(ctx context.Context, c *gh.Client, status *actions.Status, idx []int) (map[int]*actions.Revert, error) {
	found := map[int]*actions.Revert{}
	for _, k := range idx {
		pr := &status.PullRequests[k]
		if pr.State != actions.PullRequestMerged || pr.Revert != nil {
			continue
		}
		owner, repo, err := gh.SplitRepo(pr.Repository)
		if err != nil {
			return nil, err
		}
		if pr.MergeCommit == "" {
			st, err := gh.PullRequest(ctx, c, owner, repo, pr.Number)
			if err != nil {
				return nil, err
			}
			if st.MergeCommit == "" {
				return nil, fmt.Errorf("%s#%d: GitHub names no merge commit", pr.Repository, pr.Number)
			}
			pr.MergeCommit = st.MergeCommit
		}
		rv, err := revertOf(ctx, c, owner, repo, pr.MergeCommit)
		if err != nil {
			return nil, fmt.Errorf("%s#%d: %w", pr.Repository, pr.Number, err)
		}
		if rv != nil {
			found[k] = rv
		}
	}
	return found, nil
}

// revertOf is the revert of the merge commit sha of owner/repo — the newest
// commit that changed the first of its files, with its pull request — or nil
// while the merge stands.
func revertOf(ctx context.Context, c *gh.Client, owner, repo, sha string) (*actions.Revert, error) {
	ch, err := gh.ChangeOf(ctx, c, owner, repo, sha)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(ch.Files))
	for _, f := range ch.Files {
		paths = append(paths, f.Path)
	}
	blobs, err := gh.Blobs(ctx, c, owner, repo, paths)
	if err != nil {
		return nil, err
	}
	if !reverted(ch, blobs) {
		return nil, nil
	}
	last, err := gh.LastChange(ctx, c, owner, repo, ch.Files[0].Path)
	if err != nil {
		return nil, err
	}
	return &actions.Revert{Commit: last.SHA, URL: last.URL, PullRequest: last.PullRequest, PullRequestURL: last.PullRequestURL, At: now()}, nil
}

// reverted says whether every file the merge changed carries its blob from
// before the merge on the default branch now (blobs, "" where absent).
func reverted(ch gh.MergeChange, blobs map[string]string) bool {
	for _, f := range ch.Files {
		if blobs[f.Path] != f.Before {
			return false
		}
	}
	return len(ch.Files) > 0
}

// markReverted records the reverts found on status: each pull request's
// revert, every stage with a reverted pull request reverted — the action
// no longer reads enabled there — and the action reverted, its result
// naming them. It answers the text for the review's thread.
func markReverted(status *actions.Status, a *actions.Action, found map[int]*actions.Revert) string {
	if status.Rollout == nil {
		status.Rollout = stagesOf(a)
	}
	var all, stages []string
	for i := range status.Rollout.Installations {
		st := &status.Rollout.Installations[i]
		var what, links []string
		for _, k := range a.StagePullRequests(st.Name) {
			rv, ok := found[k]
			if !ok {
				continue
			}
			pr := &status.PullRequests[k]
			pr.Revert = rv
			what = append(what, fmt.Sprintf("%s#%d reverted by %s", pr.Repository, pr.Number, revertName(pr.Repository, *rv)))
			links = append(links, fmt.Sprintf("<%s|%s#%d> reverted by %s", pr.URL, pr.Repository, pr.Number, revertLink(pr.Repository, *rv)))
		}
		if len(what) == 0 {
			continue
		}
		st.State, st.Message, st.ReportedAt = actions.StateReverted, "reverted on the default branch: "+strings.Join(what, "; "), nil
		all = append(all, what...)
		stages = append(stages, fmt.Sprintf("*%s*: %s", st.Name, strings.Join(links, ", ")))
	}
	status.State = actions.StateReverted
	status.Result = &actions.Result{State: actions.StateReverted, Message: "reverted on the default branch: " + strings.Join(all, "; ") + " — the files carry their content from before the merge again", At: now()}
	if status.Rollout.FinishedAt == nil {
		status.Rollout.FinishedAt = now()
	}
	text := fmt.Sprintf("Reverted on the default branch — %s. The action is *reverted*: the files carry their content from before the merge again, whatever the installation reads.", strings.Join(stages, "; "))
	if len(text) > reportLimit {
		text = text[:reportLimit] + "…"
	}
	return text
}

// revertName is a revert in one clause: the short commit and its pull
// request in repository when GitHub links one.
func revertName(repository string, rv actions.Revert) string {
	if rv.PullRequest == 0 {
		return shortSHA(rv.Commit)
	}
	return fmt.Sprintf("%s (%s#%d)", shortSHA(rv.Commit), repository, rv.PullRequest)
}

// revertLink is revertName with links for the review's thread.
func revertLink(repository string, rv actions.Revert) string {
	commit := shortSHA(rv.Commit)
	if rv.URL != "" {
		commit = fmt.Sprintf("<%s|%s>", rv.URL, commit)
	}
	if rv.PullRequest == 0 {
		return commit
	}
	return fmt.Sprintf("<%s|%s#%d> (%s)", rv.PullRequestURL, repository, rv.PullRequest, commit)
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// mergedPRs are the indexes of the merged pull requests of the stages of a.
func mergedPRs(a *actions.Action, stages ...string) []int {
	var idx []int
	for _, name := range stages {
		for _, k := range a.StagePullRequests(name) {
			if a.Status.PullRequests[k].State == actions.PullRequestMerged {
				idx = append(idx, k)
			}
		}
	}
	return idx
}

// watchReverts runs the revert check on stage i's pull requests before the
// watch reads the installation: a reverted one records the action reverted,
// tells the review's thread and is the watch's answer; nil when every one
// stands. The check is the decision's prerequisite — without a GitHub token,
// or with a read that failed, the watch decides nothing.
func (t *Tools) watchReverts(ctx context.Context, a *actions.Action, status *actions.Status, i int) (*WatchResult, error) {
	st := status.Rollout.Installations[i]
	c, err := t.gitHubAs(ctx, ToolWatchAction)
	if err != nil {
		return nil, err
	}
	found, err := revertsOf(ctx, c, status, mergedPRs(a, st.Name))
	if err != nil {
		return nil, fmt.Errorf("%s: whether the pull requests of %s are still on the default branch could not be read as you: %w; nothing is decided — call again", ToolWatchAction, st.Name, err)
	}
	if len(found) == 0 {
		return nil, nil
	}
	report := markReverted(status, a, found)
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, *status)
	if err != nil {
		return nil, fmt.Errorf("%s: %s is reverted and the action could not record it: %w", ToolWatchAction, st.Name, err)
	}
	st = a.Status.Rollout.Installations[i]
	out := &WatchResult{Action: a, Installation: st.Name, State: st.State, Objects: []actions.RolloutObject{}, Report: report,
		Next: fmt.Sprintf("the actor (%s) withdraws the action with %s and the reason; a new action commits the change again", a.Spec.Actor.Login, ToolDenyAction)}
	out.Message = fmt.Sprintf("%s is %s (action %s, %s): %s.", st.Name, st.State, a.Name, a.Status.State, st.Message)
	if told := t.postResult(ctx, a, report); told != "" {
		out.Message += " " + told
	}
	t.d.Log.Info("action_reverted", identity.LogAttr(ctx), "action", a.Name, "installation", st.Name, "pullRequests", len(found))
	return out, nil
}

// withdrawal says whether deny_action on a is its actor's withdrawal of a
// merged action: one reverted, one that still reads its change live (the
// withdrawal reads the revert first), or one failed after its approval was
// decided — a stage failed on its merge, a wave stopped.
func withdrawal(a *actions.Action) bool {
	switch a.Status.State {
	case actions.StateReverted, actions.StateRollingOut, actions.StateWaitingForCustomer, actions.StateEnabled, actions.StateDrifted:
		return true
	case actions.StateFailed:
		return decided(a)
	}
	return false
}

// withdraw is deny_action on a merged action, as its actor, with the reason:
// an action reading its change live has the revert read first — none found,
// nothing was taken back and the withdrawal is refused; any pull request
// still open (a wave's stages after the one that stopped) is closed; the
// action moves to withdrawn, the reason on record and in the review's
// thread. The approval stands as decided: approve stays a second person's,
// withdraw is the actor's.
func (t *Tools) withdraw(ctx context.Context, a *actions.Action, id *identity.Identity, reason string) (any, error) {
	if id.Login != a.Spec.Actor.Login {
		return nil, fmt.Errorf("%s: action %s is %s — its pull requests are merged, and a merged action is withdrawn by its actor (%s), with the reason; you are %s", ToolDenyAction, a.Name, a.Status.State, a.Spec.Actor.Login, id.Login)
	}
	if reason == "" {
		return nil, fmt.Errorf("%s needs %s: the withdrawal is recorded with it", ToolDenyAction, ArgReason)
	}
	status := a.Status
	status.PullRequests = slices.Clone(status.PullRequests)
	status.Rollout = stagesOf(a)
	var posts []string
	was := status.State
	if was != actions.StateFailed && was != actions.StateReverted {
		c, err := t.gitHubAs(ctx, ToolDenyAction)
		if err != nil {
			return nil, err
		}
		found, err := revertsOf(ctx, c, &status, mergedPRs(a, a.Spec.Installations...))
		if err != nil {
			return nil, fmt.Errorf("%s: whether the pull requests of action %s are still on the default branch could not be read as you: %w; nothing is withdrawn", ToolDenyAction, a.Name, err)
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("%s: action %s is %s and every pull request it merged is on the default branch as merged (read now as you): nothing was taken back — revert the pull requests on the default branch first, then withdraw the action", ToolDenyAction, a.Name, was)
		}
		posts = append(posts, markReverted(&status, a, found))
	}
	open := openPRs(status.PullRequests)
	if len(open) > 0 {
		token, _ := identity.TokenFromContext(ctx)
		remote, err := t.d.Remote(token)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ToolDenyAction, err)
		}
		if left := closeOpen(ctx, remote, status.PullRequests); len(left) > 0 {
			return nil, fmt.Errorf("%s: closing as %s: %s", ToolDenyAction, id.Login, strings.Join(left, "; "))
		}
	}
	prior := status.State
	if status.Result != nil && status.Result.Message != "" {
		prior += ": " + status.Result.Message
	}
	status.State = actions.StateWithdrawn
	status.Withdrawal = &actions.Withdrawal{By: id.Login, Reason: reason, At: now()}
	status.Result = &actions.Result{State: actions.StateWithdrawn, Message: fmt.Sprintf("withdrawn by %s: %s (the action was %s)", id.Login, reason, prior), At: now()}
	for i := range status.Rollout.Installations {
		st := &status.Rollout.Installations[i]
		switch st.State {
		case actions.StateEnabled:
		case "":
			if len(openPRs(stagePRs(a.Status.PullRequests, a.StagePullRequests(st.Name)))) > 0 {
				st.Message += "; its pull requests were closed by " + id.Login
			}
		default:
			st.State, st.Message = actions.StateWithdrawn, fmt.Sprintf("withdrawn by %s: %s; %s", id.Login, reason, st.Message)
		}
	}
	if status.Rollout.FinishedAt == nil {
		status.Rollout.FinishedAt = now()
	}
	a, err := t.d.Actions.UpdateStatus(ctx, a.Name, status)
	if err != nil {
		return nil, fmt.Errorf("%s: the action could not record the withdrawal: %w", ToolDenyAction, err)
	}
	text := fmt.Sprintf("Withdrawn by *%s*: %s. The action is *withdrawn* (it was %s).", id.Login, reason, was)
	if len(open) > 0 {
		text += fmt.Sprintf(" Closed: %s.", prLinks(open))
	}
	posts = append(posts, text)
	msg := fmt.Sprintf("%s withdrew action %s (it was %s): %s", id.Login, a.Name, was, reason)
	if len(open) > 0 {
		msg += fmt.Sprintf(" — %d pull request(s) closed (%s)", len(open), prList(open))
	}
	msg += "."
	for _, p := range posts {
		if told := t.postResult(ctx, a, p); told != "" {
			msg += " " + told
			break
		}
	}
	t.d.Log.Info("action_withdrawn", identity.LogAttr(ctx), "action", a.Name, "was", was, "closed", len(open))
	return Decision{Action: a, Message: msg}, nil
}
