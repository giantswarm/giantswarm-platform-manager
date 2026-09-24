package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/giantswarm/gitops-commit/commit"
)

// fakeGitHub answers the calls of the identity chain and the registry reads:
// GET /user as the person — the bearer verification — and GET
// /repos/{owner}/{repo}/contents/{path} for the fixture repositories, GET
// /repos/{owner}/{repo} naming their default branch, and GET
// /repos/{owner}/{repo}/pulls/{number} for the pull requests the in-process
// remote holds (the stack's pull hook answers them from the remote's state
// and its record of who merged or closed each one). A repository that is
// not a fixture is 404 (as GitHub answers for one the person may not see),
// one in forbidden is 403, a missing file or pull request 404. Every other
// path is 404. A merge on the remote is mirrored into the store (mirror), so
// the default branch reads as GitHub would have it after the merge.
type fakeGitHub struct {
	*httptest.Server
	logins map[string]string // person user token → login
	// userCalls counts GET /user: the server under test caches a verified
	// bearer, so the count proves the cache.
	userCalls atomic.Int64
	// pull answers a pull request of owner/repo by number, as the remote
	// has it; nil answers none.
	pull func(repo string, number int) (fakePull, bool)

	mu        sync.Mutex
	files     map[string]map[string]string // owner/repo → path → content
	forbidden map[string]bool
	// contentsCalls counts the blob reads per owner/repo:path — the files
	// the server under test fetched by content, not the ones it answered
	// from a tree or its cache.
	contentsCalls map[string]int
	// treeCalls counts the tree requests per owner/repo GitHub answered with
	// a listing (200); treeChecks every one, the conditional 304s included.
	treeCalls, treeChecks map[string]int
}

// fakePull is a pull request as the fake GitHub answers it.
type fakePull struct {
	Merged, Closed bool
	HeadSHA        string
	MergeCommit    string
	MergedBy       string
	At             time.Time
	URL            string
}

// message is the key of GitHub's error bodies; nameKey the name of a file
// or an object in the fakes' documents.
const (
	message = "message"
	nameKey = "name"
	shaKey  = "sha"
)

// defaultBranch is every fixture repository's default branch.
const defaultBranch = "main"

// GitHub's messages: for a bearer it does not know, for a repository the
// App is not installed on, for a file or pull request that is not there.
const (
	badCredentials = "Bad credentials"
	notAccessible  = "Resource not accessible by integration"
	notFound       = "Not Found"
)

