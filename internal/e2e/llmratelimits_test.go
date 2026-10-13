package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// The LLM listener's rate limits are a choice the configmap patch carries
// whole, with the comment above them: typed once, the hub's patch plans the
// list under llmRouting.rateLimits in the chart's shape; put on record, a
// reconcile without the input reads list and comment back and plans the
// patch byte for byte, nothing kept.
func TestReconcileKeepsTheLLMRateLimitsOnRecord(t *testing.T) {
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
	const key = `source.unverifiedWorkload.namespace + "/" + source.unverifiedWorkload.serviceAccount`
	limits := []any{
		map[string]any{"tokens": 8000000, "unit": "Minutes"},
		map[string]any{"tokens": 5000000, "unit": "Minutes", "key": key},
	}
	const comment = "Runaway guards per data-plane replica,\nsized from the 30-day peak."
	const want = "llmRouting:\n  enabled: true\n  # Runaway guards per data-plane replica,\n  # sized from the 30-day peak.\n" +
		"  rateLimits:\n    - tokens: 8000000\n      unit: Minutes\n    - tokens: 5000000\n      unit: Minutes\n      key: " + `source.unverifiedWorkload.namespace + "/" + source.unverifiedWorkload.serviceAccount` + "\n"
	first := render(map[string]any{"llmRouting": map[string]any{"rateLimits": limits, "rateLimitsComment": comment}})
	if !strings.Contains(first.Content, want) {
		t.Fatalf("the typed rate limits are not planned in the chart's shape:\n%s", first.Content)
	}
	st.ghs.addFile(hubConfigs, marker, first.Content)

	second := render(nil)
	if second.Change != plan.ChangeUnchanged || second.Content != first.Content {
		t.Errorf("the rate limits on record are %s the second time:\n%s\nwant\n%s", second.Change, second.Content, first.Content)
	}
	for _, k := range second.Kept {
		if strings.HasPrefix(k.List, "llmRouting") {
			t.Errorf("the rate limits are rendered, never kept: %v", k)
		}
	}
	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.AgentPlatform, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	llm, _ := findPlan(t, out, hub).Inputs["llmRouting"].(map[string]any)
	// JSON on both sides: the read-back's numbers come through the tool's result.
	got, _ := json.Marshal(llm["rateLimits"])
	if wantJSON, _ := json.Marshal(limits); string(got) != string(wantJSON) {
		t.Errorf("read back %s, want %s", got, wantJSON)
	}
	if llm["rateLimitsComment"] != comment {
		t.Errorf("read back the comment %q, want %q", llm["rateLimitsComment"], comment)
	}
}
