package tools

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

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

// helmDeployed is the status of a Helm release that runs; helmSuperseded
// of one that ran and a later release replaced.
const (
	helmDeployed   = "deployed"
	helmSuperseded = "superseded"
)

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
// The release that ran at a merge is the one recordBaselines put on the
// stage (st.Baselines).
func (t *Tools) rolloutWaits(ctx context.Context, def installations.Capability, a *actions.Action, status *actions.Status, st *actions.InstallationRollout, res verify.Result) ([]rolloutWait, error) {
	current := *a
	current.Status = *status
	merged := mergedPRs(&current, st.Name)
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
			w, err := releaseWaits(ctx, c, def, status.PullRequests, merged, ch, st)
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
// release that ran at the merge (configDigest) — the baseline, from the
// history or from the stage's record. A values change reaches the
// HelmRelease through a ConfigMap of its valuesFrom: its generation stays as
// it was and Flux upgrades it on its interval, so the digest of the newest
// release is what tells the new values from the old. A pull request that
// changed no values file asks nothing of the release.
func releaseWaits(ctx context.Context, c *gh.Client, def installations.Capability, prs []actions.PullRequest, merged []int, ch verify.Check, st *actions.InstallationRollout) ([]rolloutWait, error) {
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
		release, merge := objectName(ch.Namespace, ch.Name), fmt.Sprintf("%s#%d", pr.Repository, pr.Number)
		base := baselineOf(st, release, merge)
		if w := releaseWait(object, pr, ch.Releases, base); w != "" {
			waits = append(waits, rolloutWait{what: w})
		}
	}
	return waits, nil
}

// recordBaselines records on stage st, for each of its merged pull requests
// and each HelmRelease whose release history res read, the release that ran
// at the merge, where the history still keeps it and the stage has none on
// record: the first read after the merge sees it, and Flux drops it from the
// history once a later release succeeds after the next one. Called on every read of a stage that has not
// reached a state, Ready or not.
func recordBaselines(a *actions.Action, status actions.Status, st *actions.InstallationRollout, res verify.Result) {
	current := *a
	current.Status = status
	merged := mergedPRs(&current, st.Name)
	for _, ch := range liveChecks(res) {
		if len(ch.Releases) == 0 {
			continue
		}
		release := objectName(ch.Namespace, ch.Name)
		for _, k := range merged {
			pr := status.PullRequests[k]
			merge := fmt.Sprintf("%s#%d", pr.Repository, pr.Number)
			if baselineOf(st, release, merge) != nil {
				continue
			}
			if base := ranAtMerge(release, merge, pr, ch); base != nil {
				st.Baselines = append(st.Baselines, *base)
			}
		}
	}
}

// baselineOf is the stage's record of the release that ran at merge; nil
// when none is on record.
func baselineOf(st *actions.InstallationRollout, release, merge string) *actions.ReleaseBaseline {
	for i := range st.Baselines {
		if b := st.Baselines[i]; b.Release == release && b.PullRequest == merge {
			return &b
		}
	}
	return nil
}

// ranAtMerge is the release of the history of ch that ran when pr was
// merged: the newest that was deployed — deployed or superseded since —
// before the merge; nil when the history keeps none (helm-controller's
// Snapshots.Truncate keeps the newest release and those after the previous
// successful one) or the merge time is not on record. Flux's times are whole
// seconds: a release in the merge's second ran before the merge, since Flux
// applies a merge seconds after it at the earliest. Where no release ran at
// the merge (installedAfter), the baseline is version 0 with no digest.
func ranAtMerge(release, merge string, pr actions.PullRequest, ch verify.Check) *actions.ReleaseBaseline {
	if pr.MergedAt == nil {
		return nil
	}
	mergedAt := pr.MergedAt.Truncate(time.Second)
	for _, r := range ch.Releases {
		if r.Deployed == nil || r.Deployed.After(mergedAt) || (r.Status != helmDeployed && r.Status != helmSuperseded) {
			continue
		}
		return &actions.ReleaseBaseline{Release: release, PullRequest: merge, Version: r.Version, ConfigDigest: r.ConfigDigest, Deployed: r.Deployed}
	}
	if installedAfter(ch, mergedAt) {
		return &actions.ReleaseBaseline{Release: release, PullRequest: merge}
	}
	return nil
}

