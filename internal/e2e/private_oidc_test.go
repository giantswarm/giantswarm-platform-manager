package e2e

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

const (
	allowPrivateIPOIDCPath = "muster.muster.oauth.server.dex.allowPrivateIPOIDC"
	allowPrivateIPOIDCOn   = "allowPrivateIPOIDC: true"
)

func TestReconcileKeepsAllowPrivateIPOIDCOnAPrivateInstallation(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	st.ghs.addFile(acmeConfigs, installations.ConfigPatchPath(birch), "codename: birch\nbase: acme.test\nmanagementCluster:\n  private: false\n")
	marker := installations.Capabilities()[0].EnabledMarker(birch)
	st.ghs.addFile(acmeConfigs, marker, "muster:\n  muster:\n    oauth:\n      server:\n        dex:\n          "+allowPrivateIPOIDCOn+"\n")
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	// The helpers set the call's mode on the arguments: fresh ones per call.
	args := func() map[string]any {
		return map[string]any{tools.ArgInstallation: birch, tools.ArgInputs: minimalInputs(nil)}
	}

	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args())
	if isErr {
		t.Fatal(text)
	}
	entry := findEntry(t, out, birch)
	if entry.Refused != "" {
		t.Fatalf("refused: %s", entry.Refused)
	}
	if inst, _ := entry.Inputs["installation"].(map[string]any); inst[argPrivate] != true {
		t.Fatalf("birch is not private on the inputs: %v", entry.Inputs["installation"])
	}
	marks := verify.Result{Features: entry.Features}
	if d := anyDimension(t, marks, "muster-dex-allow-private-ip-oidc"); d.Mark != verify.AsDefined || len(d.Differences) != 0 {
		t.Errorf("muster-dex-allow-private-ip-oidc: %s %+v", d.Mark, d.Differences)
	}
	for _, diff := range allDifferences(marks) {
		if diff.Path == allowPrivateIPOIDCPath {
			t.Errorf("the flag differs: %+v", diff)
		}
	}
	if content := fileOf(t, entry.Installation, acmeConfigs+":"+marker).Content; !strings.Contains(content, allowPrivateIPOIDCOn) {
		t.Errorf("the rendered patch lacks %q:\n%s", allowPrivateIPOIDCOn, content)
	}

	committed, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args())
	if isErr || committed.Action == nil {
		t.Fatal(text)
	}
	branch := branchPrefix + committed.Action.Name + "/" + birch
	if content := string(st.remote.Files(repoOf(t, acmeConfigs), branch)[marker]); !strings.Contains(content, allowPrivateIPOIDCOn) {
		t.Errorf("the committed patch lacks %q:\n%s", allowPrivateIPOIDCOn, content)
	}
}