func newFakeGitHub(t *testing.T, logins map[string]string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{logins: logins, files: map[string]map[string]string{}, forbidden: map[string]bool{}, contentsCalls: map[string]int{}, treeCalls: map[string]int{}, treeChecks: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		g.userCalls.Add(1)
		login, ok := g.logins[bearer(r)]
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{message: badCredentials})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"login": login, "id": userID(login), "email": login + "@example.test"})
	})
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.logins[bearer(r)]; !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{message: badCredentials})
			return
		}
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case g.forbidden[repo]:
			writeJSON(w, http.StatusForbidden, map[string]any{message: notAccessible})
		case g.files[repo] == nil:
			writeJSON(w, http.StatusNotFound, map[string]any{message: notFound})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"full_name": repo, "default_branch": defaultBranch})
		}
	})
	// The Contents API is what the manager read files with before it read
	// trees; a call to it is a regression.
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the Contents API was called for %s/%s:%s; the manager reads repositories as trees", r.PathValue("owner"), r.PathValue("repo"), r.PathValue("path"))
		writeJSON(w, http.StatusInternalServerError, map[string]any{message: "the fake serves no Contents API"})
	})
	// The tree of owner/repo at a ref — HEAD for every fixture, whose files
	// are its default branch — with a weak ETag over the listing, answered
	// 304 to a matching If-None-Match as GitHub does; a repository that is
	// not a fixture is 404, one in forbidden 403.
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/git/trees/{ref}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.logins[bearer(r)]; !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{message: badCredentials})
			return
		}
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		g.mu.Lock()
		defer g.mu.Unlock()
		g.treeChecks[repo]++
		if g.forbidden[repo] {
			writeJSON(w, http.StatusForbidden, map[string]any{message: notAccessible})
			return
		}
		files, ok := g.files[repo]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{message: notFound})
			return
		}
		body, etag := treeJSON(files)
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		g.treeCalls[repo]++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
	// A blob by SHA: the content of every file of owner/repo with that id,
	// counted against each such path.
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/git/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.logins[bearer(r)]; !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{message: badCredentials})
			return
		}
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.forbidden[repo] {
			writeJSON(w, http.StatusForbidden, map[string]any{message: notAccessible})
			return
		}
		var found *string
		for p, content := range g.files[repo] {
			if blobSHA(content) == r.PathValue("sha") {
				g.contentsCalls[repo+":"+p]++
				found = &content
			}
		}
		if found == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{message: notFound})
			return
		}
		_, _ = w.Write([]byte(*found))
	})
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/pulls/{number}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.logins[bearer(r)]; !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{message: badCredentials})
			return
		}
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		number, _ := strconv.Atoi(r.PathValue("number"))
		g.mu.Lock()
		forbidden, pull := g.forbidden[repo], g.pull
		g.mu.Unlock()
		if forbidden {
			writeJSON(w, http.StatusForbidden, map[string]any{message: notAccessible})
			return
		}
		var p fakePull
		ok := false
		if pull != nil {
			p, ok = pull(repo, number)
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{message: notFound})
			return
		}
		doc := map[string]any{"number": number, "state": "open", "merged": false, "html_url": p.URL, "head": map[string]any{shaKey: p.HeadSHA}}
		switch {
		case p.Merged:
			doc["state"], doc["merged"], doc["merge_commit_sha"] = "closed", true, p.MergeCommit
			doc["merged_at"], doc["closed_at"] = p.At.UTC().Format(time.RFC3339), p.At.UTC().Format(time.RFC3339)
			if p.MergedBy != "" {
				doc["merged_by"] = map[string]any{"login": p.MergedBy, "id": userID(p.MergedBy)}
			}
		case p.Closed:
			doc["state"], doc["closed_at"] = "closed", p.At.UTC().Format(time.RFC3339)
		}
		writeJSON(w, http.StatusOK, doc)
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// mirror brings the store of owner/repo from the tree before a merge to the
// tree after it — the files the merge added or changed set, the ones it
// removed deleted — leaving every other file of the store as it is.
func (g *fakeGitHub) mirror(repo string, before, after map[string][]byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.files[repo] == nil {
		g.files[repo] = map[string]string{}
	}
	for p, content := range after {
		if was, ok := before[p]; !ok || string(was) != string(content) {
			g.files[repo][p] = string(content)
		}
	}
	for p := range before {
		if _, kept := after[p]; !kept {
			delete(g.files[repo], p)
		}
	}
}

// addRepo makes owner/repo a fixture with files.
func (g *fakeGitHub) addRepo(repo string, files map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.files[repo] = files
}

// file is the content of p in the fixture owner/repo, when it has it.
func (g *fakeGitHub) file(repo, p string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	content, ok := g.files[repo][p]
	return content, ok
}

// addFile adds one file to the fixture owner/repo.
func (g *fakeGitHub) addFile(repo, p, content string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.files[repo][p] = content
}

// repos are the fixture repositories with their files, for seeding a remote.
func (g *fakeGitHub) repos() map[string]map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := map[string]map[string]string{}
	for repo, files := range g.files {
		out[repo] = maps.Clone(files)
	}
	return out
}

// forbid makes every read of owner/repo a 403.
func (g *fakeGitHub) forbid(repo string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.forbidden[repo] = true
}

// has says whether the store of owner/repo carries path.
func (g *fakeGitHub) has(repo, p string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.files[repo][p]
	return ok
}

// reads is how often owner/repo:path was read by content.
func (g *fakeGitHub) reads(repo, p string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.contentsCalls[repo+":"+p]
}

// trees is how often owner/repo's tree was answered with a listing, and how
// often it was asked for at all (the conditional 304s included).
func (g *fakeGitHub) trees(repo string) (listed, checked int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.treeCalls[repo], g.treeChecks[repo]
}

