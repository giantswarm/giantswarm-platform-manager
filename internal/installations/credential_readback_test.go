package installations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const inputAgentManagerExchange = "agentManager.githubTokenExchange.enabled"

func TestAgentManagerCredentialReadBack(t *testing.T) {
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)
	for _, test := range []struct {
		name  string
		patch string
		want  map[string]any
	}{
		// #nosec G101 -- route configuration and public URLs, no credential value
		{"enabled", "agent-manager:\n  github:\n    enabled: false\n  githubTokenExchange:\n    endpoint: https://muster.rowan.acme.test/oauth/token\n  credentials:\n    uiURL: https://portal.acme.test/credentials/{operation}\n", map[string]any{
			"agentManager.github.enabled":    false,
			inputAgentManagerExchange:        true,
			"agentManager.credentials.uiURL": "https://portal.acme.test/credentials/{operation}",
		}},
		{"absent", "{}\n", map[string]any{inputAgentManagerExchange: false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			read := files(map[string]string{"acme/configs:" + definition.EnabledMarker("rowan"): test.patch})
			got, err := definition.ReadBack(t.Context(), read, readBackInstallation, readBackName, nil)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}
