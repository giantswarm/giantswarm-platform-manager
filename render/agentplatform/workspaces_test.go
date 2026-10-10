package agentplatform

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// signinRedirect renders the shape with the workspaces fact set and answers
// the platform client's extraRedirectURIs of its Dex patch, the Dex patch
// as YAML and the redirect URIs the live probe asks Dex for on the platform
// client.
func signinRedirect(t *testing.T, workspaces map[string]any) ([]string, string, []string) {
	t.Helper()
	input, _ := loadInput(t, shapePublicCustomer)
	installation := input["installation"].(map[string]any)
	if workspaces != nil {
		installation["workspaces"] = workspaces
	}
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	patch := in.dexPatch()
	oidc, _ := mapValue(patch, "oidc").(render.Map)
	static, _ := mapValue(oidc, "staticClients").(render.Map)
	muster, _ := mapValue(static, "muster").(render.Map)
	uris, _ := mapValue(muster, "extraRedirectURIs").([]string)
	var probed []string
	for _, c := range in.dexRedirectClients() {
		if c.id == in.Installation.MusterClientID {
			probed = append(probed, c.redirectURI)
		}
	}
	return uris, string(render.MustYAML(patch)), probed
}

// With workspaces on, the platform client carries the workspace-manager's
// sign-in, https://workspace-manager.<domain>/signin, beside the template's
// redirect URI, and the live probe asks Dex for it; with them off or absent
// it carries none; an explicit base URL moves the host, its trailing slash
// dropped; a client of the installation's own gets nothing from the
// definition, the platform's client by id does. Nothing else of the Dex
// patch moves.
func TestWorkspaceSigninRedirect(t *testing.T) {
	const domain, callback = "kestrel.oakridge.example", "https://muster.kestrel.oakridge.example/oauth/callback"
	const derived = "https://workspace-manager." + domain + "/signin"
	_, off, probedOff := signinRedirect(t, nil)
	if probedOff[0] != callback || len(probedOff) != 1 || strings.Contains(off, "extraRedirectURIs") {
		t.Fatalf("workspaces absent: probed %v, patch\n%s", probedOff, off)
	}
	cases := []struct {
		name       string
		workspaces map[string]any
		want       string
	}{
		{"the switch off", map[string]any{keyEnabled: false}, ""},
		{"the switch on", map[string]any{keyEnabled: true}, derived},
		{"on with a base URL", map[string]any{keyEnabled: true, "baseUrl": "https://ws." + domain + "/"}, "https://ws." + domain + "/signin"},
		{"on with the platform client by id", map[string]any{keyEnabled: true, "clientId": "00000000-0000-4000-8000-000000000001"}, derived},
		{"on with a client of the installation's own", map[string]any{keyEnabled: true, "clientId": "ws-client"}, ""},
	}
	for _, c := range cases {
		uris, patch, probed := signinRedirect(t, c.workspaces)
		if c.want == "" {
			if len(uris) != 0 || len(probed) != 1 || patch != off {
				t.Errorf("%s: extraRedirectURIs %v, probed %v, patch differs: %v", c.name, uris, probed, patch != off)
			}
			continue
		}
		if len(uris) != 1 || uris[0] != c.want {
			t.Errorf("%s: extraRedirectURIs %v, want [%s]", c.name, uris, c.want)
		}
		if len(probed) != 2 || probed[0] != callback || probed[1] != c.want {
			t.Errorf("%s: probed %v, want [%s %s]", c.name, probed, callback, c.want)
		}
		// The one line beside the template's client secret reference is the URI.
		line := "      extraRedirectURIs:\n        - " + c.want + "\n"
		if !strings.Contains(patch, line) || strings.Replace(patch, line, "", 1) != off {
			t.Errorf("%s: the Dex patch moves beyond the URI:\n%s", c.name, patch)
		}
	}
}

// TestGoldensCoverWorkspaces holds the golden shapes to the workspaces: one
// shape turns them on and carries the sign-in on the platform client, and
// one turns them off and carries none.
func TestGoldensCoverWorkspaces(t *testing.T) {
	covered := map[bool]bool{}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		patch := string(result.Tree()["giantswarm/"+in.Installation.Customer+"-configs/installations/"+in.Installation.Name+"/apps/dex-app/configmap-values.yaml.patch"])
		on := in.Installation.Workspaces.Enabled
		if strings.Contains(patch, "extraRedirectURIs") != on {
			t.Errorf("%s: workspaces on %v, the Dex patch carries the sign-in %v", shape, on, !on)
		}
		covered[on] = true
	}
	for _, on := range []bool{true, false} {
		if !covered[on] {
			t.Errorf("no golden shape renders workspaces on %v", on)
		}
	}
}
