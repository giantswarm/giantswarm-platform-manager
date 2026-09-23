package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The resync: the Action record follows GitHub, not only the manager's own
// steps. A pull request merged by a person with the repository's own merge
// path, or closed by one, and a fileset reverted out of the default branch
// again are read on every read of the record — get_action, list_actions,
// list_installations, the approval tools before they decide, and
// watch_action — as the person reading, with the GitHub token muster puts
// on their call, at most once per ResyncInterval per action. The manager
// holds no token of its own, so nothing resyncs unattended: the portal's
// page and platformctl are what read periodically.

// DefaultResyncInterval is how old the record's picture of GitHub may be
// before a read reads the pull requests and the markers again.
const DefaultResyncInterval = time.Minute

func (t *Tools) resyncInterval() time.Duration {
	if t.d.ResyncInterval > 0 {
		return t.d.ResyncInterval
	}
	return DefaultResyncInterval
}

// due says whether a's record is old enough for a resync: an action a read
// can still move, whose last sync is older than the interval.
func (t *Tools) due(a *actions.Action) bool {
	if actions.Terminal(a.Status.State) {
		return false
	}
	return a.Status.SyncedAt == nil || time.Since(*a.Status.SyncedAt) >= t.resyncInterval()
}

// resyncAs has a's record follow GitHub as the caller of ctx and answers
// the record as it is after: a as it was when the request carries no GitHub
// token (a server without OAuth), the record is fresh enough, or the reads
// failed — a failed resync is logged and never fails the read that asked.
func (t *Tools) resyncAs(ctx context.Context, a *actions.Action) *actions.Action {
	fresh, err := t.resyncCaller(ctx, a)
	switch {
	case errors.Is(err, errNoGitHubToken):
		return a
	case err != nil:
		t.d.Log.Warn("action_resync_failed", identity.LogAttr(ctx), "action", a.Name, "error", err.Error())
		return a
	}
	return fresh
}

// errNoGitHubToken: the request carries no GitHub token to read the pull
// requests with — a server without OAuth.
var errNoGitHubToken = errors.New("the call carried no GitHub token (a server without OAuth)")

// resyncCaller is a's record after it followed GitHub as the caller of ctx:
// a itself when the manager keeps no records or the record is fresh enough,
// errNoGitHubToken when the request carries no GitHub token, else the reads'
// refusal.
func (t *Tools) resyncCaller(ctx context.Context, a *actions.Action) (*actions.Action, error) {
	if t.d.Actions == nil || !t.due(a) {
		return a, nil
	}
	token, ok := identity.TokenFromContext(ctx)
	id, _ := identity.FromContext(ctx)
	if !ok || id == nil {
		return nil, errNoGitHubToken
	}
	c, err := t.person(token)
	if err != nil {
		return nil, err
	}
	return t.resync(ctx, c, id, a)
}

// resyncAll is resyncAs over a list, in place.
func (t *Tools) resyncAll(ctx context.Context, list []actions.Action) {
	for i := range list {
		list[i] = *t.resyncAs(ctx, &list[i])
	}
}

