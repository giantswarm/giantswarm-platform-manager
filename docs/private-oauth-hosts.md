# Private OAuth hosts and proxies

The agent-platform renderer pins klaus-gateway's client metadata in Muster and
Muster's metadata in mcp-kubernetes on private installations. These clients do not
need a metadata fetch. Redirect URIs, trusted-issuer JWKS and exchange token
endpoints use the consumers' host allowlists. Muster's blanket
`dex.allowPrivateIPOIDC` permission is explicitly false so shared defaults cannot
enable it. Its configured Dex issuer can still resolve privately.

Private installations, hubs with private targets and installations with registered
exchange servers require chart line 4, agent-platform 4.185.0-rc.1 or later,
and Muster 5.64.0 or later. Private installations also require mcp-kubernetes
1.16.0 or later for the client pin. An incompatible hold is refused before
rendering. A hub lists each private target's tunnelled Dex host,
including browse-only targets: mounting the tunnel CA without a host list selects
Muster's legacy unrestricted exchange transport.

Outbound proxies are independent of private ingress. The guarded HTTP clients
honour `HTTPS_PROXY` and `NO_PROXY`, resolve and validate the destination locally,
and send CONNECT to the validated IP. TLS still verifies the original hostname.
An IP-capable proxy supports this path. A proxy that permits CONNECT only to
hostnames can reject unpinned public CIMD clients and guarded exchange or JWKS
requests. Pinning the two internal clients does not make every public client work
through such a proxy. Keep CIMD restrict mode off for other public clients.

Before installation rollout, verify sign-in for the pinned clients, registered
clients and direct public CIMD clients through the installation's actual proxy
and DNS path. Check token exchange through each private target tunnel, the CA
bundle and `NO_PROXY` routing for in-cluster services. A domain-only proxy needs
an explicit network policy decision for validated-IP CONNECT; do not restore a
blanket private-address permission to bypass it.

Standalone cluster MCP servers retain their explicit private-address settings.
They use fleet bases rather than the agent-platform meta chart, and have no
replacement client inventory here. Retiring those settings requires recorded
client pins and deployed consumers on mcp-oauth 1.15.0 or later. The
agent-platform chart hold checks do not establish those standalone prerequisites.
