package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

func TestCapabilitiesOnNamesEachStagesDefinition(t *testing.T) {
	single := &actions.Action{Spec: actions.Spec{Capability: installations.ClusterMCPServers, Installations: []string{rtGarm, rtMaple}}}
	require.Equal(t, "*cluster-mcp-servers* on *garm, maple*", capabilitiesOn(single))
	mixed := &actions.Action{Spec: actions.Spec{Kind: actions.KindReconcile, Actor: actions.Actor{Login: "alice"}, Capability: installations.ClusterMCPServers, Installations: []string{rtHub, rtGarm, rtMaple},
		CapabilityByInstallation: map[string]string{rtHub: installations.AgentPlatform}}}
	require.Equal(t, "*agent-platform* on *gazelle* and *cluster-mcp-servers* on *garm, maple*", capabilitiesOn(mixed))
	require.Contains(t, reviewText(mixed), "asks to reconcile *agent-platform* on *gazelle* and *cluster-mcp-servers* on *garm, maple*")
}
