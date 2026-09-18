package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeGitHub answers the one call of the scaffold's identity chain: GET /user
// as the person — the bearer verification. Every other path is 404.
type fakeGitHub struct {
	*httptest.Server
	logins map[string]string // person user token → login
	// userCalls counts GET /user: the server under test caches a verified
	// bearer, so the count proves the cache.
	userCalls atomic.Int64
}

func newFakeGitHub(t *testing.T, logins map[string]string) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{logins: logins}
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
	g.Server = httptest.NewServer(mux)
	t.Cleanup(g.Close)
	return g
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
