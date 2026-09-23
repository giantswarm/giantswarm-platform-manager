package live

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

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
	// muster's refusal of a tool it does not list, and the apiserver's
	// unknown resource, are not an object that does not exist.
	for _, text := range []string{"tool not found: x_kubernetes_get", `unknown tool "x_kubernetes_list"`, "Failed to get resource: the server could not find the requested resource"} {
		if err := classify(text); errors.Is(err, verify.ErrNotFound) || errors.As(err, &forbidden) || errors.As(err, &auth) || !strings.Contains(err.Error(), strings.TrimSpace(text)) {
			t.Errorf("%q: %#v", text, err)
		}
	}
	if !toolNotFound(nil, errors.New("tool not found: x_kubernetes_get")) || !toolNotFound(mcp.NewToolResultError("tool not found: x_kubernetes_get"), nil) || toolNotFound(mcp.NewToolResultError(`pods "x" not found`), nil) || toolNotFound(mcp.NewToolResultText("ok"), nil) {
		t.Error("toolNotFound reads the call's error and the result's text")
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

// platformClient is the audience of the platform's own OAuth client.
const platformClient = "agent-platform"

func TestConfigValidateNamesTheMissingField(t *testing.T) {
	cfg := Config{Path: "/mcp/live", Issuer: "https://dex.example.test/dex", Audiences: []string{platformClient}, MusterURL: "http://muster:8090/mcp", KubernetesFamily: "kubernetes"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		mut  func(*Config)
	}{
		{"issuer", func(c *Config) { c.Issuer = "" }},
		{"audiences", func(c *Config) { c.Audiences = nil }},
		{"audiences all blank", func(c *Config) { c.Audiences = []string{"", " "} }},
		{"muster URL", func(c *Config) { c.MusterURL = "not a url" }},
		{"kubernetes family", func(c *Config) { c.KubernetesFamily = "" }},
		{"kubernetes member with an instance argument", func(c *Config) { c.KubernetesInstanceArg = "management_cluster"; c.KubernetesMember = "" }},
		{"kubernetes member that is no template", func(c *Config) {
			c.KubernetesInstanceArg = "management_cluster"
			c.KubernetesMember = "{{ .Installation"
		}},
		{"path", func(c *Config) { c.Path = "mcp" }},
		{"JWKS URL over plain http", func(c *Config) { c.JWKSURL = "http://dex.dex.svc.cluster.local:5556/dex/keys" }},
		{"JWKS URL without a host", func(c *Config) { c.JWKSURL = "https:///keys" }},
	} {
		bad := cfg
		c.mut(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: accepted %+v", c.name, bad)
		}
	}
}

// A key set is read over TLS only: a plain-http JWKS URL is refused when the
// configuration is read, naming the requirement, and never on the tokens; an
// https one — a private Service name included — is what the allowance is for.
func TestConfigValidateRefusesAPlainHTTPKeySet(t *testing.T) {
	cfg := Config{Path: "/mcp/live", Issuer: "https://dex.example.test/dex", Audiences: []string{platformClient}, MusterURL: "http://muster:8090/mcp", KubernetesFamily: "kubernetes",
		JWKSURL: "https://dex.dex.svc.cluster.local:5556/dex/keys", AllowPrivateIPJWKS: true}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("an https key set on a private name: %v", err)
	}
	cfg.JWKSURL = "http://dex.dex.svc.cluster.local:5556/dex/keys"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a plain-http key set was accepted")
	}
	for _, want := range []string{"https://", cfg.JWKSURL, "TLS only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// The audience list arrives comma-separated from the flag and the chart:
// trimmed, without empties, without duplicates, in order.
func TestParseAudiences(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{" , ", nil},
		{platformClient, []string{platformClient}},
		{"agent-platform, dex-k8s-authenticator,,agent-platform ", []string{platformClient, "dex-k8s-authenticator"}},
	} {
		if got := ParseAudiences(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %v, want %v", c.in, got, c.want)
		}
	}
}

// mcp-kubernetes's refusal to answer a read whole — its response cap's
// response_too_large document, relayed by muster as the result's error — is
// the verify's TooLarge with the sizes, phrased for a reader; a log line
// that merely mentions the code, or any other document, is not.
func TestClassifyReadsResponseTooLarge(t *testing.T) {
	err := classify(`{"error":"response_too_large","bytes":153191,"limit":131072,"message":"response is 153191 bytes, exceeds 131072 byte limit","hint":"narrow the query: tighten filters, reduce the time range, or request fewer items"}`)
	var tl *verify.TooLarge
	if !errors.As(err, &tl) || tl.Bytes != 153191 || tl.Limit != 131072 {
		t.Fatalf("response_too_large: %#v", err)
	}
	for _, want := range []string{"150 KiB", "128 KiB", "mcp-kubernetes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the reason does not say %q: %v", want, err)
		}
	}
	for _, text := range []string{`level=error msg="response_too_large"`, `{"error":"other","bytes":1,"limit":2}`, `Failed to get resource: the server is currently unable to handle the request`} {
		if errors.As(classify(text), &tl) {
			t.Errorf("%q read as too large", text)
		}
	}
}

// A read asks mcp-kubernetes for what its check reads: the slim output for
// a readiness check, the normal output for a drift probe, never a whole
// object — which the response cap refuses for a HelmRelease with history.
func TestOutputOfShape(t *testing.T) {
	if got := outputOf(verify.Readiness); got != "slim" {
		t.Errorf("readiness: %q", got)
	}
	if got := outputOf(verify.Configuration); got != "normal" {
		t.Errorf("configuration: %q", got)
	}
}
