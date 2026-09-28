package gh

import (
	"context"
	"fmt"

	"github.com/google/go-github/v92/github"
)

// MergeChange is what a merge commit changed on its repository's default
// branch: every file the commit changed against its first parent, with the
// blob the commit left and the blob before it — what the revert check
// compares the default branch with.
type MergeChange struct {
	Commit string
	Files  []FileChange
}

// FileChange is one file of a merge: Blob is its blob after the merge and
// Before its blob before; "" where the file is absent.
type FileChange struct {
	Path, Blob, Before string
}

// maxChanges bounds the changes Files keeps; beyond it the map starts over.
// An entry is a commit's paths and blob SHAs, a few KiB.
const maxChanges = 4096

// ChangeOf is what the commit sha of owner/repo changed against its first
// parent, read as the person: the commit's files by status and blob (one
// request per 300 files), and — when the commit modified or removed a file —
// its parent's tree for the blobs before (one request, the tree not kept). A
// commit never changes, so the answer is cached across calls and persons;
// each asker's access is confirmed first by the repository's tree at HEAD,
// as them — the read the revert check compares with anyway.
func ChangeOf(ctx context.Context, c *Client, owner, repo, sha string) (MergeChange, error) {
	return c.files.change(ctx, c, owner, repo, sha)
}

func (f *Files) change(ctx context.Context, c *Client, owner, repo, sha string) (MergeChange, error) {
	if _, err := f.snapshot(ctx, c, owner, repo, ""); err != nil {
		return MergeChange{}, fmt.Errorf("github: %s/%s@%s: %w", owner, repo, sha, err)
	}
	key := owner + "/" + repo + "@" + sha
	f.mu.Lock()
	ch, ok := f.changes[key]
	f.mu.Unlock()
	if ok {
		return ch, nil
	}
	v, err, _ := f.flights.Do("change "+key, func() (any, error) {
		ch, err := readChange(context.WithoutCancel(ctx), c, owner, repo, sha)
		if err != nil {
			return nil, err
		}
		f.mu.Lock()
		if len(f.changes) >= maxChanges {
			f.changes = map[string]MergeChange{}
		}
		f.changes[key] = ch
		f.mu.Unlock()
		return ch, nil
	})
	if err != nil {
		return MergeChange{}, fmt.Errorf("github: %s/%s@%s: %w", owner, repo, sha, err)
	}
	return v.(MergeChange), nil
}

// readChange reads the commit's files and, when one was there before, the
// parent's tree.
func readChange(ctx context.Context, c *Client, owner, repo, sha string) (MergeChange, error) {
	ch := MergeChange{Commit: sha}
	parent, before := "", false
	opts := &github.ListOptions{PerPage: 300}
	for {
		rc, resp, err := c.Repositories.GetCommit(ctx, owner, repo, sha, opts)
		if err != nil {
			return MergeChange{}, classify(err)
		}
		if len(rc.Parents) == 0 {
			return MergeChange{}, fmt.Errorf("the commit has no parent: it merged nothing into a branch")
		}
		parent = rc.Parents[0].GetSHA()
		for _, file := range rc.Files {
			switch file.GetStatus() {
			case "added":
				ch.Files = append(ch.Files, FileChange{Path: file.GetFilename(), Blob: file.GetSHA()})
			case "removed":
				ch.Files = append(ch.Files, FileChange{Path: file.GetFilename()})
				before = true
			case "renamed":
				ch.Files = append(ch.Files, FileChange{Path: file.GetFilename(), Blob: file.GetSHA()}, FileChange{Path: file.GetPreviousFilename()})
				before = true
			default:
				ch.Files = append(ch.Files, FileChange{Path: file.GetFilename(), Blob: file.GetSHA()})
				before = true
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if !before {
		return ch, nil
	}
	body, _, err := treeAt(ctx, c, owner, repo, parent, "")
	if err != nil {
		return MergeChange{}, classify(err)
	}
	if body.GetTruncated() {
		return MergeChange{}, truncated(owner, repo, parent)
	}
	blobs := make(map[string]string, len(body.Entries))
	for _, e := range body.Entries {
		if e.GetType() != "tree" {
			blobs[e.GetPath()] = e.GetSHA()
		}
	}
	for i := range ch.Files {
		ch.Files[i].Before = blobs[ch.Files[i].Path]
	}
	return ch, nil
}

// Blobs are the blobs of paths on the default branch of owner/repo, read as
// the person from the tree at HEAD: "" for a path that is absent or a
// directory. It costs no request beyond the tree's.
func Blobs(ctx context.Context, c *Client, owner, repo string, paths []string) (map[string]string, error) {
	t, err := c.files.snapshot(ctx, c, owner, repo, "")
	if err != nil {
		return nil, fmt.Errorf("github: %s/%s: %w", owner, repo, err)
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		if e, ok := t.entries[p]; ok && !e.dir {
			out[p] = e.sha
		}
	}
	return out, nil
}

// Commit is a commit on a default branch as GitHub names it: the SHA, its
// page, and the pull request it came through when GitHub links one
// (PullRequest 0 when none does).
type Commit struct {
	SHA, URL       string
	PullRequest    int
	PullRequestURL string
}

// LastChange is the newest commit on the default branch of owner/repo that
// changed path, with the pull request GitHub links it to — a merged one
// first: two requests as the person.
func LastChange(ctx context.Context, c *Client, owner, repo, path string) (Commit, error) {
	list, _, err := c.Repositories.ListCommits(ctx, owner, repo, &github.CommitsListOptions{Path: path, ListOptions: github.ListOptions{PerPage: 1}})
	if err != nil {
		return Commit{}, fmt.Errorf("github: %s/%s: the commits of %s: %w", owner, repo, path, classify(err))
	}
	if len(list) == 0 {
		return Commit{}, fmt.Errorf("github: %s/%s: no commit on the default branch changed %s", owner, repo, path)
	}
	cm := Commit{SHA: list[0].GetSHA(), URL: list[0].GetHTMLURL()}
	prs, _, err := c.PullRequests.ListPullRequestsWithCommit(ctx, owner, repo, cm.SHA, &github.ListOptions{PerPage: 10})
	if err != nil {
		return Commit{}, fmt.Errorf("github: %s/%s: the pull request of %s: %w", owner, repo, cm.SHA, classify(err))
	}
	for _, pr := range prs {
		if cm.PullRequest == 0 || !pr.GetMergedAt().IsZero() {
			cm.PullRequest, cm.PullRequestURL = pr.GetNumber(), pr.GetHTMLURL()
		}
		if !pr.GetMergedAt().IsZero() {
			break
		}
	}
	return cm, nil
}
