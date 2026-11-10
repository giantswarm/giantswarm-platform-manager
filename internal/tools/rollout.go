package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The rollout proof: every Flux object Ready is not yet the merged change. A
// HelmRelease stays Ready on the release before the merge until Flux
// reconciles it, and a values change through its valuesFrom ConfigMap leaves
// its generation as it was, so nothing on it reads "not yet". Before the
// watch decides a stage whose pull requests are merged, it reads that the
// installation runs the merged revision: the Konfiguration has applied a
// commit of the configs repository that contains the merge, and the
// HelmRelease that reads the rendered values file has run a Helm release
// with new values since the merge. While either is missing, the stage stays
// rolling out and its message names what it waits for.

// rolloutBound is how long after a stage's last merge the watch waits for
// the proof: Flux reconciles a HelmRelease's valuesFrom on its interval (ten
// minutes on the platform's releases), a Konfiguration every minute, and a
// Helm upgrade with its waits takes minutes more. Past it, the stage fails
// on the proof as on a release Flux gave up on; the watch re-reads it, and
// the proof arriving later carries it on.
const rolloutBound = 30 * time.Minute

// helmDeployed is the status of a Helm release that runs.
const helmDeployed = "deployed"

// rolloutWait is one thing the proof still waits for: what, in one clause,
// and whether it rests on a read this call made — a revision behind, a
// release not run yet — or on one it could not make, which the bound does
// not count: what the person may not read is not checked, never a failure.
type rolloutWait struct {
	what   string
	unread bool
}

// rolloutWaits reads what stage installation's rollout still waits for in
// res, with GitHub as the person for what the cluster does not say: whether
// a commit a Konfiguration applied contains a merge, and whether a merge
// changed the rendered values file. Nothing when the merged revision runs.
func (t *Tools) rolloutWaits(ctx context.Context, def installations.Capability, a *actions.Action, status *actions.Status, installation string, res verify.Result) ([]rolloutWait, error) {
	current := *a
	current.Status = *status
	merged := mergedPRs(&current, installation)
	if len(merged) == 0 {
		return nil, nil
	}
	c, err := t.gitHubAs(ctx, ToolWatchAction)
	if err != nil {
		return nil, err
	}
	var waits []rolloutWait
	for _, ch := range liveChecks(res) {
		switch {
		case ch.Kind == string(render.SourceFollowed):
			w, err := sourceWaits(ctx, c, status.PullRequests, merged, ch)
			if err != nil {
				return nil, err
			}
			waits = append(waits, w...)
		case ch.Releases != nil:
			w, err := releaseWaits(ctx, c, def, status.PullRequests, merged, ch)
			if err != nil {
				return nil, err
			}
			waits = append(waits, w...)
		}
	}
	return waits, nil
}

// liveChecks are every check of the live dimensions of res.
func liveChecks(res verify.Result) []verify.Check {
	var out []verify.Check
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Live != nil {
				out = append(out, d.Live.Checks...)
			}
		}
	}
	return out
}

// sourceWaits holds a Konfiguration's applied revision (ch) against the
// merge commit of each merged pull request of the repository its
// GitRepository follows: the revision is the merge or one after it, read
// from GitHub when it is not the merge itself. A Konfiguration not read
// waits as unread.
func sourceWaits(ctx context.Context, c *gh.Client, prs []actions.PullRequest, merged []int, ch verify.Check) ([]rolloutWait, error) {
	object := objectLine(ch)
	if ch.Mark == verify.NotChecked && ch.Source == "" {
		return []rolloutWait{{what: object + " not read: " + orNotRead(ch.Message), unread: true}}, nil
	}
	repository := repositoryOf(ch.Source)
	applied := commitOfRevision(ch.Revision)
	var waits []rolloutWait
	for _, k := range merged {
		pr := prs[k]
		if !strings.EqualFold(pr.Repository, repository) {
			continue
		}
		if applied == "" {
			waits = append(waits, rolloutWait{what: fmt.Sprintf("%s has applied no revision of %s yet, %s#%d's merge %s", object, pr.Repository, pr.Repository, pr.Number, shortSHA(pr.MergeCommit))})
			continue
		}
		owner, repo, err := gh.SplitRepo(pr.Repository)
		if err != nil {
			return nil, err
		}
		in, err := gh.Contains(ctx, c, owner, repo, pr.MergeCommit, applied)
		if err != nil {
			return nil, err
		}
		if !in {
			waits = append(waits, rolloutWait{what: fmt.Sprintf("%s applied %s, not yet %s#%d's merge %s", object, shortSHA(applied), pr.Repository, pr.Number, shortSHA(pr.MergeCommit))})
		}
	}
	return waits, nil
}

