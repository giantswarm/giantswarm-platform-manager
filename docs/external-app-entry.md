# External application entry

The agent-platform definition owns `klausGateway.a2aServer`. It is disabled by
default. When enabled it renders the upstream agentgateway `jwtAuthentication`
and `oauthTokenExchange` configuration, the exchange and audience clients, and
exchange-only OIDC connectors in Dex. The gateway can run without a Slack app.

Use the platformctl plan, approval and commit workflow to change an installation.
Do not hand-edit its rendered Dex configuration. This input depends on
[agent-platform #1511](https://github.com/giantswarm/agent-platform/pull/1511),
[Dex #106](https://github.com/giantswarm/dex/pull/106), and
[dex-app #591](https://github.com/giantswarm/dex-app/pull/591). The released chart,
Dex image and component version floors must carry those changes before rollout.

```yaml
tenants:
  platform:
    users: []
musterMcpServerViewers:
- kind: Group
  name: staff:agent-users
klausGateway:
  a2aServer:
    enabled: true
    staffConnectors: [staff]
    exchangeSecretRef:
      name: a2a-exchange
      key: client-secret
    issuers:
    - name: application-users
      issuer: https://identity.example.com
      audience: application
      allowedClients: [application]
      connectorId: a2a-application-users
      jwks:
        host: identity.example.com
        port: 443
        path: /keys
```

The operator supplies the exchange Secret with the same value in `agent-platform`
and Dex's namespace. Application clients reference existing Secrets in Dex's
namespace. The definition accepts references and never accepts or generates their
secret values.

Resource discovery and invocation use existing RBAC. Explicitly shared resources
remain accessible across organizations; an organization name is not an extra
permission boundary. The platform user and MCP-server viewer subject lists must
be explicit and cannot contain broad system groups. Existing interactive clients
use `staffConnectors`; exchange-only connectors must have different IDs. Other
values sources that register confidential clients must also restrict their grants
before `client_credentials` is enabled installation-wide.

For application identities, add `applications` entries with `id`, `name` and
`secretRef: {name, key}`. Include the installation's Dex in `issuers` with audience
`klaus-gateway-app`, the application IDs in `allowedClients`, and a distinct
exchange-only connector ID. The application requests the
`audience:server:client_id:klaus-gateway-app` scope alongside its required claims.
Only these application clients receive `client_credentials`; the exchange client
receives token exchange only. A reconcile reads the application references and
grant restrictions back from the Dex patch.

An application identity and a session reference do not authenticate an end user.
Customer backends use muster's backend OAuth flow and their own user access
tokens. Platform bearer tokens must not be forwarded to customer backends.
Independent security sign-off and an attributable customer operation remain
rollout and acceptance requirements.