// installedAfter says that no release of ch's HelmRelease ran at the merge
// (mergedAt, its whole second): the HelmRelease was created after the merge,
// so none of its releases ran at it, and its history keeps its install,
// release v1, deployed after the merge. helm-controller clears the history
// when it installs (internal/reconcile/install.go) and Helm numbers a
// release name's first release 1, so a v1 in the history is the install the
// HelmRelease's history starts with; a HelmRelease that took over a release
// Helm ran before it is upgraded to a later version, and a truncated history
// has lost its v1. A release uninstalled and installed again starts at v1
// too: the creation time tells it, the HelmRelease being older than the
// merge.
func installedAfter(ch verify.Check, mergedAt time.Time) bool {
	if ch.Created == nil || !ch.Created.After(mergedAt) {
		return false
	}
	for _, r := range ch.Releases {
		if r.Version == 1 {
			return r.Deployed != nil && r.Deployed.After(mergedAt)
		}
	}
	return false
}

// releaseWait is what the release history says of pr's merge against base,
// the release that ran at the merge: "" only when the newest release runs,
// was deployed after the merge's second and carries values of another digest
// than base. Without a base the values that ran at the merge are not known,
// and nothing proves the newest release's are new. A base of version 0 says
// no release ran at the merge: any release deployed after it runs values
// Flux applied after it.
func releaseWait(object string, pr actions.PullRequest, releases []verify.Release, base *actions.ReleaseBaseline) string {
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
	if !newest.Deployed.After(mergedAt) {
		return fmt.Sprintf("%s runs release v%d, deployed at %s, before %s", object, newest.Version, newest.Deployed.UTC().Format(time.RFC3339), merge)
	}
	if base == nil {
		return fmt.Sprintf("%s runs release v%d since %s, and the release that ran at the merge is neither in its history nor on record: whether its values are new is not known", object, newest.Version, merge)
	}
	if base.Version != 0 && newest.ConfigDigest == base.ConfigDigest {
		return fmt.Sprintf("%s runs release v%d with the values of release v%d from before %s", object, newest.Version, base.Version, merge)
	}
	return ""
}

// changesValues says whether pr's merge changed the values of the
// capability's rendered values file, read as the person (gh.ChangeOf and
// the blobs, cached): a file added or removed does, and a changed one does
// when its YAML before and after differs. A file rewritten with the same
// values — a comment or the layout — composes the same values, and Flux
// runs no release for it.
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
		if !strings.HasSuffix(f.Path, verify.ValuesSuffix(def.Name)) {
			continue
		}
		if f.Before == "" || f.Blob == "" {
			return true, nil
		}
		same, err := sameValues(ctx, c, owner, repo, f.Before, f.Blob)
		if err != nil {
			return false, err
		}
		if !same {
			return true, nil
		}
	}
	return false, nil
}

// sameValues says whether the YAML documents of two blobs carry the same
// values; a blob that is no YAML differs.
func sameValues(ctx context.Context, c *gh.Client, owner, repo, before, after string) (bool, error) {
	var docs [2]any
	for i, sha := range []string{before, after} {
		content, err := gh.Blob(ctx, c, owner, repo, sha)
		if err != nil {
			return false, err
		}
		if err := yaml.Unmarshal([]byte(content), &docs[i]); err != nil {
			return false, nil
		}
	}
	return reflect.DeepEqual(docs[0], docs[1]), nil
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
	return !merged.IsZero() && time.Since(merged) > rolloutBound && readWait(waits)
}

// readWait says whether one of the waits rests on a read this call made.
func readWait(waits []rolloutWait) bool {
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
