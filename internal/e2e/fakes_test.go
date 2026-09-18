package e2e

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeGitHub answers the calls of the identity chain and the registry reads:
// GET /user as the person — the bearer verification — and GET
// /repos/{owner}/{repo}/contents/{path} for the fixture repositories. A
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

func newFakeGitHub(t *testing.T, logins map[string]string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{logins: logins, files: map[string]map[string]string{}, forbidden: map[string]bool{}, contentsCalls: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		g.userCalls.Add(1)
		login, ok := g.logins[bearer(r)]
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"login": login, "id": userID(login)})
	})
	mux.HandleFunc("GET /api/v3/repos/{owner}/{repo}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := g.logins[bearer(r)]; !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials"})
			return
		}
		repo := r.PathValue("owner") + "/" + r.PathValue("repo")
		p := r.PathValue("path")
		g.mu.Lock()
		defer g.mu.Unlock()
		g.contentsCalls[repo+":"+p]++
		if g.forbidden[repo] {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Resource not accessible by integration"})
			return
		}
		content, ok := g.files[repo][p]
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
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
