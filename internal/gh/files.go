package gh

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/go-github/v92/github"
	"golang.org/x/sync/singleflight"
)

// DefaultFreshness is how long a person's validation of a repository's tree
// stands before their next call validates it again: long enough to cover
// the burst a portal page fires (a list and the verifies of one installation
// within a few seconds), short enough that a merge is seen by the next click.
const DefaultFreshness = 10 * time.Second

// defaultRef is the tree read when a caller names no ref: the default
// branch's head, as GitHub resolves HEAD.
const defaultRef = "HEAD"

// blobCacheBytes bounds the content cache; beyond it the least recently used
// blobs go.
const blobCacheBytes = 32 << 20

// treeIdle is how long a tree nobody read stays cached.
const treeIdle = time.Hour

// Files is the manager's read path for repository files. A call reads a
// repository as one recursive Git tree at a ref (HEAD: the default branch),
// fetched as the person with a conditional request: a 304 costs the person's
// quota nothing and remains the permission check, since GitHub answers 404
// for a repository the person may not see. A file's existence is answered
// from the tree, its content read as a blob by SHA. Trees and blobs are
// cached across calls and persons — a blob's SHA is its content, a tree's
// ETag its listing — so an unchanged file is never fetched twice, and only a
// repository that changed costs its tree and its changed blobs. A person's
// re-validation of a tree is skipped within the freshness window after their
// last one; Invalidate ends the window early for a repository the manager
// merged into.
type Files struct {
	freshness time.Duration
	maxBytes  int
	now       func() time.Time

	mu    sync.Mutex
	trees map[treeKey]*tree
	blobs map[string]*list.Element // blob SHA → element holding a *blob
	lru   *list.List
	bytes int

	// flights coalesces concurrent fetches of one blob, across calls: the
	// content is the SHA's whoever fetches it.
	flights singleflight.Group
}

// NewFiles is an empty cache whose validations stand for freshness; zero
// validates every call's first read of a repository.
func NewFiles(freshness time.Duration) *Files {
	return &Files{freshness: freshness, maxBytes: blobCacheBytes, now: time.Now, trees: map[treeKey]*tree{}, blobs: map[string]*list.Element{}, lru: list.New()}
}

// treeKey names a repository's listing: owner/repo at a ref.
type treeKey struct{ repo, ref string }

// tree is one repository's listing at a ref as GitHub last answered it.
type tree struct {
	etag    string
	sha     string
	entries map[string]entry
	// validated is, per person, when GitHub last confirmed this listing to
	// them; used when anyone last read it.
	validated map[string]time.Time
	used      time.Time
}

// entry is one path of a tree: a blob with its SHA, or a directory.
type entry struct {
	sha string
	dir bool
}

// blob is one file's content, by SHA.
type blob struct {
	sha     string
	content string
}

// flight is one call's fetch of a tree, shared by every read of the call
// that asks for it while it runs.
type flight struct {
	done chan struct{}
	t    *tree
	err  error
}

// Invalidate ends every person's freshness window for repository (owner/repo)
// at every ref: the next read validates the tree with GitHub again. Called
// after the manager merged into the repository.
func (f *Files) Invalidate(repository string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, t := range f.trees {
		if k.repo == repository {
			t.validated = map[string]time.Time{}
		}
	}
}

// read is path of owner/repo at ref as the person c acts as.
func (f *Files) read(ctx context.Context, c *Client, owner, repo, path, ref string) (string, error) {
	t, err := f.snapshot(ctx, c, owner, repo, ref)
	if err != nil {
		return "", fmt.Errorf("github: %s/%s:%s: %w", owner, repo, path, err)
	}
	e, ok := t.entries[path]
	switch {
	case !ok:
		return "", fmt.Errorf("github: %s/%s:%s: %w", owner, repo, path, ErrNotFound)
	case e.dir:
		return "", fmt.Errorf("github: %s/%s:%s is a directory, not a file", owner, repo, path)
	}
	content, err := f.blob(ctx, c, owner, repo, e.sha)
	if err != nil {
		return "", fmt.Errorf("github: %s/%s:%s: %w", owner, repo, path, err)
	}
	return content, nil
}

// exists says whether path is a file of owner/repo at HEAD as the person c
// acts as.
func (f *Files) exists(ctx context.Context, c *Client, owner, repo, path string) (bool, error) {
	t, err := f.snapshot(ctx, c, owner, repo, "")
	if err != nil {
		return false, fmt.Errorf("github: %s/%s:%s: %w", owner, repo, path, err)
	}
	e, ok := t.entries[path]
	return ok && !e.dir, nil
}