// releaseWaits holds the release history of the HelmRelease that reads the
// rendered values file (ch, a drift check of its whole values) against each
// merged pull request that changed that file: the newest release runs, was
// deployed at or after the merge, and with values other than those of the
// release that ran at the merge (configDigest). A values change reaches the
// HelmRelease through a ConfigMap of its valuesFrom: its generation stays as
// it was and Flux upgrades it on its interval, so the digest of the newest
// release is what tells the new values from the old. A pull request that
// changed no values file asks nothing of the release.
func releaseWaits(ctx context.Context, c *gh.Client, def installations.Capability, prs []actions.PullRequest, merged []int, ch verify.Check) ([]rolloutWait, error) {
	object := objectLine(ch)
	var waits []rolloutWait
	for _, k := range merged {
		pr := prs[k]
		changed, err := changesValues(ctx, c, def, pr)
		if err != nil {
			return nil, err
		}
		if !changed {
			continue
		}
		if len(ch.Releases) == 0 {
			if ch.Mark == verify.NotChecked {
				waits = append(waits, rolloutWait{what: fmt.Sprintf("the releases of %s not read: %s", object, orNotRead(ch.Message)), unread: true})
				continue
			}
			waits = append(waits, rolloutWait{what: fmt.Sprintf("%s keeps no release history to tell %s#%d's values by", object, pr.Repository, pr.Number)})
			continue
		}
		if w := releaseWait(object, pr, ch.Releases); w != "" {
			waits = append(waits, rolloutWait{what: w})
		}
	}
	return waits, nil
}

// releaseWait is what the release history says of pr's merge, "" when the
// newest release runs the merged values. A merge time not on record (a
// merge always records one) asks no more than a release that runs.
func releaseWait(object string, pr actions.PullRequest, releases []verify.Release) string {
	newest := releases[0]
	var mergedAt time.Time
	if pr.MergedAt != nil {
		mergedAt = pr.MergedAt.Truncate(time.Second)
	}
	merge := fmt.Sprintf("%s#%d's merge", pr.Repository, pr.Number)
	if pr.MergedAt != nil {
		merge += " at " + mergedAt.UTC().Format(time.RFC3339)
	}
	if newest.Status != helmDeployed || newest.Deployed == nil {
		return fmt.Sprintf("%s: release v%d is %s, no release runs since %s", object, newest.Version, orNotRead(newest.Status), merge)
	}
	if newest.Deployed.Before(mergedAt) {
		return fmt.Sprintf("%s runs release v%d, deployed at %s, before %s", object, newest.Version, newest.Deployed.UTC().Format(time.RFC3339), merge)
	}
	for _, r := range releases[1:] {
		if r.Deployed == nil || !r.Deployed.Before(mergedAt) {
			continue
		}
		if r.ConfigDigest == newest.ConfigDigest {
			return fmt.Sprintf("%s runs release v%d with the values of release v%d from before %s", object, newest.Version, r.Version, merge)
		}
		break
	}
	return ""
}

// changesValues says whether pr's merge changed the capability's rendered
// values file, read as the person (gh.ChangeOf, cached per commit).
func changesValues(ctx context.Context, c *gh.Client, def installations.Capability, pr actions.PullRequest) (bool, error) {
	if pr.MergeCommit == "" {
		return false, fmt.Errorf("%s#%d: GitHub names no merge commit", pr.Repository, pr.Number)
	}
	owner, repo, err := gh.SplitRepo(pr.Repository)
	if err != nil {
		return false, err
	}
	change, err := gh.ChangeOf(ctx, c, owner, repo, pr.MergeCommit)
	if err != nil {
		return false, err
	}
	for _, f := range change.Files {
		if strings.HasSuffix(f.Path, verify.ValuesSuffix(def.Name)) {
			return true, nil
		}
	}
	return false, nil
}

// objectLine names a check's object as the stage's message does: kind and
// namespace/name.
func objectLine(ch verify.Check) string {
	kind, _, _ := strings.Cut(ch.Resource, ".")
	return kind + " " + objectName(ch.Namespace, ch.Name)
}

// repositoryOf is the owner/repo of a Git URL as Flux's GitRepository names
// it: https://github.com/owner/repo, ssh://git@host:443/owner/repo.git or
// git@github.com:owner/repo.git.
func repositoryOf(url string) string {
	path := strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	owner := parts[len(parts)-2]
	if i := strings.LastIndex(owner, ":"); i >= 0 {
		owner = owner[i+1:]
	}
	return owner + "/" + parts[len(parts)-1]
}

// commitOfRevision is the commit of a Flux revision: a Konfiguration's bare
// commit, or a Kustomization's "main@sha1:<commit>".
func commitOfRevision(revision string) string {
	if i := strings.LastIndex(revision, ":"); i >= 0 {
		return revision[i+1:]
	}
	return revision
}

// lastMerge is when the last of stage installation's pull requests was
// merged; zero when none carries the time.
func lastMerge(a *actions.Action, status actions.Status, installation string) time.Time {
	current := *a
	current.Status = status
	var last time.Time
	for _, k := range mergedPRs(&current, installation) {
		if at := status.PullRequests[k].MergedAt; at != nil && at.After(last) {
			last = *at
		}
	}
	return last
}

// overdue says whether the proof is past rolloutBound: a wait this call
// read, and the stage's last merge longer ago than the bound.
func overdue(waits []rolloutWait, merged time.Time) bool {
	if merged.IsZero() || time.Since(merged) <= rolloutBound {
		return false
	}
	for _, w := range waits {
		if !w.unread {
			return true
		}
	}
	return false
}

// waitLines are the waits, each in one clause.
func waitLines(waits []rolloutWait) []string {
	out := make([]string, 0, len(waits))
	for _, w := range waits {
		out = append(out, w.what)
	}
	return out
}
