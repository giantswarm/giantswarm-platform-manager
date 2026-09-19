package live

import (
	"errors"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// A kubernetes tool's refusal is mapped to what the verify shows: muster's
// auth_required for the installation with its sign-in URL, the apiserver's
// forbidden naming the person, an object that does not exist, anything else
// as it was said.
func TestClassifyMapsRefusals(t *testing.T) {
	err := classify("auth_required: server 'gopher-mcp-kubernetes' requires authentication before its tools can be called (this session is not authenticated to it).\n\nPlease sign in to connect to this server:\n\nhttps://muster.example.test/oauth/proxy/start?state=abc")
	var auth *verify.AuthRequired
	if !errors.As(err, &auth) || auth.Server != "gopher-mcp-kubernetes" || auth.URL != "https://muster.example.test/oauth/proxy/start?state=abc" {
		t.Errorf("auth_required: %#v", err)
	}
	err = classify(`Failed to get resource: secrets "kagent-oauth2-proxy-credentials" is forbidden: User "oidc:viewer@lab.local" cannot get resource "secrets" in API group "" in the namespace "kagent" (impersonating user=viewer@lab.local)`)
	var forbidden *verify.Forbidden
	if !errors.As(err, &forbidden) || forbidden.Person != "oidc:viewer@lab.local" {
		t.Errorf("forbidden: %#v", err)
	}
	err = classify(`Failed to get resource: helmreleases.helm.toolkit.fluxcd.io "kagent" not found`)
	if !errors.Is(err, verify.ErrNotFound) {
		t.Errorf("not found: %#v", err)
	}
	err = classify("Failed to get resource: the server is currently unable to handle the request")
	if errors.Is(err, verify.ErrNotFound) || errors.As(err, &forbidden) || errors.As(err, &auth) {
		t.Errorf("anything else stays an error: %#v", err)
	}
}

func TestKindArgsSplitKindAndGroup(t *testing.T) {
	for _, c := range []struct{ in, kind, group string }{
		{"HelmRelease", "helmrelease", ""},
		{"Cluster.postgresql.cnpg.io", "cluster", "postgresql.cnpg.io"},
		{"MCPServer.muster.giantswarm.io", "mcpserver", "muster.giantswarm.io"},
	} {
		kind, group := kindArgs(c.in)
		if kind != c.kind || group != c.group {
			t.Errorf("%s: %s %s", c.in, kind, group)
		}
	}
}

func TestConfigValidateNamesTheMissingField(t *testing.T) {
	cfg := Config{Path: "/mcp/live", Issuer: "https://dex.example.test/dex", Audience: "agent-platform", MusterURL: "http://muster:8090/mcp", KubernetesFamily: "kubernetes"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		mut  func(*Config)
	}{
		{"issuer", func(c *Config) { c.Issuer = "" }},
		{"audience", func(c *Config) { c.Audience = "" }},
		{"muster URL", func(c *Config) { c.MusterURL = "not a url" }},
		{"kubernetes family", func(c *Config) { c.KubernetesFamily = "" }},
		{"path", func(c *Config) { c.Path = "mcp" }},
	} {
		bad := cfg
		c.mut(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", c.name, bad)
		}
	}
}
