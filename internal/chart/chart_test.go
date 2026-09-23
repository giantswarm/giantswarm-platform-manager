package chart

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	chartDir   = "../../helm/giantswarm-platform-manager"
	testValues = "../../tests/test-values.yaml"
	serverName = "giantswarm-platform-manager"
)

// render runs helm template over the test values and the arguments given and
// answers the rendered objects by kind/name; a failed render answers helm's
// message instead.
func render(t *testing.T, args ...string) (map[string]map[string]any, string) {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not on PATH: the chart render proofs run in CI's render-consumption job")
	}
	cmd := exec.Command("helm", append([]string{"template", "giantswarm-platform-manager", chartDir, "-f", testValues}, args...)...) // #nosec G204 -- fixed binary, arguments are the test's own values files and flags
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, stderr.String()
	}
	objects := map[string]map[string]any{}
	dec := yaml.NewDecoder(&stdout)
	for {
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("decode the render: %v", err)
		}
		if doc == nil {
			continue
		}
		meta, _ := doc["metadata"].(map[string]any)
		objects[doc["kind"].(string)+"/"+meta["name"].(string)] = doc
	}
	return objects, ""
}

// at walks a rendered object by keys and list indexes given as a dotted path.
func at(obj any, path string) any {
	for _, key := range strings.Split(path, ".") {
		switch v := obj.(type) {
		case map[string]any:
			obj = v[key]
		case []any:
			var found any
			for _, item := range v {
				if m, ok := item.(map[string]any); ok && m["name"] == key {
					found = m
					break
				}
			}
			obj = found
		default:
			return nil
		}
	}
	return obj
}

// mcpServers are the MCPServer objects of a render, by name.
func mcpServers(objects map[string]map[string]any) []string {
	var names []string
	for key := range objects {
		if name, ok := strings.CutPrefix(key, "MCPServer/"); ok {
			names = append(names, name)
		}
	}
	return names
}

// registration is the one MCPServer of a render.
func registration(t *testing.T, objects map[string]map[string]any) map[string]any {
	t.Helper()
	if names := mcpServers(objects); !reflect.DeepEqual(names, []string{serverName}) {
		t.Fatalf("MCPServers rendered: %v, want the one %s", names, serverName)
	}
	return objects["MCPServer/"+serverName]
}

func env(t *testing.T, objects map[string]map[string]any, name string) any {
	t.Helper()
	deployment := objects["Deployment/giantswarm-platform-manager"]
	if deployment == nil {
		t.Fatal("no Deployment rendered")
	}
	return at(deployment, "spec.template.spec.containers.giantswarm-platform-manager.env."+name+".value")
}

