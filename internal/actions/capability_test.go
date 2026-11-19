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

func TestFilterMatchesTheStagesOwnDefinition(t *testing.T) {
	mixed := Action{Spec: Spec{Capability: installations.ClusterMCPServers, Installations: []string{"gazelle", "garm"},
		CapabilityByInstallation: map[string]string{"gazelle": installations.AgentPlatform}}}
	for _, tc := range []struct {
		name string
		f    Filter
		want bool
	}{
		{"hub by its own definition", Filter{Installation: "gazelle", Capability: installations.AgentPlatform}, true},
		{"hub by the action's definition", Filter{Installation: "gazelle", Capability: installations.ClusterMCPServers}, false},
		{"target by the action's definition", Filter{Installation: "garm", Capability: installations.ClusterMCPServers}, true},
		{"target by the hub's definition", Filter{Installation: "garm", Capability: installations.AgentPlatform}, false},
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
