package e2e

// A reconcile of an installation reached through the hub's tunnel whose
// record declares the management cluster public: muster's blanket
// private-address flag on record is a planned removal, and the render lists
// agentgateway's host instead.

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// allowPrivateIPOIDCPath is the blanket flag's path in the agent-platform
// patch, allowPrivateIPOIDCOn the line a hand-written patch carries, and
// redirectHostsOn the host list the render carries in its place.
const (
	allowPrivateIPOIDCPath = "muster.muster.oauth.server.dex.allowPrivateIPOIDC"
	allowPrivateIPOIDCOn   = "allowPrivateIPOIDC: true"
	redirectHostsOn        = "allowPrivateIPRedirectURIHosts:\n          - agentgateway.birch.acme.test"
)

// birch is reached through the hub's tunnel while its record says
// managementCluster.private: false, and its patch on record carries muster's
// allowPrivateIPOIDC by hand. The reconcile's dry run plans the flag's
// removal under its migration, the rendered patch lacks it and lists
// agentgateway's host for private redirect URIs, from the record's private
// fact, and the committed reconcile writes the same.
func TestReconcileSwapsMusterPrivateFlagsForHosts(t *testing.T) {
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
	planned := false
	for _, diff := range allDifferences(verify.Result{Features: entry.Features}) {
		if diff.Path == allowPrivateIPOIDCPath {
			planned = strings.HasSuffix(diff.Planned, "· M46")
		}
	}
	if !planned {
		t.Errorf("the flag's removal is not planned under M46: %+v", allDifferences(verify.Result{Features: entry.Features}))
	}
	content := fileOf(t, entry.Installation, acmeConfigs+":"+marker).Content
	if strings.Contains(content, allowPrivateIPOIDCOn) || !strings.Contains(content, redirectHostsOn) {
		t.Errorf("the rendered patch keeps %q or lacks %q:\n%s", allowPrivateIPOIDCOn, redirectHostsOn, content)
	}

	committed, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args())
	if isErr || committed.Action == nil {
		t.Fatal(text)
	}
	branch := branchPrefix + committed.Action.Name + "/" + birch
	content = string(st.remote.Files(repoOf(t, acmeConfigs), branch)[marker])
	if strings.Contains(content, allowPrivateIPOIDCOn) || !strings.Contains(content, redirectHostsOn) {
		t.Errorf("the committed patch keeps %q or lacks %q:\n%s", allowPrivateIPOIDCOn, redirectHostsOn, content)
	}
}
