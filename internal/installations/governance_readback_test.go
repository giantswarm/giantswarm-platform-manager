package installations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// governanceOnRecord appends the platform's governance to a configmap patch,
// as a commit of governance.* writes it.
func governanceOnRecord(patch string) string {
	return patch + "governance: governed\ngovernedServers:\n  giantswarm-platform-manager:\n    safe:\n      - get_info\n  mcp-kubernetes: null\n"
}

// The governance on record is read back as governance.mode and
// governance.governedServers, a dropped source as null, so a reconcile
// that types nothing renders it again: it is no drift a reconcile removes.
// With the meta chart release that renders it cut, the patch read back and
// rendered again carries it byte for byte.
func TestGovernanceSurvivesAReconcile(t *testing.T) {
	chart, muster := agentplatform.GovernanceChart, agentplatform.GovernanceMuster
	agentplatform.GovernanceChart, agentplatform.GovernanceMuster = "4.190.0", "5.70.0"
	t.Cleanup(func() { agentplatform.GovernanceChart, agentplatform.GovernanceMuster = chart, muster })
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)

	patch, read, doc, marker := inputFromTheRecord(t, "platform-tenant-retired", governanceOnRecord)
	require.Equal(t, agentplatform.GovernanceGoverned, read["governance.mode"])
	require.Equal(t, map[string]any{"giantswarm-platform-manager": map[string]any{"safe": []any{"get_info"}}, "mcp-kubernetes": nil}, read["governance.governedServers"])
	result, err := definition.Render(doc, nil, render.ModeCommit)
	require.NoError(t, err)
	again := string(result.Tree()["giantswarm/giantswarm-configs/"+marker])
	require.Contains(t, again, "governance: governed\n")
	require.Contains(t, again, "  giantswarm-platform-manager:\n    safe:\n      - get_info\n  mcp-kubernetes: null\n")
	_, _, doc, _ = inputFromTheRecord(t, "platform-tenant-retired", func(string) string { return again })
	result, err = definition.Render(doc, nil, render.ModeCommit)
	require.NoError(t, err)
	require.Equal(t, again, string(result.Tree()["giantswarm/giantswarm-configs/"+marker]), "rendered from %s", patch)
}

// While no meta chart release renders the governance, a governance written
// by hand into the record is read back and refused at the reconcile, as
// when it is typed: the definition never carries a governance the
// installation's chart cannot enforce (fail closed).
func TestGovernanceOnRecordIsRefusedUntilItsReleaseExists(t *testing.T) {
	require.Empty(t, agentplatform.GovernanceChart, "the governance release is unset until it is cut")
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)
	_, read, doc, _ := inputFromTheRecord(t, "platform-tenant-retired", governanceOnRecord)
	require.Equal(t, agentplatform.GovernanceGoverned, read["governance.mode"])
	_, err := definition.Render(doc, nil, render.ModeCommit)
	require.ErrorContains(t, err, "(governance.mode) needs the meta chart release that renders muster's governance configuration")
}