// The platform shape: one registration at /mcp, pinned to the App, that
// forwards the person's identity and requires the cross-client audience; the
// server trusts it next to the clients people sign in with — the union, in
// order, is LIVE_AUDIENCES. Its timeout covers the longest call, a verify.
func TestOneRegistrationForwardsTheIdentity(t *testing.T) {
	objects, msg := render(t, "-f", filepath.Join("../../tests", "oauth-values.yaml"))
	if msg != "" {
		t.Fatal(msg)
	}
	mcp := registration(t, objects)
	for path, want := range map[string]any{
		"spec.url":                                 "http://giantswarm-platform-manager.default.svc.cluster.local:8080/mcp",
		"spec.timeout":                             180,
		"spec.auth.type":                           "oauth",
		"spec.auth.forwardIdentity":                true,
		"spec.auth.requiredAudiences":              []any{"dex-k8s-authenticator"},
		"spec.auth.authorizationServer.issuer":     "https://github.com/apps/giantswarm-platform-manager",
		"spec.auth.authorizationServer.grantScope": "subject",
	} {
		if got := at(mcp, path); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
	if got := at(mcp, "spec.auth.forwardToken"); got != nil {
		t.Errorf("spec.auth.forwardToken rendered: %v", got)
	}
	if got := env(t, objects, "LIVE_AUDIENCES"); got != "agent-platform,dex-k8s-authenticator" {
		t.Errorf("LIVE_AUDIENCES: %v", got)
	}
	if got := env(t, objects, "LIVE_ENABLED"); got != "true" {
		t.Errorf("LIVE_ENABLED: %v", got)
	}
}

// The lab shape names one audience and requires none: the registration
// forwards the identity without requiredAudiences and the server trusts the
// one.
func TestOneRegistrationWithoutRequiredAudiences(t *testing.T) {
	objects, msg := render(t, "-f", filepath.Join("../../tests", "lab-oauth-values.yaml"))
	if msg != "" {
		t.Fatal(msg)
	}
	mcp := registration(t, objects)
	if got := at(mcp, "spec.auth.forwardIdentity"); got != true {
		t.Errorf("spec.auth.forwardIdentity: %v", got)
	}
	if got := at(mcp, "spec.auth.requiredAudiences"); got != nil {
		t.Errorf("spec.auth.requiredAudiences rendered: %v", got)
	}
	if got := env(t, objects, "LIVE_AUDIENCES"); got != "agent-platform" {
		t.Errorf("LIVE_AUDIENCES: %v", got)
	}
}

// Without live.enabled the registration forwards no identity and requires
// no audience, and the Deployment serves no live tools.
func TestNoIdentityForwardedWithoutLive(t *testing.T) {
	objects, msg := render(t, "-f", filepath.Join("../../tests", "oauth-values.yaml"), "--set", "live.enabled=false")
	if msg != "" {
		t.Fatal(msg)
	}
	mcp := registration(t, objects)
	for _, path := range []string{"spec.auth.forwardIdentity", "spec.auth.requiredAudiences"} {
		if got := at(mcp, path); got != nil {
			t.Errorf("%s rendered: %v", path, got)
		}
	}
	if got := at(mcp, "spec.auth.authorizationServer.issuer"); got != "https://github.com/apps/giantswarm-platform-manager" {
		t.Errorf("the App pin: %v", got)
	}
	if got := env(t, objects, "LIVE_ENABLED"); got != nil {
		t.Errorf("LIVE_ENABLED: %v", got)
	}
}

// Neither list naming an audience refuses the render by name: an empty list
// would accept every audience.
func TestLiveRefusesNoAudience(t *testing.T) {
	_, msg := render(t, "-f", filepath.Join("../../tests", "oauth-values.yaml"), "--set", "live.audiences=null", "--set", "muster.mcpServer.requiredAudiences=null")
	if !strings.Contains(msg, "live.audiences must name at least one OAuth client") {
		t.Errorf("rendered, or refused for another reason: %q", msg)
	}
}

// The identity rides on the registration's OAuth auth: live tools without
// OAuth would be served and never reached with an identity, so the render
// is refused by name.
func TestLiveRequiresOAuth(t *testing.T) {
	_, msg := render(t, "-f", filepath.Join("../../tests", "oauth-values.yaml"), "--set", "oauth.enabled=false")
	for _, want := range []string{"live.enabled", "auth.forwardIdentity", "oauth.enabled"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not name %q: %q", want, msg)
		}
	}
}

// The key set is read over TLS only: a plain-http jwksURL is refused at
// render, naming the allowances that exist, not at the first token.
func TestLiveRefusesAPlainHTTPKeySet(t *testing.T) {
	_, msg := render(t, "-f", filepath.Join("../../tests", "lab-oauth-values.yaml"), "--set", "live.jwksURL=http://dex.dex.svc.cluster.local:5556/dex/keys")
	for _, want := range []string{"live.jwksURL must be an https:// URL", "http://dex.dex.svc.cluster.local:5556/dex/keys", "live.allowPrivateIPJWKS", "live.caSecret"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not name %q: %q", want, msg)
		}
	}
}
