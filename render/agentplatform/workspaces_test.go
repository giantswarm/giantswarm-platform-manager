package agentplatform

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The platform's Dex client carries the one redirect URI the template sets on
// every golden shape: the workspace-manager signs no one in to Dex (muster
// does), so no Dex patch renders extraRedirectURIs and no live probe asks Dex
// for a workspace-manager /signin redirect.
func TestDexPatchCarriesNoWorkspaceSignin(t *testing.T) {
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
		if strings.Contains(patch, "extraRedirectURIs") || strings.Contains(patch, "/signin") {
			t.Errorf("%s: the Dex patch carries a workspace-manager sign-in:\n%s", shape, patch)
		}
		for _, c := range in.dexRedirectClients() {
			if strings.HasSuffix(c.redirectURI, "/signin") {
				t.Errorf("%s: the live probe asks Dex for %s on %s", shape, c.redirectURI, c.id)
			}
		}
	}
}
