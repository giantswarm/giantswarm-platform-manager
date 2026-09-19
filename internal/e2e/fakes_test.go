package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"
)

// fakeGitHub answers the calls of the identity chain and the registry reads:
// GET /user as the person — the bearer verification — and GET
// /repos/{owner}/{repo}/contents/{path} for the fixture repositories, and
// GET /repos/{owner}/{repo} naming their default branch. A
// repository that is not a fixture is 404 (as GitHub answers for one the
// person may not see), one in forbidden is 403, a missing file 404. Every
// other path is 404.
type fakeGitHub struct {
	*httptest.Server
	logins map[string]string // person user token → login
	// userCalls counts GET /user: the server under test caches a verified
	// bearer, so the count proves the cache.
	userCalls atomic.Int64

	mu        sync.Mutex
	files     map[string]map[string]string // owner/repo → path → content
	forbidden map[string]bool
	// contentsCalls counts the content reads per owner/repo:path.
	contentsCalls map[string]int
}

// message is the key of GitHub's error bodies; nameKey the name of a file
// or an object in the fakes' documents.
const (
	message = "message"
	nameKey = "name"
)

// defaultBranch is every fixture repository's default branch.
const defaultBranch = "main"

// badCredentials is GitHub's message for a bearer it does not know.
const badCredentials = "Bad credentials"

func newFakeGitHub(t *testing.T, logins map[string]string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{logins: logins, files: map[string]map[string]string{}, forbidden: map[string]bool{}, contentsCalls: map[string]int{}}
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
			writeJSON(w, http.StatusForbidden, map[string]any{message: "Resource not accessible by integration"})
		case g.files[repo] == nil:
			writeJSON(w, http.StatusNotFound, map[string]any{message: "Not Found"})
		default:
			writeJSON(w, http.StatusOK, map[string]any{"full_name": repo, "default_branch": defaultBranch})
		}
	})
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.logins[bearer(r)]; !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{message: badCredentials})
			return
		}
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		p := r.PathValue("path")
		g.mu.Lock()
		defer g.mu.Unlock()
		g.contentsCalls[repo+":"+p]++
		if g.forbidden[repo] {
			writeJSON(w, http.StatusForbidden, map[string]any{message: "Resource not accessible by integration"})
			return
		}
		content, ok := g.files[repo][p]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{message: "Not Found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{typeKey: "file", "encoding": "base64", nameKey: path.Base(p), "path": p,
			"content": base64.StdEncoding.EncodeToString([]byte(content))})
	})
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
}

// addRepo makes owner/repo a fixture with files.
func (g *fakeGitHub) addRepo(repo string, files map[string]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.files[repo] = files
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

// reads is how often owner/repo:path was read.
func (g *fakeGitHub) reads(repo, p string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.contentsCalls[repo+":"+p]
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

// expectedAnswer is what a healthy installation answers an anonymous probe:
// the status, the Location of a redirect, the body.
func expectedAnswer(host string, r *http.Request) (int, string, string) {
	switch {
	case strings.HasPrefix(host, "dex.") && r.URL.Path == "/auth" && r.URL.Query().Get("client_id") != "":
		return http.StatusFound, "", ""
	case strings.HasPrefix(host, "kagent.") && r.URL.Path == "/api/agents":
		return http.StatusForbidden, "", ""
	case strings.HasPrefix(host, "kagent.") && r.URL.Path == "/oauth2/start":
		return http.StatusFound, "https://dex." + strings.TrimPrefix(host, "kagent.") + "/auth?client_id=kagent&response_type=code", ""
	case strings.HasPrefix(host, "kagent."):
		return http.StatusFound, "", ""
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
	return r.Remote.Close(ctx, pr, deleteBranch)
}

func (r asRemote) Merge(ctx context.Context, pr commit.PullRequest) error {
	r.st.record(r.login, commit.OpMerge, pr)
	return r.Remote.Merge(ctx, pr)
}
