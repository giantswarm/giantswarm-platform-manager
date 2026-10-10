package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// The MCP backends the platform's network policy fences are a choice the
// configmap patch carries whole: typed once, the hub's patch plans the map
// under networkPolicy.mcpBackends in the chart's shape; put on record, a
// reconcile without the input reads it back and plans the patch byte for
// byte, nothing kept.
func TestReconcileKeepsTheMCPBackendsOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	c := st.mcpClient(t, aliceToken)
	render := func(inputs map[string]any) plan.File {
		t.Helper()
		out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.AgentPlatform, tools.ArgInputs: minimalInputs(inputs), tools.ArgContent: true})
		if isErr {
			t.Fatal(text)
		}
		for _, f := range findPlan(t, out, hub).Files {
			if f.Path == marker {
				return f
			}
		}
		t.Fatalf("the plan carries no %s", marker)
		return plan.File{}
	}
	backends := map[string]any{
		"tickets": map[string]any{"namespace": "mcp-tickets", "ports": []any{8080},
			"additionalPeers": []any{map[string]any{"namespace": "envoy-gateway-system", "matchLabels": map[string]any{"app.kubernetes.io/name": "envoy"}}}},
		"runbooks": map[string]any{"namespace": "mcp-runbooks", "ports": []any{8080}, "metrics": true},
	}
	const want = "networkPolicy:\n  mcpBackends:\n    runbooks:\n      namespace: mcp-runbooks\n      ports:\n        - 8080\n      metrics: true\n" +
		"    tickets:\n      namespace: mcp-tickets\n      ports:\n        - 8080\n      additionalPeers:\n        - namespace: envoy-gateway-system\n          matchLabels:\n            app.kubernetes.io/name: envoy\n"
	first := render(map[string]any{"networkPolicy": map[string]any{"mcpBackends": backends}})
	if !strings.Contains(first.Content, want) {
		t.Fatalf("the typed backends are not planned in the chart's shape:\n%s", first.Content)
	}
	st.ghs.addFile(hubConfigs, marker, first.Content)

	second := render(nil)
	if second.Change != plan.ChangeUnchanged || second.Content != first.Content {
		t.Errorf("the backends on record are %s the second time:\n%s\nwant\n%s", second.Change, second.Content, first.Content)
	}
	for _, k := range second.Kept {
		if strings.HasPrefix(k.List, "networkPolicy") {
			t.Errorf("the backends are rendered, never kept: %v", k)
		}
	}
	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.AgentPlatform, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	np, _ := findPlan(t, out, hub).Inputs["networkPolicy"].(map[string]any)
	// JSON on both sides: the read-back's numbers come through the tool's result.
	got, _ := json.Marshal(np["mcpBackends"])
	if wantJSON, _ := json.Marshal(backends); string(got) != string(wantJSON) {
		t.Errorf("read back %s, want %s", got, wantJSON)
	}
}
