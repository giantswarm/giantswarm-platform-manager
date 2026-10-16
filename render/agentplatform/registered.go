package agentplatform

import (
	"fmt"
	"slices"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// What the installation registers with muster beyond the platform's own: its
// servers, as MCPServer objects under extras/agent-platform/mcpservers/, and
// its clients with a stable callback, as ConfigMaps under
// extras/agent-platform/mcpclients/. Both are facts of the record
// (installation.mcpServers, installation.mcpClients): the definition renders
// nothing per server, reads each server's state live, and renders the muster
// values the registrations decide; a confidential client it renders whole:
// its Dex client, the client's Secret and muster's audience (confidential).

// authExchange is the auth mode of a registered server muster exchanges the
// person's token for, in the agent-platform-mcps chart's words (the others:
// none, forward, oauth).
const authExchange = "exchange"

// RegisteredServer is an MCP server registered on the installation beyond
// the platform's own three: an MCPServer object on record.
type RegisteredServer struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Auth is how muster authenticates to it: none, forward, exchange or oauth.
	Auth string `json:"auth"`
	// DexTokenEndpoint is, for exchange, the Dex the exchange runs at.
	DexTokenEndpoint string `json:"dexTokenEndpoint,omitempty"`
	// GitHubGrant says, for oauth, that the server holds the person's own
	// GitHub grant: GitHub's authorization server, grants scoped to the
	// person (github.go).
	GitHubGrant bool `json:"githubGrant,omitempty"`
	// ToolPrefix is the prefix muster exposes the server's tools under
	// (spec.toolPrefix); empty, the server's name.
	ToolPrefix string `json:"toolPrefix,omitempty"`
	// Board says the server serves the roadmap board: pro, by muster's type
	// label (hive.go).
	Board bool `json:"board,omitempty"`
}

// RegisteredClient is an MCP client registered on the installation with a
// stable HTTPS callback: a ConfigMap on record naming its redirect URIs. A
// client with a ClientID is confidential: a Dex client with a secret of its
// own, which the client's operator configures it with, in place of a public
// registration with muster.
type RegisteredClient struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirectURIs"`
	// ClientID is a confidential client's Dex client id; empty, the client
	// is public.
	ClientID string `json:"clientId,omitempty"`
	// SecretFile is, for a confidential client registered by hand, the file
	// under extras/agent-platform/ that declares its Secret on record: the
	// render adopts it there, so its value stands.
	SecretFile string `json:"secretFile,omitempty"`
}

func (c RegisteredClient) confidential() bool { return c.ClientID != "" }

// confidentialClients are the registered confidential clients, in the
// registrations' order.
func (in *Input) confidentialClients() []RegisteredClient {
	var out []RegisteredClient
	for _, c := range in.Installation.MCPClients {
		if c.confidential() {
			out = append(out, c)
		}
	}
	return out
}

// confidentialDexClient is a confidential client's entry of the dex-app
// patch's oidc.extraStaticClients: its id, the registration's name and
// redirect URIs, and its secret referenced from dex-client-<id>.
func confidentialDexClient(c RegisteredClient) render.Map {
	return render.Map{e("id", c.ClientID), e("name", c.Name), e("redirectURIs", c.RedirectURIs), e("secretRef", dexClientRef(c.ClientID))}
}

// confidentialSecretName is the generated value of a confidential client's
// secret, drawn once and read by the client's operator from the cluster.
func confidentialSecretName(c RegisteredClient) string { return c.ClientID + "-dex-client-secret" }

// registeredExchange says whether a registered server exchanges the person's
// token at a Dex of its own. muster's token-exchange client then reaches that
// Dex: a workload cluster's, behind an internal load balancer, resolves to a
// private address, which the client's guard refuses unless allowed
// (muster.muster.oauth.mcpClient.tokenExchange.allowPrivateIP). The endpoint
// the guard is lifted for is on record, in the object.
func (in *Input) registeredExchange() bool {
	for _, s := range in.Installation.MCPServers {
		if s.Auth == authExchange {
			return true
		}
	}
	return false
}

// clientRedirectURIs are the redirect URIs of every registered public
// client, in the registrations' order: muster's allowlist for dynamic
// registration of a client with a stable callback
// (muster.muster.oauth.server.trustedPublicRegistrationRedirectURIs). A
// confidential client registers with Dex, not with muster.
func (in *Input) clientRedirectURIs() []string {
	var uris []string
	for _, c := range in.Installation.MCPClients {
		if !c.confidential() {
			uris = append(uris, c.RedirectURIs...)
		}
	}
	return uris
}

// checkConfidentialClients refuses a confidential client whose id is a Dex
// client the definition renders for the platform: the registration would
// take over that client's entry, its Secret and its generated value.
func (in *Input) checkConfidentialClients() error {
	platform := []string{in.Installation.MusterClientID, componentMuster, authenticatorClient, componentKagent, render.PortalDexClientID}
	for _, hub := range in.Installation.Federation.Hubs {
		platform = append(platform, in.targetClient(hub))
	}
	for _, c := range in.confidentialClients() {
		if slices.Contains(platform, c.ClientID) {
			return refuse(fmt.Sprintf("%s registers the confidential client %q under the id %s, a Dex client the platform renders itself; give the registration a data.clientId of its own", describe("installation.mcpClients"), c.Name, c.ClientID))
		}
	}
	return nil
}
