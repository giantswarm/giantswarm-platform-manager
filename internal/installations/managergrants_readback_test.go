package installations

import (
	"fmt"
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	grantClusterManager = "cluster-manager"
	grantModelManager   = "model-manager"
	grantSkillManager   = "skill-manager"
	grantExchangeField  = "tokenExchange"
)

func managerGrantDefaults(choices map[string]any) map[string]any {
	want := map[string]any{
		"clusterManager.github.tokenExchange.enabled": false,
		"modelManager.github.tokenExchange.enabled":   false,
		"skillManager.github.tokenExchange.enabled":   false,
	}
	maps.Copy(want, choices)
	return want
}

func TestManagerGrantReadBack(t *testing.T) {
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)
	for _, manager := range []struct{ component, input string }{
		{grantClusterManager, "clusterManager"},
		{grantModelManager, "modelManager"},
		{grantSkillManager, "skillManager"},
	} {
		for _, scenario := range []struct {
			name, values string
			exchange     bool
			pin          *bool
		}{
			{"on demand", "  github:\n    enabled: true\n    tokenExchange:\n      tokenEndpoint: https://muster.rowan.acme.test/oauth/token\n", true, nil},
			{"former key", "  githubTokenExchange:\n    endpoint: https://muster.rowan.acme.test/oauth/token\n", true, nil},
			{"legacy access", "  github:\n    enabled: true\n", false, new(true)},
			{"empty exchange", "  github:\n    enabled: true\n    tokenExchange: {}\n", false, nil},
			{"not configured", "{}\n", false, nil},
		} {
			t.Run(manager.component+"/"+scenario.name, func(t *testing.T) {
				patch := fmt.Sprintf("%s:\n%s", manager.component, scenario.values)
				if scenario.name == "not configured" {
					patch = scenario.values
				}
				read := files(map[string]string{"acme/configs:" + definition.EnabledMarker("rowan"): patch})
				got, err := definition.ReadBack(t.Context(), read, readBackInstallation, readBackName, nil)
				require.NoError(t, err)
				require.Equal(t, scenario.exchange, got[manager.input+".github.tokenExchange.enabled"])
				pin := manager.input + ".github.enabled"
				if scenario.pin == nil {
					require.NotContains(t, got, pin)
				} else {
					require.Equal(t, *scenario.pin, got[pin])
				}
			})
		}
	}
}
