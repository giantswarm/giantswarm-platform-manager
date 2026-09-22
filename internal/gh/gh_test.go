package gh

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGitHub answers the two reads Files makes — a repository's recursive
// tree at a ref, conditionally on its ETag, and a blob by SHA — for the
// repositories each token may see, and counts them.
type fakeGitHub struct {
	*httptest.Server
	mu     sync.Mutex
	files  map[string]map[string]string // owner/repo → path → content
	access map[string]map[string]bool   // token → owner/repo the token may read
	// rateLimited makes every request a 403 with the quota spent until reset.
	rateLimited bool
	reset       time.Time
	truncated   bool

	inFlight, peak         atomic.Int64
	treeRequests, treeFull atomic.Int64 // every tree request; the ones answered 200
	blobRequests           atomic.Int64
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{files: map[string]map[string]string{}, access: map[string]map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/git/trees/{ref}", func(w http.ResponseWriter, r *http.Request) {
		g.treeRequests.Add(1)
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		if !g.admit(w, r, repo) {
			return
		}
		g.mu.Lock()
		files := g.files[repo]
		truncated := g.truncated
		g.mu.Unlock()
		body, etag := treeJSON(files, truncated)
		if r.Header.Get("If-None-Match") == etag {
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		g.treeFull.Add(1)
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/git/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		g.blobRequests.Add(1)
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		if !g.admit(w, r, repo) {
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		for _, content := range g.files[repo] {
			if blobSHA(content) == r.PathValue("sha") {
				time.Sleep(2 * time.Millisecond)
				_, _ = w.Write([]byte(content))
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]any{messageKey: "Not Found"})
	})
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := g.inFlight.Add(1)
		defer g.inFlight.Add(-1)
		for {
			p := g.peak.Load()
			if n <= p || g.peak.CompareAndSwap(p, n) {
				break
			}
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.Close)
	return g
}

// admit answers the refusals: the quota spent, a repository the token may not
// see (404, as GitHub answers).
func (g *fakeGitHub) admit(w http.ResponseWriter, r *http.Request, repo string) bool {
	g.mu.Lock()
	limited, reset := g.rateLimited, g.reset
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	allowed := g.access[token][repo]
	g.mu.Unlock()
	if limited {
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		writeJSON(w, http.StatusForbidden, map[string]any{messageKey: "API rate limit exceeded for user ID 1."})
		return false
	}
	if !allowed {
		writeJSON(w, http.StatusNotFound, map[string]any{messageKey: "Not Found"})
		return false
	}
	return true
}

func (g *fakeGitHub) grant(token, repo string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.access[token] == nil {
		g.access[token] = map[string]bool{}
	}
	g.access[token][repo] = true
}

func (g *fakeGitHub) set(repo, path, content string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.files[repo] == nil {
		g.files[repo] = map[string]string{}
	}
	g.files[repo][path] = content
}

// treeJSON is GitHub's recursive tree of files — the blobs and the
// directories above them — with a weak ETag over the listing.
func treeJSON(files map[string]string, truncated bool) ([]byte, string) {
	dirs := map[string]bool{}
	var entries []map[string]any
	for p, content := range files {
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			dirs[strings.Join(parts[:i], "/")] = true
		}
		entries = append(entries, map[string]any{"path": p, "mode": "100644", "type": "blob", shaKey: blobSHA(content), "size": len(content)})
	}
	for d := range dirs {
		entries = append(entries, map[string]any{"path": d, "mode": "040000", "type": "tree", shaKey: blobSHA(d)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["path"].(string) < entries[j]["path"].(string) })
	body, _ := json.Marshal(map[string]any{shaKey: "t" + blobSHA(fmt.Sprint(entries)), "truncated": truncated, "tree": entries})
	sum := sha256.Sum256(body)
	return body, `W/"` + hex.EncodeToString(sum[:]) + `"`
}

// blobSHA is the fake's blob id of content: a hash of it, as git's is. The
// client takes the id from the tree and never inspects its form.
func blobSHA(content string) string {
	sum := sha256.Sum256([]byte("blob " + strconv.Itoa(len(content)) + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const (
	repoOwner, repoName = "o", "r"
	repoFull            = repoOwner + "/" + repoName
	aliceToken          = "alice-token"
	bobToken            = "bob-token"
	// The keys of GitHub's documents the fake writes, and a fixture file.
	shaKey, messageKey = "sha", "message"
	oneYAML            = "one: 1\n"
)

func (g *fakeGitHub) client(t *testing.T, files *Files, token string) *Client {
	t.Helper()
	c, err := AsPerson(files, g.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A client's requests run at most maxInFlight at a time, however many are
// asked for at once, and every one of them is counted: one tree, then one
// blob per distinct file.
func TestClientBoundsRequestsInFlight(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	const want = 100
	for i := range want {
		g.set(repoFull, "x"+strconv.Itoa(i), "file "+strconv.Itoa(i)+"\n")
	}
	c := g.client(t, NewFiles(0), aliceToken)
	var wg sync.WaitGroup
	for i := range want {
		wg.Go(func() {
			if got, err := ReadFile(context.Background(), c, repoOwner, repoName, "x"+strconv.Itoa(i)); err != nil || got != "file "+strconv.Itoa(i)+"\n" {
				t.Errorf("x%d: %q, %v", i, got, err)
			}
		})
	}
	wg.Wait()
	if p := g.peak.Load(); p > maxInFlight || p < 2 {
		t.Fatalf("peak in flight %d, bound %d", p, maxInFlight)
	}
	if n := c.Requests(); n != want+1 {
		t.Fatalf("%d requests counted for %d files and one tree", n, want)
	}
	if g.treeRequests.Load() != 1 || g.blobRequests.Load() != want {
		t.Fatalf("tree requests %d, blob requests %d", g.treeRequests.Load(), g.blobRequests.Load())
	}
}

// One call reads a repository's tree once, however many files it asks for;
// existence costs nothing beyond it; an absent file is ErrNotFound and a
// directory refused.
func TestOneCallReadsTheTreeOnce(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.set(repoFull, "a/one.yaml", oneYAML)
	g.set(repoFull, "a/two.yaml", "two: 2\n")
	c := g.client(t, NewFiles(0), aliceToken)
	ctx := context.Background()
	if got, err := ReadFile(ctx, c, repoOwner, repoName, "a/one.yaml"); err != nil || got != oneYAML {
		t.Fatalf("one: %q, %v", got, err)
	}
	if got, err := ReadFile(ctx, c, repoOwner, repoName, "a/two.yaml"); err != nil || got != "two: 2\n" {
		t.Fatalf("two: %q, %v", got, err)
	}
	if ok, err := FileExists(ctx, c, repoOwner, repoName, "a/two.yaml"); err != nil || !ok {
		t.Fatalf("two exists: %v, %v", ok, err)
	}
	if ok, err := FileExists(ctx, c, repoOwner, repoName, "a/three.yaml"); err != nil || ok {
		t.Fatalf("three exists: %v, %v", ok, err)
	}
	if _, err := ReadFile(ctx, c, repoOwner, repoName, "a/three.yaml"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("three: %v", err)
	}
	if _, err := ReadFile(ctx, c, repoOwner, repoName, "a"); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("a: %v", err)
	}
	if g.treeRequests.Load() != 1 || g.blobRequests.Load() != 2 || c.Requests() != 3 {
		t.Fatalf("tree %d blob %d counted %d", g.treeRequests.Load(), g.blobRequests.Load(), c.Requests())
	}
}

// The next call validates the tree with a conditional request — a 304, which
// costs no quota — and reads every unchanged file from the cache; a file
// that changed costs its blob and nothing else; a repository that changed
// costs its tree.
func TestTheNextCallCostsOnlyWhatChanged(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.set(repoFull, "one.yaml", oneYAML)
	g.set(repoFull, "two.yaml", "two: 2\n")
	files := NewFiles(0)
	ctx := context.Background()
	readBoth := func(wantTwo string) *Client {
		c := g.client(t, files, aliceToken)
		if got, err := ReadFile(ctx, c, repoOwner, repoName, "one.yaml"); err != nil || got != oneYAML {
			t.Fatalf("one: %q, %v", got, err)
		}
		if got, err := ReadFile(ctx, c, repoOwner, repoName, "two.yaml"); err != nil || got != wantTwo {
			t.Fatalf("two: %q, %v", got, err)
		}
		return c
	}
	readBoth("two: 2\n")
	if g.treeFull.Load() != 1 || g.blobRequests.Load() != 2 {
		t.Fatalf("cold: tree 200s %d, blobs %d", g.treeFull.Load(), g.blobRequests.Load())
	}
	c := readBoth("two: 2\n")
	if g.treeRequests.Load() != 2 || g.treeFull.Load() != 1 || g.blobRequests.Load() != 2 || c.Requests() != 1 || c.Charged() != 0 {
		t.Fatalf("warm: tree requests %d (200s %d), blobs %d, the call made %d and was charged %d", g.treeRequests.Load(), g.treeFull.Load(), g.blobRequests.Load(), c.Requests(), c.Charged())
	}
	g.set(repoFull, "two.yaml", "two: 3\n")
	c = readBoth("two: 3\n")
	if g.treeFull.Load() != 2 || g.blobRequests.Load() != 3 || c.Requests() != 2 || c.Charged() != 2 {
		t.Fatalf("changed: tree 200s %d, blobs %d, the call made %d and was charged %d", g.treeFull.Load(), g.blobRequests.Load(), c.Requests(), c.Charged())
	}
}

// Within the freshness window a person's next call skips the validation;
// another person's call validates as them; the window ends with time, and
// early with Invalidate.
func TestFreshnessIsThePersonsAlone(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.grant(bobToken, repoFull)
	g.set(repoFull, "one.yaml", oneYAML)
	files := NewFiles(time.Minute)
	clock := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	files.now = func() time.Time { return clock }
	ctx := context.Background()
	read := func(token string) {
		c := g.client(t, files, token)
		if got, err := ReadFile(ctx, c, repoOwner, repoName, "one.yaml"); err != nil || got != oneYAML {
			t.Fatalf("%s: %q, %v", token, got, err)
		}
	}
	read(aliceToken)
	read(aliceToken)
	if g.treeRequests.Load() != 1 {
		t.Fatalf("alice's second call within the window validated: %d tree requests", g.treeRequests.Load())
	}
	read(bobToken)
	if g.treeRequests.Load() != 2 || g.treeFull.Load() != 1 {
		t.Fatalf("bob's first call did not validate as bob: %d tree requests, %d 200s", g.treeRequests.Load(), g.treeFull.Load())
	}
	clock = clock.Add(2 * time.Minute)
	read(aliceToken)
	if g.treeRequests.Load() != 3 {
		t.Fatalf("alice's call after the window did not validate: %d tree requests", g.treeRequests.Load())
	}
	files.Invalidate(repoFull)
	read(aliceToken)
	if g.treeRequests.Load() != 4 {
		t.Fatalf("alice's call after Invalidate did not validate: %d tree requests", g.treeRequests.Load())
	}
	if g.blobRequests.Load() != 1 {
		t.Fatalf("the unchanged blob was fetched %d times", g.blobRequests.Load())
	}
}

// A person who may not read a repository gets ErrNotFound, however warm the
// cache is from another person's reads: the tree request as them is the
// permission check.
func TestACachedRepositoryStaysInvisibleToAPersonWithoutAccess(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.set(repoFull, "one.yaml", oneYAML)
	files := NewFiles(time.Minute)
	ctx := context.Background()
	if _, err := ReadFile(ctx, g.client(t, files, aliceToken), repoOwner, repoName, "one.yaml"); err != nil {
		t.Fatal(err)
	}
	_, err := ReadFile(ctx, g.client(t, files, bobToken), repoOwner, repoName, "one.yaml")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob read a repository he may not see: %v", err)
	}
	if _, err := FileExists(ctx, g.client(t, files, bobToken), repoOwner, repoName, "one.yaml"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob's existence check: %v", err)
	}
}

// A spent quota is ErrRateLimited naming the reset, for the tool's error.
func TestASpentQuotaNamesTheReset(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.set(repoFull, "one.yaml", oneYAML)
	g.mu.Lock()
	g.rateLimited, g.reset = true, time.Now().Add(17*time.Minute).Truncate(time.Second)
	g.mu.Unlock()
	_, err := ReadFile(context.Background(), g.client(t, NewFiles(0), aliceToken), repoOwner, repoName, "one.yaml")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("not rate limited: %v", err)
	}
	if !strings.Contains(err.Error(), "spent until "+g.reset.UTC().Format("15:04 UTC")) || !strings.Contains(err.Error(), "5000 requests per hour") || !strings.Contains(err.Error(), "min)") {
		t.Fatalf("the reset is not named: %v", err)
	}
	if errors.Is(err, ErrForbidden) {
		t.Fatalf("a spent quota is not a forbidden repository: %v", err)
	}
}

// A tree GitHub truncates is an error naming it, never a partial listing.
func TestATruncatedTreeIsRefused(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.set(repoFull, "one.yaml", oneYAML)
	g.mu.Lock()
	g.truncated = true
	g.mu.Unlock()
	_, err := FileExists(context.Background(), g.client(t, NewFiles(0), aliceToken), repoOwner, repoName, "one.yaml")
	if err == nil || !strings.Contains(err.Error(), "truncates the tree") {
		t.Fatalf("truncated: %v", err)
	}
}

// The blob cache is bounded: beyond its bytes the least recently used
// content goes and is fetched again when asked for.
func TestBlobsAreEvictedLeastRecentlyUsed(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.set(repoFull, "a", strings.Repeat("a", 10))
	g.set(repoFull, "b", strings.Repeat("b", 10))
	g.set(repoFull, "c", strings.Repeat("c", 10))
	files := NewFiles(0)
	files.maxBytes = 25
	ctx := context.Background()
	c := g.client(t, files, aliceToken)
	for _, p := range []string{"a", "b", "a", "c"} {
		if _, err := ReadFile(ctx, c, repoOwner, repoName, p); err != nil {
			t.Fatal(err)
		}
	}
	// a, b, c fetched once each; c's arrival evicted b, the least recently used.
	if g.blobRequests.Load() != 3 {
		t.Fatalf("%d blob requests for three files", g.blobRequests.Load())
	}
	if _, err := ReadFile(ctx, c, repoOwner, repoName, "a"); err != nil {
		t.Fatal(err)
	}
	if g.blobRequests.Load() != 3 {
		t.Fatalf("a was evicted: %d blob requests", g.blobRequests.Load())
	}
	if _, err := ReadFile(ctx, c, repoOwner, repoName, "b"); err != nil {
		t.Fatal(err)
	}
	if g.blobRequests.Load() != 4 {
		t.Fatalf("b was not fetched again after its eviction: %d blob requests", g.blobRequests.Load())
	}
}

// A file read at a ref other than HEAD reads that ref's tree.
func TestReadFileAtReadsTheRefsTree(t *testing.T) {
	g := newFakeGitHub(t)
	g.grant(aliceToken, repoFull)
	g.set(repoFull, "one.yaml", oneYAML)
	c := g.client(t, NewFiles(0), aliceToken)
	ctx := context.Background()
	if _, err := ReadFileAt(ctx, c, repoOwner, repoName, "one.yaml", "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(ctx, c, repoOwner, repoName, "one.yaml"); err != nil {
		t.Fatal(err)
	}
	if g.treeRequests.Load() != 2 || g.blobRequests.Load() != 1 {
		t.Fatalf("two refs, one content: tree requests %d, blob requests %d", g.treeRequests.Load(), g.blobRequests.Load())
	}
}