// treeJSON is GitHub's recursive tree of files — the blobs and the
// directories above them — with a weak ETag over the listing.
func treeJSON(files map[string]string) ([]byte, string) {
	dirs := map[string]bool{}
	entries := make([]map[string]any, 0, len(files))
	for p, content := range files {
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			dirs[strings.Join(parts[:i], "/")] = true
		}
		entries = append(entries, map[string]any{"path": p, "mode": "100644", typeKey: "blob", shaKey: blobSHA(content), "size": len(content)})
	}
	for d := range dirs {
		entries = append(entries, map[string]any{"path": d, "mode": "040000", typeKey: "tree", shaKey: blobSHA(d)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["path"].(string) < entries[j]["path"].(string) })
	body, _ := json.Marshal(map[string]any{shaKey: blobSHA(fmt.Sprint(entries)), "truncated": false, "tree": entries})
	sum := sha256.Sum256(body)
	return body, `W/"` + hex.EncodeToString(sum[:]) + `"`
}

// blobSHA is the fake's blob id of content: a hash of it, as git's is. The
// server under test takes the id from the tree and never inspects its form.
func blobSHA(content string) string {
	sum := sha256.Sum256([]byte("blob " + strconv.Itoa(len(content)) + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

// userID is a login's stable numeric id in the fake.
func userID(login string) int64 {
	var n int64
	for _, c := range login {
		n = n*31 + int64(c)
	}
	return n
}

func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// addFiles adds files to the fixture owner/repo, over the ones it has.
func (g *fakeGitHub) addFiles(repo string, files map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.files[repo] == nil {
		g.files[repo] = map[string]string{}
	}
	for p, c := range files {
		g.files[repo][p] = c
	}
}

// probeHostHeader carries the host a probe was addressed to through the
// rewrite to the fake.
const probeHostHeader = "X-Probe-Host"

// fakeProbes stands in for every public endpoint the anonymous probes reach:
// a request to any host lands here, answered as the definition expects unless
// a test set an answer for host+path.
type fakeProbes struct {
	*httptest.Server
	mu      sync.Mutex
	answers map[string]int
}

// restore takes the answer set for host+path away: the probe answers as
// expected again (the cause of a red probe fixed).
func (f *fakeProbes) restore(hostPath string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.answers, hostPath)
}

func newFakeProbes(t *testing.T) *fakeProbes {
	t.Helper()
	f := &fakeProbes{answers: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Header.Get(probeHostHeader)
		f.mu.Lock()
		status, ok := f.answers[host+r.URL.Path]
		f.mu.Unlock()
		location, body := "", ""
		if !ok {
			status, location, body = expectedAnswer(host, r)
		}
		if status == http.StatusFound && location == "" {
			location = "https://" + host + "/login"
		}
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) // #nosec G705 -- a test double answering fixture text
	}))
	t.Cleanup(f.Close)
	return f
}

// fakeDexConnector is the one connector of every installation's Dex here.
const fakeDexConnector = "github"

// expectedAnswer is what a healthy installation answers an anonymous probe:
// the status, the Location of a redirect, the body. Dex has one connector:
// /auth redirects to it with the request's query, and the connector answers
// with a redirect to the identity provider.
func expectedAnswer(host string, r *http.Request) (int, string, string) {
	switch {
	case strings.HasPrefix(host, "dex.") && r.URL.Path == "/auth" && r.URL.Query().Get("client_id") != "":
		return http.StatusFound, "/auth/" + fakeDexConnector + "?" + r.URL.RawQuery, ""
	case strings.HasPrefix(host, "dex.") && r.URL.Path == "/auth/"+fakeDexConnector && r.URL.Query().Get("client_id") != "":
		return http.StatusFound, "https://github.example/login/oauth/authorize", ""
	case strings.HasPrefix(host, "kagent.") && r.URL.Path == "/api/agents":
		return http.StatusForbidden, "", ""
	case strings.HasPrefix(host, "kagent.") && r.URL.Path == "/oauth2/start":
		return http.StatusFound, "https://dex." + strings.TrimPrefix(host, "kagent.") + "/auth?client_id=kagent&response_type=code", ""
	case strings.HasPrefix(host, "kagent."):
		return http.StatusFound, "", ""
	case strings.HasPrefix(host, "portal.") && r.URL.Path == "/":
		return http.StatusOK, "", ""
	case strings.HasPrefix(host, "portal.") && strings.HasPrefix(r.URL.Path, "/api/auth/oidc-") && strings.HasSuffix(r.URL.Path, "/start"):
		return http.StatusFound, "https://dex." + strings.TrimPrefix(host, "portal.") + "/auth?client_id=dev-portal&response_type=code", ""
	case r.URL.Path == "/.well-known/oauth-protected-resource":
		return http.StatusOK, "", `{"resource":"https://` + host + `/mcp","authorization_servers":["https://` + host + `"]}`
	}
	return http.StatusNotFound, "", ""
}

