package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func TestHubCommitRecordsDeferredPairs(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFor(t, st.ghs, acmeConfigs, acmeMCs, hubConfigs, hubMCs)
	portal := strings.Replace(portalConfig(hub, alder, birch), "        gs:\n", "        gs:\n          clusterTokenBroker:\n            tokenUrl: https://muster."+hub+".example.test/token\n", 1)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), portal)
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	patch, _ := st.ghs.file(hubConfigs, marker)
	st.ghs.addFile(hubConfigs, marker, strings.Replace(patch, "      server:\n", "      server:\n        tokenExchangeBroker:\n          brokerClients:\n            broker: {}\n", 1))
	st.ghs.addRepo(teleportFleet, map[string]string{tunnelportValuesPath: tunnelportValuesBare})
	connection := st.mcpClient(t, aliceToken)
	arguments := map[string]any{tools.ArgInstallation: hub, tools.ArgInputs: minimalInputs(nil), tools.ArgContent: true}
	dry, text, isError := dryRun(t, connection, tools.ToolReconcileCapability, arguments)
	if isError {
		t.Fatal(text)
	}
	planned := findPlan(t, dry, hub)
	if len(planned.DeferredPairs) == 0 {
		t.Fatal("no deferred pair")
	}
	for _, file := range planned.Files {
		if isSecretFile(file.Path) && strings.Contains(file.Content, "SUPPLIED(") {
			st.ghs.addFile(file.Repository, file.Path, "sops: on record\n")
		}
	}
	seedRemote(t, st)
	delete(arguments, tools.ArgDryRun)
	committed, text, isError := commitCall(t, connection, tools.ToolReconcileCapability, arguments)
	if isError || committed.Action == nil {
		t.Fatalf("commit: %v %s", isError, text)
	}
	name := "muster-token-exchange-" + birch + "-client-secret"
	if !slices.Contains(committed.Action.Spec.DeferredPairs[hub], name) {
		t.Fatalf("action omitted deferred pair: %+v", committed.Action.Spec.DeferredPairs)
	}
	for _, pullRequest := range committed.PullRequests {
		for path, content := range st.remote.Files(repoOf(t, pullRequest.Repository), pullRequest.Head) {
			if strings.Contains(path, birch+"-token-exchange-credentials.yaml") || strings.Contains(string(content), "GENERATED("+name+")") {
				t.Errorf("deferred pair committed in %s", path)
			}
		}
	}
}
