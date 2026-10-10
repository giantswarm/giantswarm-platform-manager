package render

import (
	"slices"
	"strings"
	"testing"
)

// The invented installations of the tests: a target of the registry's hub
// and of a second hub, whose client in the target's Dex carries its name.
const (
	burrow       = "burrow"
	gopher       = "gopher"
	warren       = "warren"
	warrenClient = "muster-token-exchange-burrow-warren"
)

// Both sides of a pair render the client id and the value's name from the
// target and whether the hub is the registry's: the registry's hub carries
// the fleet's plain id, every other hub its own name too.
func TestTokenExchangeClient(t *testing.T) {
	cases := []struct {
		target, hub string
		registryHub bool
		want        string
	}{
		{burrow, gopher, true, "muster-token-exchange-burrow"},
		{burrow, warren, false, warrenClient},
	}
	for _, tc := range cases {
		if got := TokenExchangeClient(tc.target, tc.hub, tc.registryHub); got != tc.want {
			t.Errorf("TokenExchangeClient(%s, %s, %v) = %s, want %s", tc.target, tc.hub, tc.registryHub, got, tc.want)
		}
		if got := ExchangeSecretName(tc.want); got != tc.want+"-client-secret" {
			t.Errorf("ExchangeSecretName(%s) = %s", tc.want, got)
		}
	}
}

// The Dex side of the exchange on a target of two hubs, the registry's and
// another: the trusted peers and the extra static clients in the hubs' order,
// the registry's hub's under the plain id, and each hub's Secret the generated
// value of the pair, peered with the hub's credentials Secret for the target.
func TestExchangeTarget(t *testing.T) {
	x := ExchangeTarget{Installation: burrow, Hubs: []string{gopher, warren}, RegistryHub: gopher}
	if peers := x.TrustedPeers(); !slices.Equal(peers, []string{"muster-token-exchange-burrow", warrenClient}) {
		t.Errorf("trusted peers: %v", peers)
	}
	clients := x.DexClients()
	if len(clients) != 2 || clients[0][0].Value != "muster-token-exchange-burrow" || clients[1][0].Value != warrenClient ||
		clients[1][1].Value != "warren token exchange" || clients[1][2].Value.(Map)[0].Value != "dex-client-muster-token-exchange-burrow-warren" {
		t.Errorf("dex clients: %v", clients)
	}
	file, f := x.Secret(warren)
	if file != "dex-client-muster-token-exchange-burrow-warren-secret.yaml" {
		t.Errorf("file: %s", file)
	}
	if !strings.Contains(string(f.Content), "name: dex-client-muster-token-exchange-burrow-warren\n") || !strings.Contains(string(f.Content), "namespace: "+DexNamespace+"\n") {
		t.Errorf("secret:\n%s", f.Content)
	}
	want := Peer{Installation: warren, Path: "management-clusters/warren/extras/agent-platform/secrets/burrow-token-exchange-credentials.yaml"}
	if len(f.Generated) != 1 || f.Generated[0].Name != "muster-token-exchange-burrow-warren-client-secret" || f.Generated[0].Peer == nil || *f.Generated[0].Peer != want {
		t.Errorf("generated: %+v", f.Generated)
	}
	if x.Client("other") != "muster-token-exchange-burrow-other" {
		t.Errorf("a hub not among Hubs still names its client: %s", x.Client("other"))
	}
}
