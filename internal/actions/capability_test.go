package actions

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

const hubName, targetName = "gazelle", "garm"

func TestCapabilityOfTakesTheStagesOwnDefinition(t *testing.T) {
	a := Action{Spec: Spec{Capability: installations.ClusterMCPServers, Installations: []string{hubName, targetName},
		CapabilityByInstallation: map[string]string{hubName: installations.AgentPlatform}}}
	require.Equal(t, installations.AgentPlatform, a.CapabilityOf(hubName))
	require.Equal(t, installations.ClusterMCPServers, a.CapabilityOf(targetName))
	require.Equal(t, installations.ClusterMCPServers, Action{Spec: Spec{Capability: installations.ClusterMCPServers}}.CapabilityOf(targetName))
}

func TestFilterMatchesTheStagesOwnDefinition(t *testing.T) {
	mixed := Action{Spec: Spec{Capability: installations.ClusterMCPServers, Installations: []string{hubName, targetName},
		CapabilityByInstallation: map[string]string{hubName: installations.AgentPlatform}}}
	for _, tc := range []struct {
		name string
		f    Filter
		want bool
	}{
		{"hub by its own definition", Filter{Installation: hubName, Capability: installations.AgentPlatform}, true},
		{"hub by the action's definition", Filter{Installation: hubName, Capability: installations.ClusterMCPServers}, false},
		{"target by the action's definition", Filter{Installation: targetName, Capability: installations.ClusterMCPServers}, true},
		{"target by the hub's definition", Filter{Installation: targetName, Capability: installations.AgentPlatform}, false},
		{"any stage's definition", Filter{Capability: installations.AgentPlatform}, true},
		{"the action's definition", Filter{Capability: installations.ClusterMCPServers}, true},
		{"another definition", Filter{Capability: installations.CustomerPortal}, false},
		{"installation outside the action", Filter{Installation: "tamarin"}, false},
		{"no filter", Filter{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.f.matches(mixed))
		})
	}
}
