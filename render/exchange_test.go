package render

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The policy names one connector per hub and organisation — the
// organisation's plain name for its first hub into a target, the hub's own
// for a further one — and renders none for a hub of an organisation whose
// connectors the fleet's Dex base registers. An entry is an oidc connector
// trusting the hub's Dex as the issuer, with the groups and no client of its
// own; a template that cannot name one is ErrConnectorPolicy naming the key.
func TestExchangeConnectors(t *testing.T) {
	names, err := ConnectorPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if names.First != "{{ .Customer }}-simple-oidc" || names.Further != "{{ .Customer }}-{{ .Name }}-oidc" || !slices.Equal(names.FromBase, []string{"giantswarm"}) {
		t.Fatalf("the policy's federation.connector: %+v", names)
	}
	hubs := []HubConnector{
		{Hub: "gopher", Customer: "giantswarm", BaseDomain: "gopher.example.io", First: true},
		{Hub: "heron", Customer: "oakridge", BaseDomain: "heron.oakridge.example", First: true},
		{Hub: "kestrel", Customer: "oakridge", BaseDomain: "kestrel.oakridge.example"},
	}
	list, err := ExchangeConnectors(names, hubs)
	if err != nil {
		t.Fatal(err)
	}
	want := "- id: oakridge-simple-oidc\n  connectorType: oidc\n  connectorName: heron token exchange\n  connectorConfig: |\n    issuer: https://dex.heron.oakridge.example\n    insecureEnableGroups: true\n" +
		"- id: oakridge-kestrel-oidc\n  connectorType: oidc\n  connectorName: kestrel token exchange\n  connectorConfig: |\n    issuer: https://dex.kestrel.oakridge.example\n    insecureEnableGroups: true\n"
	if got := string(MustYAML(list)); got != want {
		t.Errorf("the connectors:\n%s\nwant:\n%s", got, want)
	}
	if none, err := ExchangeConnectors(names, hubs[:1]); err != nil || none != nil {
		t.Errorf("the fleet hub alone renders none: %v %v", none, err)
	}
	if none, err := ExchangeConnectors(names, nil); err != nil || none != nil {
		t.Errorf("no hub, no connector: %v %v", none, err)
	}
	broken := ConnectorNames{First: "{{ .Missing }}-oidc", Further: names.Further}
	if _, err := ExchangeConnectors(broken, hubs[1:2]); !errors.Is(err, ErrConnectorPolicy) || !strings.Contains(err.Error(), "federation.connector.first") {
		t.Errorf("a template naming a field the record lacks: %v", err)
	}
	if _, err := ExchangeConnectors(ConnectorNames{First: names.First}, hubs[2:]); !errors.Is(err, ErrConnectorPolicy) || !strings.Contains(err.Error(), "federation.connector.further") || !strings.Contains(err.Error(), "renders empty") {
		t.Errorf("a template rendering empty: %v", err)
	}
}
