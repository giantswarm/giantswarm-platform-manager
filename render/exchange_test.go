package render

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// fleetOrganisation is the organisation whose further hubs' connectors the
// policy keeps by hand.
const fleetOrganisation = "giantswarm"

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
		names.ExchangeThrough[fleetOrganisation] != "giantswarm-simple-oidc" {
		t.Fatalf("the policy's federation.connector: %+v", names)
	}
	hubs := []HubConnector{
		{Hub: "gopher", Customer: fleetOrganisation, BaseDomain: "gopher.example.io", First: true},
		{Hub: "warren", Customer: fleetOrganisation, BaseDomain: "warren.example.io"},
		{Hub: "heron", Customer: "oakridge", BaseDomain: "heron.oakridge.example", First: true},
		{Hub: "kestrel", Customer: "oakridge", BaseDomain: "kestrel.oakridge.example"},
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
		"  redirectURI: https://dex.plover.oakridge.example/callback\n  scopes:\n    - openid\n    - profile\n    - email\n    - groups\n" +
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

// A staff organisation's first hub exchanges tokens through the connector
// exchangeThrough names until its people's subjects move; every other hub
// through the connector the target registers for it.
func TestExchangeConnector(t *testing.T) {
	names, err := ConnectorPolicy()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		first          bool
		customer, want string
	}{
		{true, fleetOrganisation, "giantswarm-simple-oidc"},
		{false, fleetOrganisation, "giantswarm-gopher-oidc"},
		{true, "oakridge", "oakridge-simple-oidc"},
		{false, "oakridge", "oakridge-gopher-oidc"},
	} {
		got, err := names.Exchange(tc.first, tc.customer, "gopher")
		if err != nil || got != tc.want {
			t.Errorf("Exchange(%v, %s) = %s, %v; want %s", tc.first, tc.customer, got, err, tc.want)
		}
	}
}
