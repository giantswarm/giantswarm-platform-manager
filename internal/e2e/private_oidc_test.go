package e2e

// A reconcile of an installation reached through the hub's tunnel whose
// record declares the management cluster public: its Dex sits on the same
// private ingress, so muster's allowPrivateIPOIDC must stay true.

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// allowPrivateIPOIDCPath is the flag's path in the agent-platform patch, and
// allowPrivateIPOIDCOn the line a rendered or hand-written patch carries.
const (
	allowPrivateIPOIDCPath = "muster.muster.oauth.server.dex.allowPrivateIPOIDC"
	allowPrivateIPOIDCOn   = "allowPrivateIPOIDC: true"
)

// birch is reached through the hub's tunnel while its record says
// managementCluster.private: false, and its patch on record carries muster's
// allowPrivateIPOIDC by hand, as an installation whose Dex resolves to a
// private ingress does, since the shared default derives the flag from the
// record's managementCluster.private. The reconcile's dry run reads the flag
// as defined — no planned removal, no drift, the rendered patch carries it —
// and the committed reconcile writes it true, from the record's private
// fact.
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
