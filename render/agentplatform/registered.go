package agentplatform

// What the installation registers with muster beyond the platform's own: its
// servers, as MCPServer objects under extras/agent-platform/mcpservers/, and
// its clients with a stable callback, as ConfigMaps under
// extras/agent-platform/mcpclients/. Both are facts of the record
// (installation.mcpServers, installation.mcpClients): the definition renders
// nothing per registration, reads each server's state live, and renders the
// two muster values the registrations decide.

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
}

// RegisteredClient is an MCP client registered on the installation with a
// stable HTTPS callback: a ConfigMap on record naming its redirect URIs.
type RegisteredClient struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirectURIs"`
}

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

// clientRedirectURIs are the redirect URIs of every registered client, in
// the registrations' order: muster's allowlist for dynamic registration of a
// client with a stable callback
// (muster.muster.oauth.server.trustedPublicRegistrationRedirectURIs).
func (in *Input) clientRedirectURIs() []string {
	var uris []string
	for _, c := range in.Installation.MCPClients {
		uris = append(uris, c.RedirectURIs...)
	}
	return uris
}
