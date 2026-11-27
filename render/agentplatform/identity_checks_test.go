package agentplatform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	identityEntraConnector  = "giantswarm-ad"
	identityLegacyConnector = "giantswarm-simple-oidc"
	identityHubConnector    = "giantswarm-gopher-oidc"
	identityCustomer        = "browser-customer"
)

func TestIdentityCheckSelectionRequiresMatchingStaffExchange(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		selected   bool
		connectors []string
		cutover    bool
		wantError  string
	}{
		{name: "unselected"},
		{name: "null list", selected: true, wantError: "must be a list"},
		{name: "blank connector", selected: true, connectors: []string{" "}, wantError: "empty connector"},
		{name: "exchange still legacy", selected: true, connectors: []string{identityEntraConnector, identityHubConnector}, wantError: "selected staff exchange connector giantswarm-simple-oidc"},
		{name: "browser still legacy", selected: true, connectors: []string{identityEntraConnector, identityLegacyConnector}, cutover: true, wantError: "selected staff exchange connector giantswarm-gopher-oidc"},
		{name: "matching selection", selected: true, connectors: []string{identityEntraConnector, identityHubConnector}, cutover: true},
		{name: "rollback together", selected: true, connectors: []string{identityEntraConnector, identityLegacyConnector}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			policy, err := loadPolicy()
			require.NoError(t, err)
			policy.Federation.Connector.ExchangeThrough[portalTenant] = render.ExchangeConnectors{Default: identityLegacyConnector}
			installation := Installation{Name: targetBurrow, Federation: Federation{Connectors: []render.HubConnector{
				{Hub: portalCaseOwn, Customer: portalTenant, First: true},
			}}}
			if testCase.selected {
				policy.Federation.IdentityCheckConnectors = map[string][]string{installation.Name: testCase.connectors}
			}
			if testCase.cutover {
				selection := policy.Federation.Connector.ExchangeThrough[portalTenant]
				selection.Targets = map[string]string{installation.Name: ""}
				policy.Federation.Connector.ExchangeThrough[portalTenant] = selection
			}
			connectors, err := policy.identityCheckConnectors(installation)
			if testCase.wantError != "" {
				require.ErrorContains(t, err, testCase.wantError)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testCase.connectors, connectors)
			other, err := policy.identityCheckConnectors(Installation{Name: "another-target"})
			require.NoError(t, err)
			require.Nil(t, other)
		})
	}
}

func TestIdentityCheckRenderPreservesScopedOIDCAndCustomerLogins(t *testing.T) {
	for _, shape := range []string{shapeGiantswarmOwned, shapeGiantswarmSlackApp, shapePublicCustomer} {
		t.Run(shape, func(t *testing.T) {
			raw, _ := loadInput(t, shape)
			input, err := Parse(raw)
			require.NoError(t, err)
			input.IdentityCheckConnectors = nil
			before := string(render.MustYAML(input.musterValues()))
			require.NotContains(t, before, "identityCheckConnectors:")
			input.IdentityCheckConnectors = []string{"customer-login", identityEntraConnector, "giantswarm-gazelle-oidc"}
			after := string(render.MustYAML(input.musterValues()))
			require.Contains(t, after, "identityCheckConnectors:\n")
			for _, connector := range input.IdentityCheckConnectors {
				require.Contains(t, after, "- "+connector+"\n")
			}
			require.Equal(t, 1, strings.Count(after, "dex:"))
			require.NotContains(t, after, "allowPrivateIPOIDC: true")
			require.Equal(t, input.Installation.Private, strings.Contains(after, "allowPrivateIPOIDC: false"))
			require.NotContains(t, after, "connectorId:")
			var checks []render.Comparison
			for _, probe := range input.probes() {
				if probe.ID == "live-muster-connector-and-client-id" {
					checks = append(checks, probe.Expect.Compare...)
				}
			}
			require.Equal(t, []render.Comparison{
				{Live: "data.config.yaml:aggregator.oauth.server.dex.connectorId", Rendered: "muster.muster.oauth.server.dex.connectorId"},
				{Live: "data.config.yaml:aggregator.oauth.server.dex.identityCheckConnectors", Rendered: "muster.muster.oauth.server.dex.identityCheckConnectors", Exact: true},
			}, checks)
		})
	}
	policy, err := loadPolicy()
	require.NoError(t, err)
	policy.Federation.IdentityCheckConnectors = map[string][]string{identityCustomer: {}}
	connectors, err := policy.identityCheckConnectors(Installation{Name: identityCustomer})
	require.NoError(t, err)
	require.NotNil(t, connectors)
	input := &Input{IdentityCheckConnectors: connectors}
	require.Contains(t, string(render.MustYAML(input.musterValues())), "identityCheckConnectors: []")
}

