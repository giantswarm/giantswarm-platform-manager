package format

import (
	"bytes"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func TestDeferredPairsDryRun(t *testing.T) {
	result := tools.CapabilityResult{Tool: tools.ToolReconcileCapability, Capability: agentPlatform, Hub: hazel,
		Order: []string{hazel}, Installations: []tools.DryRun{{Installation: plan.Installation{Name: hazel,
			DeferredPairs: []plan.DeferredPair{{Name: "muster-token-exchange-oak-client-secret", Installation: oak,
				Files:  []string{"fleet/clusters:management-clusters/hazel/extras/agent-platform/secrets/oak-token-exchange-credentials.yaml"},
				Reason: "oak is outside this plan and neither side is on record; reconcile both installations in the same wave"}},
		}}}}
	var output bytes.Buffer
	if err := Plan(&output, result, false); err != nil {
		t.Fatal(err)
	}
	golden(t, "testdata/deferred-pairs-dry-run.golden", output.Bytes())
}
