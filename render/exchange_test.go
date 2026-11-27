package render

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fleetOrganisation is the organisation whose further hubs' connectors the
// policy keeps by hand.
const fleetOrganisation = "giantswarm"

// customerOrganisation is an organisation the policy treats like any other.
const customerOrganisation = "oakridge"

const (
	legacyExchangeConnector   = "giantswarm-simple-oidc"
	renderedExchangeConnector = "giantswarm-gopher-oidc"
	targetVole                = "vole"
	customExchangeConnector   = "another-connector"
)

// The policy names one connector per hub and organisation — the
// organisation's plain name for its first hub into a target, the hub's own
// for a further one and for every hub of a staff organisation — and renders
// none for a further hub of an organisation whose further hubs' connectors
// are kept by hand. A staff organisation's connectors go under
// oidc.giantswarm, every other one's under oidc.customer. An entry is an oidc
// connector trusting the hub's Dex as the issuer, with the groups and the
// hub's login client for the target, its secret referenced; a template that
// cannot name one is ErrConnectorPolicy naming the key.
func TestFederatedConnectors(t *testing.T) {
	names, err := ConnectorPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if names.First != "{{ .Customer }}-simple-oidc" || names.Further != "{{ .Customer }}-{{ .Name }}-oidc" ||
		!slices.Equal(names.Staff, []string{fleetOrganisation}) || !slices.Equal(names.FurtherByHand, []string{fleetOrganisation}) ||
		len(names.ExchangeThrough) != 0 {
		t.Fatalf("the policy's federation.connector: %+v", names)
	}
	hubs := []HubConnector{
		{Hub: "gopher", Customer: fleetOrganisation, BaseDomain: "gopher.example.io", First: true},
		{Hub: "warren", Customer: fleetOrganisation, BaseDomain: "warren.example.io"},
		{Hub: "heron", Customer: customerOrganisation, BaseDomain: "heron.oakridge.example", First: true},
		{Hub: "kestrel", Customer: customerOrganisation, BaseDomain: "kestrel.oakridge.example"},
	}
	list, err := FederatedConnectors(names, hubs)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range list {
		got = append(got, c.Owner+"/"+c.ID)
	}
	if want := []string{"giantswarm/giantswarm-gopher-oidc", "customer/oakridge-simple-oidc", "customer/oakridge-kestrel-oidc"}; !slices.Equal(got, want) {
		t.Errorf("the connectors: %v, want %v (the kept-by-hand further hub renders none)", got, want)
	}
	want := "connectorType: oidc\nconnectorName: kestrel\nconfig:\n" +
		"  issuer: https://dex.kestrel.oakridge.example\n  insecureEnableGroups: true\n  clientID: federation-plover\n" +
		"  redirectURI: https://dex.plover.oakridge.example/callback\n  scopes:\n    - openid\n    - profile\n    - email\n    - groups\n    - offline_access\n" +
		"clientSecretRef:\n  name: dex-connector-oakridge-kestrel-oidc\n  key: client-secret\n"
	if got := string(MustYAML(list[2].Entry("plover", "plover.oakridge.example"))); got != want {
		t.Errorf("the entry:\n%s\nwant:\n%s", got, want)
	}
	byOwner := string(MustYAML(ConnectorsByOwner(list, "plover", "plover.oakridge.example")))
	if !strings.HasPrefix(byOwner, "giantswarm:\n  connectors:\n    giantswarm-gopher-oidc:\n") ||
		!strings.Contains(byOwner, "\ncustomer:\n  connectors:\n    oakridge-simple-oidc:\n") || !strings.Contains(byOwner, "\n    oakridge-kestrel-oidc:\n") {
		t.Errorf("the connectors by owner:\n%s", byOwner)
	}
	if none, err := FederatedConnectors(names, hubs[1:2]); err != nil || none != nil {
		t.Errorf("a kept-by-hand further hub alone renders none: %v %v", none, err)
	}
	if none, err := FederatedConnectors(names, nil); err != nil || none != nil {
		t.Errorf("no hub, no connector: %v %v", none, err)
	}
	broken := ConnectorNames{First: "{{ .Missing }}-oidc", Further: names.Further}
	if _, err := FederatedConnectors(broken, hubs[2:3]); !errors.Is(err, ErrConnectorPolicy) || !strings.Contains(err.Error(), "federation.connector.first") {
		t.Errorf("a template naming a field the record lacks: %v", err)
	}
	if _, err := FederatedConnectors(ConnectorNames{First: names.First}, hubs[3:]); !errors.Is(err, ErrConnectorPolicy) || !strings.Contains(err.Error(), "federation.connector.further") || !strings.Contains(err.Error(), "renders empty") {
		t.Errorf("a template rendering empty: %v", err)
	}
}

// With no exchangeThrough selection every hub, a staff organisation's first
// hub included, exchanges tokens through the connector the target registers
// for it.
func TestExchangeConnector(t *testing.T) {
	names, err := ConnectorPolicy()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		first          bool
		customer, want string
	}{
		{true, fleetOrganisation, renderedExchangeConnector},
		{false, fleetOrganisation, renderedExchangeConnector},
		{true, customerOrganisation, "oakridge-simple-oidc"},
		{false, customerOrganisation, "oakridge-gopher-oidc"},
	} {
		got, err := names.Exchange(tc.first, tc.customer, "gopher", burrow)
		if err != nil || got != tc.want {
			t.Errorf("Exchange(%v, %s) = %s, %v; want %s", tc.first, tc.customer, got, err, tc.want)
		}
	}
}

func TestExchangeConnectorTargetSelection(t *testing.T) {
	names, err := ConnectorPolicy()
	require.NoError(t, err)
	names.ExchangeThrough[fleetOrganisation] = ExchangeConnectors{
		Default: legacyExchangeConnector,
		Targets: map[string]string{burrow: "", targetVole: customExchangeConnector},
	}
	for _, testCase := range []struct {
		name, customer, target, want string
		first                        bool
	}{
		{"selected target", fleetOrganisation, burrow, renderedExchangeConnector, true},
		{"explicit connector", fleetOrganisation, targetVole, customExchangeConnector, true},
		{"unselected target", fleetOrganisation, "marmot", legacyExchangeConnector, true},
		{"further hub", fleetOrganisation, targetVole, renderedExchangeConnector, false},
		{"customer hub", customerOrganisation, burrow, "oakridge-simple-oidc", true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := names.Exchange(testCase.first, testCase.customer, "gopher", testCase.target)
			require.NoError(t, err)
			require.Equal(t, testCase.want, got)
		})
	}
	delete(names.ExchangeThrough[fleetOrganisation].Targets, burrow)
	rolledBack, err := names.Exchange(true, fleetOrganisation, "gopher", burrow)
	require.NoError(t, err)
	require.Equal(t, legacyExchangeConnector, rolledBack)
}