// answer makes host+path answer status.
func (f *fakeProbes) answer(hostPath string, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[hostPath] = status
}

// client is the HTTP client the manager under test probes with: every
// request is rewritten to the fake, redirects are not followed.
func (f *fakeProbes) client() *http.Client {
	return &http.Client{Transport: rewriteTransport{target: f.URL},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	r2 := req.Clone(req.Context())
	r2.Header.Set(probeHostHeader, req.URL.Host)
	r2.URL.Scheme, r2.URL.Host, r2.Host = u.Scheme, u.Host, u.Host
	return http.DefaultTransport.RoundTrip(r2)
}

// fixtureSA is the projected ServiceAccount token the manager under test
// reads from its token file and the fake gateway accepts.
const fixtureSA = "sa-fixture-for-klaus-gateway"

// errorKey is the key of the fake gateway's error bodies.
const errorKey = "error"

// fakeReview is one review the fake gateway received, as the manager sent it.
type fakeReview struct {
	ID      string
	Body    map[string]any
	Results []map[string]any
}

// fakeGateway stands in for klaus-gateway's team-review endpoint: POST
// /reviews answers a receipt, POST /reviews/{id}/results appends to the
// review's thread — 404 for a review it does not hold (forgotten, as after a
// restart on the memory store). Any other bearer than fixtureSA is 401.
type fakeGateway struct {
	*httptest.Server
	mu      sync.Mutex
	reviews []*fakeReview
	next    int
	// down makes the results endpoint answer 502, as a gateway Slack refuses.
	down bool
}

// setDown turns the results endpoint's failure on or off.
func (g *fakeGateway) setDown(down bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.down = down
}

func newFakeGateway(t *testing.T) *fakeGateway {
	t.Helper()
	g := &fakeGateway{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /reviews", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != fixtureSA {
			writeJSON(w, http.StatusUnauthorized, map[string]any{errorKey: "unauthorized"})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{errorKey: err.Error()})
			return
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		g.next++
		rv := &fakeReview{ID: "rev-" + strconv.Itoa(g.next), Body: body}
		g.reviews = append(g.reviews, rv)
		receipt := map[string]any{"id": rv.ID, "channel": body["channel"], "ts": "1700000000.000100"}
		if body["noticeChannel"] != nil {
			receipt["notice_ts"] = "1700000000.000099"
		}
		writeJSON(w, http.StatusCreated, receipt)
	})
	mux.HandleFunc("POST /reviews/{id}/results", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != fixtureSA {
			writeJSON(w, http.StatusUnauthorized, map[string]any{errorKey: "unauthorized"})
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.down {
			writeJSON(w, http.StatusBadGateway, map[string]any{errorKey: "slack refused"})
			return
		}
		for _, rv := range g.reviews {
			if rv.ID == r.PathValue("id") {
				rv.Results = append(rv.Results, body)
				writeJSON(w, http.StatusCreated, map[string]any{"id": rv.ID, "channel": rv.Body["channel"], "ts": "1700000000.000200"})
				return
			}
		}
		writeJSON(w, http.StatusNotFound, map[string]any{errorKey: "no such review"})
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// posted returns every review received, oldest first.
func (g *fakeGateway) posted() []fakeReview {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]fakeReview, 0, len(g.reviews))
	for _, rv := range g.reviews {
		out = append(out, *rv)
	}
	return out
}

// forget drops the gateway's record of review id, as a restart would.
func (g *fakeGateway) forget(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reviews = slices.DeleteFunc(g.reviews, func(rv *fakeReview) bool { return rv.ID == id })
}

// asRemote is the remote the manager under test opens with a person's token:
// gitops-commit's Fake, with the identity behind each approval, close and
// merge recorded on the stack — what "as the member" and "as the actor" mean.
type asRemote struct {
	commit.Remote
	login string
	st    *stack
}