// snapshot is the tree of owner/repo at ref for the person c acts as: the one
// this call already validated, the shared one their last validation still
// covers, else GitHub's answer to a conditional request as them.
func (f *Files) snapshot(ctx context.Context, c *Client, owner, repo, ref string) (*tree, error) {
	if ref == "" {
		ref = defaultRef
	}
	key := treeKey{repo: owner + "/" + repo, ref: ref}
	c.mu.Lock()
	fl, ok := c.trees[key]
	if !ok {
		fl = &flight{done: make(chan struct{})}
		c.trees[key] = fl
	}
	c.mu.Unlock()
	if ok {
		select {
		case <-fl.done:
			return fl.t, fl.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	fl.t, fl.err = f.validated(ctx, c, key, owner, repo, ref)
	close(fl.done)
	return fl.t, fl.err
}

// validated is the tree as the person last had it confirmed within the
// freshness window, else fetched.
func (f *Files) validated(ctx context.Context, c *Client, key treeKey, owner, repo, ref string) (*tree, error) {
	f.mu.Lock()
	t := f.trees[key]
	now := f.now()
	if t != nil && now.Sub(t.validated[c.person]) < f.freshness {
		t.used = now
		f.mu.Unlock()
		return t, nil
	}
	f.mu.Unlock()
	return f.fetch(ctx, c, key, owner, repo, ref)
}

// fetch asks GitHub for the tree as the person, conditionally on the ETag of
// the one cached: a 304 confirms that one to the person, a 200 replaces it.
func (f *Files) fetch(ctx context.Context, c *Client, key treeKey, owner, repo, ref string) (*tree, error) {
	f.mu.Lock()
	known := f.trees[key]
	f.mu.Unlock()
	u := fmt.Sprintf("repos/%s/%s/git/trees/%s?recursive=1", owner, repo, url.PathEscape(ref))
	req, err := c.NewRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if known != nil {
		req.Header.Set("If-None-Match", known.etag)
	}
	var body github.Tree
	resp, err := c.Do(req, &body)
	now := f.now()
	if err != nil {
		var er *github.ErrorResponse
		if known != nil && errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == http.StatusNotModified {
			f.mu.Lock()
			known.validated[c.person] = now
			known.used = now
			f.mu.Unlock()
			return known, nil
		}
		return nil, classify(err)
	}
	if body.GetTruncated() {
		return nil, fmt.Errorf("GitHub truncates the tree of %s/%s at %s (over 100,000 entries or 7 MB), and the manager reads a repository as one tree", owner, repo, ref)
	}
	t := &tree{etag: resp.Header.Get("ETag"), sha: body.GetSHA(), entries: make(map[string]entry, len(body.Entries)), validated: map[string]time.Time{c.person: now}, used: now}
	for _, e := range body.Entries {
		t.entries[e.GetPath()] = entry{sha: e.GetSHA(), dir: e.GetType() == "tree"}
	}
	f.mu.Lock()
	f.trees[key] = t
	for k, old := range f.trees {
		if now.Sub(old.used) > treeIdle {
			delete(f.trees, k)
		}
	}
	f.mu.Unlock()
	return t, nil
}

// blob is the content of sha in owner/repo: cached, or fetched once however
// many ask at once. The fetch runs as the first asker, whose tree named the
// SHA, and is not cut short by their call ending: the content is the SHA's,
// and the next call wants it too.
func (f *Files) blob(ctx context.Context, c *Client, owner, repo, sha string) (string, error) {
	f.mu.Lock()
	if el, ok := f.blobs[sha]; ok {
		f.lru.MoveToFront(el)
		f.mu.Unlock()
		return el.Value.(*blob).content, nil
	}
	f.mu.Unlock()
	v, err, _ := f.flights.Do(sha, func() (any, error) {
		raw, _, err := c.Git.GetBlobRaw(context.WithoutCancel(ctx), owner, repo, sha)
		if err != nil {
			return nil, classify(err)
		}
		content := string(raw)
		f.mu.Lock()
		f.store(sha, content)
		f.mu.Unlock()
		return content, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// store puts content under sha, most recently used, and evicts the least
// recently used blobs beyond maxBytes. Called with f.mu held.
func (f *Files) store(sha, content string) {
	if _, ok := f.blobs[sha]; ok {
		return
	}
	f.blobs[sha] = f.lru.PushFront(&blob{sha: sha, content: content})
	f.bytes += len(content)
	for f.bytes > f.maxBytes && f.lru.Len() > 1 {
		last := f.lru.Back()
		b := last.Value.(*blob)
		f.lru.Remove(last)
		delete(f.blobs, b.sha)
		f.bytes -= len(b.content)
	}
}
