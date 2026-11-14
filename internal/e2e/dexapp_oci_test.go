package e2e

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func TestDexPrerequisiteRefusesAnInheritedOCIConstraint(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	seedRemote(t, st)
	st.ghs.addFile(basesRepo, installations.DexAppBasePath, "apiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: dex-app\n  namespace: giantswarm\nspec:\n  ref:\n    tag: 3.4.24\n")
	st.ghs.addFile(basesRepo, "bases/collections/capa/stages/stable/kustomization.yaml", "resources: [../../../shared/base/dex-app.yaml]\ncomponents: [../../../shared/constraint]\n")
	st.ghs.addFile(basesRepo, "bases/collections/shared/constraint/kustomization.yaml", "patches:\n  - target: {kind: OCIRepository}\n    patch: |\n      kind: OCIRepository\n      metadata: {name: patched-by-target-selector}\n      spec:\n        ref:\n          semver: '>=0.0.0-0'\n")
	connection := st.mcpClient(t, aliceToken)
	arguments := map[string]any{tools.ArgInstallation: birch, tools.ArgInputs: minimalInputs(nil)}
	result, text, isError := dryRun(t, connection, tools.ToolReconcileCapability, arguments)
	if isError {
		t.Fatal(text)
	}
	planned := findPlan(t, result, birch)
	if !strings.Contains(planned.CommitRefused, "not an exact version") {
		t.Fatalf("inherited selector did not hold the prerequisite: %q", planned.CommitRefused)
	}
	delete(arguments, tools.ArgDryRun)
	_, text, isError = commitCall(t, connection, tools.ToolReconcileCapability, arguments)
	if !isError || !strings.Contains(text, "not an exact version") || len(st.remote.PullRequests()) != 0 {
		t.Fatalf("commit: error %v, pull requests %d, %s", isError, len(st.remote.PullRequests()), text)
	}
}
