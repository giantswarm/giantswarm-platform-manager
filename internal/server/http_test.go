package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"
)

// The probes answer 200 without OAuth and with it; the guard covers the MCP
// path only (the identity chain in internal/e2e proves the guard itself).
func TestProbesAreOpen(t *testing.T) {
	for _, cfg := range []Config{
		{Addr: "127.0.0.1:0"},
		{Addr: "127.0.0.1:0", OAuth: &OAuthConfig{BaseURL: "http://manager.test:8080", AuthorizationServer: DefaultAuthorizationServer}},
	} {
		s, err := New(cfg, mcpserver.NewMCPServer("test", "0"), nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/healthz", "/readyz"} {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s (oauth %v): %d, want 200", path, cfg.OAuth != nil, rec.Code)
			}
		}
	}
}

// An OAuth configuration without its URLs is refused at start, not at the
// first request.
func TestOAuthConfigIsValidatedAtStart(t *testing.T) {
	if _, err := New(Config{OAuth: &OAuthConfig{AuthorizationServer: DefaultAuthorizationServer}}, mcpserver.NewMCPServer("test", "0"), nil); err == nil {
		t.Fatal("a missing base URL was accepted")
	}
}
