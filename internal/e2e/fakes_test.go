package e2e

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
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

// message is the key of GitHub's error bodies.
const message = "message"

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
		writeJSON(w, http.StatusOK, map[string]any{"login": login, "id": userID(login)})
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
		writeJSON(w, http.StatusOK, map[string]any{"type": "file", "encoding": "base64", "name": path.Base(p), "path": p,
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
		if !ok {
			status = expectedAnswer(host, r)
		}
		if status == http.StatusFound {
			w.Header().Set("Location", "https://"+host+"/login")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(f.Close)
	return f
}

// expectedAnswer is what a healthy installation answers an anonymous probe.
func expectedAnswer(host string, r *http.Request) int {
	switch {
	case strings.HasPrefix(host, "dex.") && r.URL.Path == "/auth" && r.URL.Query().Get("client_id") != "":
		return http.StatusFound
	case strings.HasPrefix(host, "kagent."):
		return http.StatusFound
	case r.URL.Path == "/.well-known/oauth-protected-resource":
		return http.StatusOK
	}
	return http.StatusNotFound
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
