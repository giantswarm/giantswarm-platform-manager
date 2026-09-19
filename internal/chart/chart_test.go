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
	liveName   = "giantswarm-platform-manager-live"
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

func liveEnv(t *testing.T, objects map[string]map[string]any) string {
	t.Helper()
	deployment := objects["Deployment/giantswarm-platform-manager"]
	if deployment == nil {
		t.Fatal("no Deployment rendered")
	}
	v, _ := at(deployment, "spec.template.spec.containers.giantswarm-platform-manager.env.LIVE_AUDIENCES.value").(string)
	return v
}

// The platform shape: the live registration requires the cross-client
// audience on the CR and the server trusts it next to the clients people
// sign in with — the union, in order, is LIVE_AUDIENCES.
func TestLiveRegistrationRendersRequiredAudiences(t *testing.T) {
	objects, msg := render(t, "-f", filepath.Join("../../tests", "oauth-values.yaml"))
	if msg != "" {
		t.Fatal(msg)
	}
	live := objects["MCPServer/"+liveName]
	if live == nil {
		t.Fatal("no live MCPServer rendered")
	}
	if got := at(live, "spec.auth.requiredAudiences"); !reflect.DeepEqual(got, []any{"dex-k8s-authenticator"}) {
		t.Errorf("spec.auth.requiredAudiences: %v", got)
	}
	if got := at(live, "spec.auth.forwardToken"); got != true {
		t.Errorf("spec.auth.forwardToken: %v", got)
	}
	if got := liveEnv(t, objects); got != "agent-platform,dex-k8s-authenticator" {
		t.Errorf("LIVE_AUDIENCES: %q", got)
	}
}

// The lab shape names one audience and requires none: the CR carries no
// requiredAudiences and the server trusts the one.
func TestLiveRegistrationWithoutRequiredAudiences(t *testing.T) {
	objects, msg := render(t, "-f", filepath.Join("../../tests", "lab-oauth-values.yaml"))
	if msg != "" {
		t.Fatal(msg)
	}
	live := objects["MCPServer/"+liveName]
	if live == nil {
		t.Fatal("no live MCPServer rendered")
	}
	if got := at(live, "spec.auth.requiredAudiences"); got != nil {
		t.Errorf("spec.auth.requiredAudiences rendered: %v", got)
	}
	if got := liveEnv(t, objects); got != "agent-platform" {
		t.Errorf("LIVE_AUDIENCES: %q", got)
	}
}

// Neither list naming an audience refuses the render by name: an empty list
// would accept every audience.
func TestLiveRegistrationRefusesNoAudience(t *testing.T) {
	_, msg := render(t, "-f", filepath.Join("../../tests", "oauth-values.yaml"), "--set", "live.audiences=null", "--set", "muster.liveServer.requiredAudiences=null")
	if !strings.Contains(msg, "live.audiences must name at least one OAuth client") {
		t.Errorf("rendered, or refused for another reason: %q", msg)
	}
}
