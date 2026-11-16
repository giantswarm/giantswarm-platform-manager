package e2e

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func TestHubCommitRefusesAnUnbackedDexSecretReference(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	seedRemote(t, st)
	const secret = "external-client-secret"
	patch := "installations/" + hub + "/apps/dex-app/configmap-values.yaml.patch"
	st.ghs.addFile(hubConfigs, patch, "oidc:\n  extraStaticClients:\n    - id: external\n      secretRef:\n        name: "+secret+"\n        key: client-secret\n")
	connection := st.mcpClient(t, aliceToken)
	arguments := map[string]any{tools.ArgInstallation: hub, tools.ArgInputs: minimalInputs(nil)}
	result, text, isError := dryRun(t, connection, tools.ToolReconcileCapability, arguments)
	if isError {
		t.Fatal(text)
	}
	planned := findPlan(t, result, hub)
	if !strings.Contains(planned.CommitRefused, "Secret/"+secret) || len(planned.UnresolvedReferences) == 0 {
		t.Fatalf("no reference refusal: %q", planned.CommitRefused)
	}
	delete(arguments, tools.ArgDryRun)
	_, text, isError = commitCall(t, connection, tools.ToolReconcileCapability, arguments)
	if !isError || !strings.Contains(text, "Secret/"+secret) || len(st.remote.PullRequests()) != 0 {
		t.Fatalf("commit: error %v, pull requests %d, %s", isError, len(st.remote.PullRequests()), text)
	}
	_, text, isError = commitCall(t, connection, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{hub}, tools.ArgInputs: minimalInputs(nil)})
	if !isError || !strings.Contains(text, "Secret/"+secret) || len(st.remote.PullRequests()) != 0 {
		t.Fatalf("wave: error %v, pull requests %d, %s", isError, len(st.remote.PullRequests()), text)
	}
}
