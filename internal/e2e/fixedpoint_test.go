package e2e

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// A reconcile is a fixed point: the hub's plain files as a dry run renders
// them, put on record, render to the same bytes again, and the dry run
// plans no change. The hub's portal signs in through its own Dex client,
// and the render lists the definition's backstage client before it, which
// the next read-back must not take for the portal's; and the note after
// the kept trustedIssuers, at the server section's indentation, stays
// there.
func TestReconcileIsAFixedPoint(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	st.ghs.mu.Lock()
	onRecord := st.ghs.files[hubConfigs][marker]
	st.ghs.mu.Unlock()
	const issuers = "        trustedIssuers:\n          - issuer: https://issuer.example.test\n            subjectClaim: email\n        # A note on the server section, after its last block.\n"
	withIssuers := strings.Replace(onRecord, "kagent:\n", issuers+"kagent:\n", 1)
	if withIssuers == onRecord {
		t.Fatalf("the hub's platform patch on record:\n%s", onRecord)
	}
	st.ghs.addFiles(hubConfigs, map[string]string{marker: withIssuers})
	c := st.mcpClient(t, aliceToken)
	render := func() plan.Installation {
		t.Helper()
		out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.AgentPlatform, tools.ArgInputs: minimalInputs(nil), tools.ArgContent: true})
		if isErr {
			t.Fatal(text)
		}
		return findPlan(t, out, hub)
	}

	first := render()
	written := map[string]string{}
	for _, f := range first.Files {
		if f.Change == plan.ChangeUnchanged || isSecretFile(f.Path) || strings.Contains(f.Content, "GENERATED(") || strings.Contains(f.Content, "SUPPLIED(") {
			continue
		}
		written[f.Repository+":"+f.Path] = f.Content
		st.ghs.addFile(f.Repository, f.Path, f.Content)
	}
	for _, path := range []string{hubConfigs + ":" + marker, hubConfigs + ":" + installations.DexPatchPath(hub)} {
		if _, ok := written[path]; !ok {
			t.Fatalf("the first render writes no %s: %v", path, first.Diff)
		}
	}
	if !strings.Contains(written[hubConfigs+":"+marker], issuers) {
		t.Fatalf("the kept trustedIssuers and the note after them:\n%s", written[hubConfigs+":"+marker])
	}

	second := render()
	for _, f := range second.Files {
		want, ok := written[f.Repository+":"+f.Path]
		if !ok {
			continue
		}
		if f.Change != plan.ChangeUnchanged || f.Content != want {
			t.Errorf("%s:%s is %s the second time:\n%s\nwant\n%s", f.Repository, f.Path, f.Change, f.Content, want)
		}
	}
}
