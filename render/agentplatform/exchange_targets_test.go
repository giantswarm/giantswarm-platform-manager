package agentplatform

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestTargetExchangeSelectionReachesBrokerAndProviders(t *testing.T) {
	raw, _ := loadInput(t, shapeHubPrivateTarget)
	input, err := Parse(raw)
	require.NoError(t, err)
	policy, err := loadPolicy()
	require.NoError(t, err)
	policy.Federation.Connector.ExchangeThrough[input.Installation.Customer] = render.ExchangeConnectors{
		Default: "giantswarm-simple-oidc",
		Targets: map[string]string{targetBurrow: ""},
	}
	input.Connectors, err = policy.connectors(input.Installation)
	require.NoError(t, err)
	broker := render.MustYAML(input.brokerValues())
	providers := string(render.MustYAML(input.identityProviders()))
	require.Contains(t, string(broker), "burrow:\n    dexTokenEndpoint: https://dex-burrow.agent-platform.svc.cluster.local:8443/token\n    expectedIssuer: https://dex.burrow.example.io\n    connectorId: giantswarm-gopher-oidc\n")
	require.Contains(t, providers, "burrow:\n  tokenEndpoint: https://dex-burrow.agent-platform.svc.cluster.local:8443/token\n  expectedIssuer: https://dex.burrow.example.io\n  connectorId: giantswarm-gopher-oidc\n")
	require.Contains(t, string(broker), "marmot:\n    dexTokenEndpoint: https://dex.marmot.example.io/token\n    connectorId: giantswarm-simple-oidc\n")
	require.Contains(t, providers, "marmot:\n  tokenEndpoint: https://dex.marmot.example.io/token\n  connectorId: giantswarm-simple-oidc\n")

	input.Installation.Federation.Targets[0].BrowseOnly = true
	require.Contains(t, string(render.MustYAML(input.brokerValues())), "connectorId: giantswarm-gopher-oidc")
	require.NotContains(t, string(render.MustYAML(input.identityProviders())), "burrow:")

	input.Installation.Federation.Targets[0].Hubs = []string{"another-hub", input.Installation.Name}
	require.Equal(t, input.Connectors.Further, input.exchangeConnector(input.Installation.Federation.Targets[0]))
}