func (r asRemote) Approve(ctx context.Context, pr commit.PullRequest, body string) error {
	r.st.record(r.login, commit.OpApprove, pr)
	return r.Remote.Approve(ctx, pr, body)
}

func (r asRemote) Close(ctx context.Context, pr commit.PullRequest, deleteBranch bool) error {
	r.st.record(r.login, commit.OpClose, pr)
	return r.st.close(ctx, r.Remote, pr, deleteBranch)
}

func (r asRemote) Merge(ctx context.Context, pr commit.PullRequest) error {
	r.st.record(r.login, commit.OpMerge, pr)
	return r.st.merge(ctx, r.Remote, pr, r.login)
}

// pullFacts is who merged or closed a pull request, and when — what GitHub
// knows and gitops-commit's Fake does not record.
type pullFacts struct {
	login string
	at    time.Time
}

func pullKey(repo commit.Repository, number int) string { return fmt.Sprintf("%s#%d", repo, number) }

// merge merges pr on remote as login — through the manager or outside it —
// and mirrors the merged tree into the fake GitHub's store, as GitHub would
// show the default branch after the merge, recording who merged when.
func (st *stack) merge(ctx context.Context, remote commit.Remote, pr commit.PullRequest, login string) error {
	before := st.remote.Files(pr.Repository, defaultBranch)
	if err := remote.Merge(ctx, pr); err != nil {
		return err
	}
	st.ghs.mirror(pr.Repository.String(), before, st.remote.Files(pr.Repository, defaultBranch))
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pulls[pullKey(pr.Repository, pr.Number)] = pullFacts{login: login, at: time.Now()}
	return nil
}

// close closes pr unmerged on remote, recording when.
func (st *stack) close(ctx context.Context, remote commit.Remote, pr commit.PullRequest, deleteBranch bool) error {
	if err := remote.Close(ctx, pr, deleteBranch); err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pulls[pullKey(pr.Repository, pr.Number)] = pullFacts{at: time.Now()}
	return nil
}

// pullState answers the fake GitHub's pull request read from the remote's
// state and the stack's facts: what a person merging or closing by hand
// leaves for the manager to read.
func (st *stack) pullState(repo string, number int) (fakePull, bool) {
	for _, pr := range st.remote.PullRequests() {
		if pr.Repository.String() != repo || pr.Number != number {
			continue
		}
		st.mu.Lock()
		facts := st.pulls[pullKey(pr.Repository, pr.Number)]
		st.mu.Unlock()
		p := fakePull{Merged: pr.Merged, Closed: pr.Closed, HeadSHA: pr.HeadSHA, MergedBy: facts.login, At: facts.at, URL: pr.URL}
		if pr.Merged {
			// The Fake fast-forwards the base: the merge commit is the head.
			p.MergeCommit = pr.HeadSHA
		}
		return p, true
	}
	return fakePull{}, false
}

// mergeOutside merges pr as login with the repository's own merge path —
// nothing tells the manager.
func (st *stack) mergeOutside(t *testing.T, pr commit.PullRequest, login string) {
	t.Helper()
	if err := st.merge(context.Background(), st.remote, pr, login); err != nil {
		t.Fatalf("merge %s#%d as %s: %v", pr.Repository, pr.Number, login, err)
	}
}

// closeOutside closes pr unmerged, outside the manager.
func (st *stack) closeOutside(t *testing.T, pr commit.PullRequest) {
	t.Helper()
	if err := st.close(context.Background(), st.remote, pr, true); err != nil {
		t.Fatalf("close %s#%d: %v", pr.Repository, pr.Number, err)
	}
}

// revertOutside reverts a merged pr by hand as login: the revert pull
// request opened and merged with the repository's own merge path, so the
// files the pull request added leave the default branch again.
func (st *stack) revertOutside(t *testing.T, pr commit.PullRequest, login string) {
	t.Helper()
	revert, err := st.remote.Revert(context.Background(), pr, "Reverted by hand.", nil)
	if err != nil {
		t.Fatalf("revert %s#%d: %v", pr.Repository, pr.Number, err)
	}
	st.mergeOutside(t, revert, login)
}
