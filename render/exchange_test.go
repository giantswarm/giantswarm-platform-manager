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
// for a further one — and renders none for a further hub of an organisation
// whose further hubs' connectors are kept by hand. An entry is an oidc
// connector trusting the hub's Dex as the issuer, with the groups and the
// hub's login client for the target; a template that cannot name one is
// ErrConnectorPolicy naming the key.
func TestFederatedConnectors(t *testing.T) {
	names, err := ConnectorPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if names.First != "{{ .Customer }}-simple-oidc" || names.Further != "{{ .Customer }}-{{ .Name }}-oidc" || !slices.Equal(names.FurtherByHand, []string{fleetOrganisation}) {
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
	var ids []string
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	if want := []string{"giantswarm-simple-oidc", "oakridge-simple-oidc", "oakridge-kestrel-oidc"}; !slices.Equal(ids, want) {
		t.Errorf("the connectors: %v, want %v (the kept-by-hand further hub renders none)", ids, want)
	}
	want := "id: oakridge-kestrel-oidc\nconnectorType: oidc\nconnectorName: kestrel token exchange\nconnectorConfig: |\n" +
		"  issuer: https://dex.kestrel.oakridge.example\n  insecureEnableGroups: true\n" +
		"  clientID: $DEX_CONNECTOR_OAKRIDGE_KESTREL_OIDC_CLIENT_ID\n  clientSecret: $DEX_CONNECTOR_OAKRIDGE_KESTREL_OIDC_CLIENT_SECRET\n" +
		"  redirectURI: https://dex.plover.oakridge.example/callback\n  scopes:\n    - openid\n    - profile\n    - email\n    - groups\n"
	if got := string(MustYAML(list[2].Entry("plover.oakridge.example"))); got != want {
		t.Errorf("the entry:\n%s\nwant:\n%s", got, want)
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