// resync reads, as the person c stands for, every pull request of a that is
// open on record or merged without its merge on record, and the marker of
// every stage whose pull requests are merged; it records what GitHub says
// and moves the action: every pull request of the stage in flight merged →
// rolling out (the approval recorded as merged without approval, by whom,
// when no one decided), one closed unmerged → failed naming it, every merged
// stage's marker gone from the default branch → removed, with the objects
// the definition rendered on the installations when its Kustomization does
// not prune. One write of the record; the review's thread is told of a move.
func (t *Tools) resync(ctx context.Context, c *gh.Client, id *identity.Identity, a *actions.Action) (*actions.Action, error) {
	status := a.Status
	status.PullRequests = slices.Clone(status.PullRequests)
	var merged, closed []actions.PullRequest
	var unread []string
	for k := range status.PullRequests {
		pr := &status.PullRequests[k]
		if pr.State == actions.PullRequestClosed || (pr.State == actions.PullRequestMerged && pr.MergeCommit != "") {
			continue
		}
		owner, repo, err := gh.SplitRepo(pr.Repository)
		if err != nil {
			return nil, err
		}
		st, err := gh.PullRequest(ctx, c, owner, repo, pr.Number)
		if err != nil {
			unread = append(unread, fmt.Sprintf("%s#%d: %v", pr.Repository, pr.Number, err))
			continue
		}
		switch {
		case st.Merged:
			was := pr.State
			pr.State, pr.MergeCommit, pr.MergedAt, pr.MergedBy = actions.PullRequestMerged, st.MergeCommit, st.MergedAt, st.MergedBy
			if was != actions.PullRequestMerged {
				merged = append(merged, *pr)
			}
		case st.State == gh.PullRequestClosed:
			pr.State, pr.ClosedAt = actions.PullRequestClosed, st.ClosedAt
			closed = append(closed, *pr)
		}
	}
	// The helpers read the pull requests off an action: this one carries the
	// states as GitHub has them now.
	fresh := *a
	fresh.Status = status
	var posts []string

	// A pull request closed unmerged: the action cannot complete.
	if len(closed) > 0 && !actions.Terminal(status.State) && status.State != actions.StateFailed {
		msg := fmt.Sprintf("%d pull request(s) closed unmerged outside the manager (%s)", len(closed), prList(closed))
		if open := openPRs(status.PullRequests); len(open) > 0 {
			msg += fmt.Sprintf("; %d pull request(s) stay open (%s) — %s closes them", len(open), prList(open), ToolDenyAction)
		}
		status.State = actions.StateFailed
		status.Result = &actions.Result{State: actions.StateFailed, Message: msg, At: now()}
		if status.Rollout != nil && status.Rollout.StartedAt != nil {
			status.Rollout.FinishedAt = now()
		}
		posts = append(posts, fmt.Sprintf("Closed unmerged outside the manager: %s. The action is failed.", prLinks(closed)))
	}

	// The stage in flight of an action pending approval, every pull request
	// merged outside merge_action: it rolls out as after the merge.
	// The rollout is written only when a stage starts: a single installation's
	// action carries none before its merge, and its absence is what lets the
	// files' state stand once the action is denied.
	if status.State == actions.StatePendingApproval {
		stages := stagesOf(&fresh)
		for i, st := range stages.Installations {
			if st.State == actions.StateEnabled {
				continue
			}
			if idx := a.StagePullRequests(st.Name); len(idx) > 0 && allMerged(&fresh, st.Name) {
				status.Rollout = stages
				startStage(&status, i)
				by := mergedBy(status.PullRequests, idx)
				if status.Approval == nil || status.Approval.Decision == "" {
					approval := *approvalOf(&status)
					approval.Decision, approval.DecidedBy, approval.At = actions.DecisionMergedWithoutApproval, by, now()
					status.Approval = &approval
				}
				posts = append(posts, fmt.Sprintf("Merged outside the manager by %s: %s. *%s* is rolling out — the rollout and the probes follow here.", by, prLinks(stagePRs(status.PullRequests, idx)), st.Name))
			}
			break
		}
		fresh.Status = status
	}

	// The revert: every stage whose pull requests are merged has its marker
	// read; every one gone from its default branch again removes the action.
	if !actions.Terminal(status.State) {
		if def, ok := installations.FindCapability(a.Spec.Capability); ok {
			gone, kept, errs := t.revertedStages(ctx, c, &fresh, def)
			unread = append(unread, errs...)
			if len(gone) > 0 && kept == 0 {
				posts = append(posts, remove(&status, &fresh, def, gone))
			}
		}
	}

	if len(unread) == 0 {
		status.SyncedAt, status.SyncedBy = now(), id.String()
	}
	out, err := t.d.Actions.UpdateStatus(ctx, a.Name, status)
	if err != nil {
		return nil, err
	}
	t.d.Log.Info("action_resynced", identity.LogAttr(ctx), "action", out.Name, "state", out.Status.State, "was", a.Status.State, "merged", len(merged), "closed", len(closed), "unread", len(unread))
	for _, u := range unread {
		t.d.Log.Info("action_resync_unread", identity.LogAttr(ctx), "action", out.Name, "what", u)
	}
	for _, text := range posts {
		t.postResult(ctx, out, text)
	}
	return out, nil
}

// revertedStage is one stage whose marker left the default branch again.
type revertedStage struct {
	installation, repository, path string
}

// revertedStages reads the marker of every stage of a whose pull requests
// are merged: the stages whose marker is gone, how many still carry it, and
// the reads that failed.
func (t *Tools) revertedStages(ctx context.Context, c *gh.Client, a *actions.Action, def installations.Capability) (gone []revertedStage, kept int, errs []string) {
	for _, st := range stagesOf(a).Installations {
		if len(a.StagePullRequests(st.Name)) == 0 || !allMerged(a, st.Name) {
			continue
		}
		marker := markerOf(a, def, st.Name)
		repository, path, ok := strings.Cut(marker, ":")
		if !ok {
			continue
		}
		owner, repo, err := gh.SplitRepo(repository)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		exists, err := gh.FileExists(ctx, c, owner, repo, path)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("%s: %v", marker, err))
			kept++
		case exists:
			kept++
		default:
			gone = append(gone, revertedStage{installation: st.Name, repository: repository, path: path})
		}
	}
	return gone, kept, errs
}

