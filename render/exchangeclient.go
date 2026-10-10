package render

// The token-exchange client: the client a hub's muster presents at an
// installation's Dex to exchange tokens into it. The pair has two sides in two
// installations' plans — the hub's credentials Secret for the installation and
// the installation's Dex-side Secret, one generated value — and each side
// names the other (Peer). The hub's definition renders the hub's side; the
// installation's own definition renders the Dex side — agent-platform where
// the platform runs there, cluster-mcp-servers where the installation runs
// its MCP servers without it — into the same files, so the pair survives the
// platform's enabling and disabling with its value, and whichever definition
// renders the Dex side, both sides render the client id and the value's name
// from the same two facts: the installation and whether the hub is the
// registry's.

// TokenExchangeClient is the id of the token-exchange client hub uses in
// target's Dex — the one the hub's credentials Secret for the target presents
// and the target's Dex registers. The registry's hub carries the fleet's id,
// muster-token-exchange-<target>: the client every installation registered
// for it by hand before the definition rendered one, so the credentials on
// record present it and the render adds no second client beside it. Every
// other hub's carries the hub's name too, muster-token-exchange-<target>-<hub>,
// since a target's Dex registers one client per hub and only one can carry
// the plain id. Both sides render the id from the same two facts, the target
// and whether the hub is the registry's, so the pair agrees whatever else
// each side sees.
func TokenExchangeClient(target, hub string, registryHub bool) string {
	id := "muster-token-exchange-" + target
	if registryHub {
		return id
	}
	return id + "-" + hub
}

// ExchangeSecretName is the generated value shared by the hub's credentials
// Secret for a target and the target's Dex-side copy: named after the client,
// which names the pair, so the two installations' filesets agree on which
// value they share. Each side names the other as its peer (Peer): the two are
// in two installations' plans, and a value drawn on one side alone never
// reaches the other. A target that renders no Dex side — neither the agent
// platform nor cluster-mcp-servers on record — keeps its Dex client by hand:
// no peer, the hub's side draws alone.
func ExchangeSecretName(client string) string { return client + "-client-secret" }

// ExchangeCredentialsSecret is a hub's credentials Secret for the target: the
// hub-side file carrying the hub's client in the target's Dex.
func ExchangeCredentialsSecret(target string) string { return target + "-token-exchange-credentials" }

// PlatformSecretsPath is a file of an installation's agent-platform Secrets
// in its management-clusters repository: where a pair's other side is on
// record, whichever definition renders it there.
func PlatformSecretsPath(installation, file string) string {
	return "management-clusters/" + installation + "/extras/agent-platform/secrets/" + file
}

// ExchangeTarget is the Dex side of the token exchange on one installation:
// the hubs whose muster exchanges tokens into it (installation.federation.hubs,
// in the registry's order) and the registry's hub by name
// (installation.federation.registryHub), among Hubs or not.
type ExchangeTarget struct {
	Installation string
	Hubs         []string
	RegistryHub  string
}

// Client is the id of hub's token-exchange client in the installation's Dex
// (TokenExchangeClient): the fleet's plain id for the registry's hub, the
// hub's name in it for every other hub.
func (t ExchangeTarget) Client(hub string) string {
	return TokenExchangeClient(t.Installation, hub, hub == t.RegistryHub)
}

// TrustedPeers are the hubs' clients as trusted peers of the authenticator
// (oidc.staticClients.dexK8SAuthenticator.trustedPeers), in the hubs' order:
// the hub's broker asks Dex for the cluster tokens (audience:server:client_id)
// through its client, so the peer is that client's id.
func (t ExchangeTarget) TrustedPeers() []string {
	peers := make([]string, 0, len(t.Hubs))
	for _, hub := range t.Hubs {
		peers = append(peers, t.Client(hub))
	}
	return peers
}

// DexClients are the hubs' clients as entries of the dex-app values'
// oidc.extraStaticClients, in the hubs' order: each with its secret read from
// the Secret in Dex's namespace the Dex side's fileset carries (Secret).
func (t ExchangeTarget) DexClients() []Map {
	clients := make([]Map, 0, len(t.Hubs))
	for _, hub := range t.Hubs {
		client := t.Client(hub)
		clients = append(clients, ExtraStaticClient(client, hub+" token exchange", Entry{Key: "secretRef", Value: DexClientSecretRef(client)}))
	}
	return clients
}

// Secret is the Dex-side Secret of hub's client, by its file name under the
// installation's agent-platform Secrets (PlatformSecretsPath): the generated
// value shared with the hub's credentials Secret for the installation, peered
// with that file in the hub's management-clusters repository.
func (t ExchangeTarget) Secret(hub string) (string, File) {
	client := t.Client(hub)
	value := ExchangeSecretName(client)
	f := DexClientSecret(client, value).Peered(value, Peer{Installation: hub, Path: PlatformSecretsPath(hub, ExchangeCredentialsSecret(t.Installation)+".yaml")})
	return DexClientSecretFile(client), f
}
