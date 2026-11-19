package actions

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

func TestCapabilityOfTakesTheStagesOwnDefinition(t *testing.T) {
	a := Action{Spec: Spec{Capability: installations.ClusterMCPServers, Installations: []string{"gazelle", "garm"},
		CapabilityByInstallation: map[string]string{"gazelle": installations.AgentPlatform}}}
	require.Equal(t, installations.AgentPlatform, a.CapabilityOf("gazelle"))
	require.Equal(t, installations.ClusterMCPServers, a.CapabilityOf("garm"))
	require.Equal(t, installations.ClusterMCPServers, Action{Spec: Spec{Capability: installations.ClusterMCPServers}}.CapabilityOf("garm"))
}