// markerOf is the marker of installation's stage: the one recorded at
// commit, else — a record from before the markers were recorded — the
// definition's marker in the stage's pull request whose repository is the
// definition's marker repository by the fleet's naming; "" when neither.
func markerOf(a *actions.Action, def installations.Capability, installation string) string {
	if m := a.Spec.Markers[installation]; m != "" {
		return m
	}
	suffix := "-" + string(def.MarkerRepository)
	for _, k := range a.StagePullRequests(installation) {
		if repo := a.Status.PullRequests[k].Repository; strings.HasSuffix(repo, suffix) {
			return repo + ":" + def.EnabledMarker(installation)
		}
	}
	return ""
}

// remove moves status to removed for the stages gone — the fileset left the
// default branch again — and, for a definition whose Kustomization does not
// prune, records the objects it rendered on those installations that stay
// until a person deletes them. It answers the text for the review's thread.
func remove(status *actions.Status, a *actions.Action, def installations.Capability, gone []revertedStage) string {
	status.Rollout = stagesOf(a)
	var where []string
	status.Orphans = nil
	for _, g := range gone {
		if i := stageIndex(status.Rollout, g.installation); i >= 0 {
			st := &status.Rollout.Installations[i]
			st.State, st.Message = actions.StateRemoved, fmt.Sprintf("%s is gone from the default branch of %s again", g.path, g.repository)
		}
		where = append(where, fmt.Sprintf("%s (%s in %s)", g.installation, g.path, g.repository))
		if !def.Prunes {
			status.Orphans = append(status.Orphans, orphansOf(def, g.installation, a.InputsOnRecord(g.installation))...)
		}
	}
	msg := "the fileset is gone from the default branch again: " + strings.Join(where, ", ")
	text := fmt.Sprintf("The fileset of *%s* is gone from the default branch again: the action is removed.", strings.Join(installationsOf(gone), "*, *"))
	switch {
	case def.Prunes:
		msg += "; Flux prunes what the tree applied"
		text += " Flux prunes what the tree applied."
	case len(status.Orphans) > 0:
		msg += fmt.Sprintf("; %d object(s) the definition rendered stay on the installation(s) until a person deletes them — the Kustomization over the tree does not prune (orphans)", len(status.Orphans))
		text += fmt.Sprintf("\n%d object(s) stay until a person deletes them — the Kustomization over the tree does not prune:", len(status.Orphans))
		for _, o := range status.Orphans {
			text += fmt.Sprintf("\n• %s: %s %s", o.Installation, o.Kind, objectName(o.Namespace, o.Name))
		}
	}
	status.State = actions.StateRemoved
	status.Result = &actions.Result{State: actions.StateRemoved, Message: msg, At: now()}
	if status.Rollout.FinishedAt == nil {
		status.Rollout.FinishedAt = now()
	}
	if len(text) > reportLimit {
		text = text[:reportLimit] + "…"
	}
	return text
}

func installationsOf(gone []revertedStage) []string {
	out := make([]string, 0, len(gone))
	for _, g := range gone {
		out = append(out, g.installation)
	}
	return out
}

func objectName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

// orphansOf are the objects the definition renders on installation from the
// inputs on record — the HelmReleases its probes name, the manifests among
// its files — as a revert leaves them when the Kustomization does not prune.
// No inputs on record, or a render that refuses them, names none.
func orphansOf(def installations.Capability, installation string, inputs map[string]any) []actions.Orphan {
	if inputs == nil {
		return nil
	}
	in, err := def.Parse(inputs)
	if err != nil {
		return nil
	}
	res, err := def.Render(inputs, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		return nil
	}
	objects := plan.Objects(res)
	out := make([]actions.Orphan, 0, len(objects))
	for _, o := range objects {
		out = append(out, actions.Orphan{Installation: installation, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name})
	}
	return out
}

// stagePRs are the pull requests at idx.
func stagePRs(prs []actions.PullRequest, idx []int) []actions.PullRequest {
	out := make([]actions.PullRequest, 0, len(idx))
	for _, k := range idx {
		out = append(out, prs[k])
	}
	return out
}

// mergedBy names who merged the pull requests at idx, each login once, in
// order; "someone" when GitHub named nobody.
func mergedBy(prs []actions.PullRequest, idx []int) string {
	var logins []string
	for _, k := range idx {
		if by := prs[k].MergedBy; by != "" && !slices.Contains(logins, by) {
			logins = append(logins, by)
		}
	}
	if len(logins) == 0 {
		return "someone"
	}
	return strings.Join(logins, ", ")
}
