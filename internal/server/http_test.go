package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// A body beyond the bound is refused, and the endpoint keeps answering.
func TestMCPBodyIsBounded(t *testing.T) {
	s, err := New(Config{Addr: "127.0.0.1:0"}, mcpserver.NewMCPServer("test", "0"), nil)
	if err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", maxMCPBodyBytes) + `"}}`
	if code := post(big); code < 400 {
		t.Errorf("MCP body over the bound: %d, want a refusal", code)
	}
	initialize := `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	if code := post(initialize); code != http.StatusOK {
		t.Errorf("MCP initialize after a refusal: %d, want 200", code)
	}
}