func TestPilotPolicyAlignsBrowserBrokerAndProviderConnectors(t *testing.T) {
	const staffHub = "gazelle"
	const staffConnector = "giantswarm-gazelle-oidc"
	policy, err := loadPolicy()
	require.NoError(t, err)
	for _, installation := range []string{grantFixtureInstallation, "glean", "unselected-target"} {
		t.Run(installation, func(t *testing.T) {
			browser, err := policy.identityCheckConnectors(Installation{Name: installation, Federation: Federation{Connectors: []render.HubConnector{
				{Hub: staffHub, Customer: portalTenant, First: true},
			}}})
			require.NoError(t, err)
			expected := staffConnector
			if installation == "unselected-target" {
				require.Nil(t, browser)
			} else {
				require.Contains(t, browser, identityEntraConnector)
				require.Contains(t, browser, expected)
				require.NotContains(t, browser, identityLegacyConnector)
			}
			raw, _ := loadInput(t, shapeHubPrivateTarget)
			hub, err := Parse(raw)
			require.NoError(t, err)
			hub.Installation.Name = staffHub
			target := hub.Installation.Federation.Targets[0]
			target.Installation, target.Hubs = installation, []string{staffHub}
			hub.Installation.Federation.Targets = []Target{target}
			hub.Connectors, err = policy.connectors(hub.Installation)
			require.NoError(t, err)
			require.Contains(t, string(render.MustYAML(hub.brokerValues())), "connectorId: "+expected+"\n")
			require.Contains(t, string(render.MustYAML(hub.identityProviders())), "connectorId: "+expected+"\n")
		})
	}
}

func TestExchangeOnlyTargetsUseStaffConnector(t *testing.T) {
	const staffHub = "gazelle"
	const staffConnector = "giantswarm-gazelle-oidc"
	policy, err := loadPolicy()
	require.NoError(t, err)
	for _, installation := range []string{"agama", "alba", "alligator", "anemone", "armadillo", "avocet", "cedar", "enigma", "ferret", "finch", "gaggle", "galaxy", "garm", "gerbil", "glippy", "grizzly", "grouse", "jackal", "leopard", "sardine", "tamarin", "wallaby"} {
		t.Run(installation, func(t *testing.T) {
			browser, err := policy.identityCheckConnectors(Installation{Name: installation, Federation: Federation{Connectors: []render.HubConnector{
				{Hub: staffHub, Customer: portalTenant, First: true},
			}}})
			require.NoError(t, err)
			require.Nil(t, browser)
			raw, _ := loadInput(t, shapeHubPrivateTarget)
			hub, err := Parse(raw)
			require.NoError(t, err)
			hub.Installation.Name = staffHub
			target := hub.Installation.Federation.Targets[0]
			target.Installation, target.Hubs = installation, []string{staffHub}
			hub.Installation.Federation.Targets = []Target{target}
			hub.Connectors, err = policy.connectors(hub.Installation)
			require.NoError(t, err)
			require.Contains(t, string(render.MustYAML(hub.brokerValues())), "connectorId: "+staffConnector+"\n")
			require.Contains(t, string(render.MustYAML(hub.identityProviders())), "connectorId: "+staffConnector+"\n")
		})
	}
}
