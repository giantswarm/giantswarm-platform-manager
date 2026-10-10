# cluster-mcp-servers definition

The content of the `cluster-mcp-servers` capability definition: a management cluster's own MCP servers
(mcp-kubernetes, mcp-prometheus, mcp-capi) with their Valkeys on an installation without the agent
platform, and the token-exchange clients of the hubs that reach them. Data only; the render library
(`render/clustermcpservers`, the servers' files from `render/mcpservers` and the hubs' clients from
`render.ExchangeTarget`, both of which the agent-platform definition renders for the same servers and the
same pairing) reads it.

| File | What it is |
|---|---|
| `schema.json` | JSON Schema (draft 2020-12, `additionalProperties: false`) of the inputs: the facts on record under `installation` — the hubs whose muster exchanges tokens into the installation and the registry's hub among them (`federation`) — and, per server, whether it runs and how it reaches Dex (`privateURLs`, `privateIPs`, `dexCA`). Whether a server runs is read from the record — its extras directory's kustomization — where the capability is on record; a fresh enable runs all three. The Dex settings are read back from the server's user values. |
| `removals.yaml` | The servers' inline Dex client secrets in the encrypted Dex values: the client reads a referenced Secret instead, dex-app ignores the inline value next to it, and a person deletes it (the definition decrypts nothing). |
| `migrations.yaml` | What an installation whose servers were set up by hand lacks and the first reconcile adds: the credentials revision, the Dex client Secret, the kustomization's entries and patches, the reference in the Dex patch; and the hubs' token-exchange clients where they were registered by hand or the platform rendered them before it was disabled. |
| `features.yaml` | The consistency features (servers, identity) and their dimensions. |
| `probes.yaml` | The anonymous probe: Dex's discovery document. |

## The fileset

Per running server, `management-clusters/<name>/extras/mcp-<server>/` of the management-clusters
repository: the kustomization over the fleet base, the OAuth credentials and valkey-auth Secrets, the Dex
client's Secret in Dex's namespace, the credentials revision Secret in the Flux namespace the server's and
its Valkey's HelmReleases read into their charts' checksum values (a rotation of the server's credentials
restarts both, a reconcile without one restarts nothing), and the user values where Dex is reached on private
addresses or served with a certificate from the installation's private CA (the installation's own Secret
`mcp-<server>-dex-ca`, listed, never rendered). In the configs repository, each running server's Dex client
as `clientSecretRef`, and the token-exchange connector of every customer hub that brokers into the
installation under `oidc.customer.connectors` (an `oidc` connector trusting the hub's Dex as the issuer,
named by the agent-platform policy's `federation.connector` over the hub's record as the hub names it in its
broker; none for a hub of an organisation under that policy's `fromBase`, whose connector the fleet's Dex
base registers), in `installations/<name>/apps/dex-app/configmap-values.yaml.patch`, a file whose other
clients, connectors and keys stay their owners'.

Per hub that exchanges tokens into the installation (`installation.federation.hubs`), its token-exchange
client in that patch — `muster-token-exchange-<name>` for the registry's hub, `muster-token-exchange-<name>-<hub>`
for every other, under `oidc.extraStaticClients` with its secret in a referenced Secret, and as a trusted
peer of the authenticator, so the hub's broker gets the cluster tokens it exchanges for — and that Secret
in `management-clusters/<name>/extras/agent-platform/secrets/`, with the kustomization of that directory and
of `extras/agent-platform/` listing it and the include of `./agent-platform/` in the extras kustomization.
These are the files, paths and entries the agent-platform definition renders for the same pairing: the
secret is one generated value with the hub's credentials Secret for the installation, in the hub's plan,
and each side names the other, so a plan that would draw it on one side alone is refused. Keeping the
Dex side in the platform's file is what lets the pair keep its value: a disable of the platform leaves
the Secret on record as this definition's (the manager decrypts nothing, so a value cannot move to
another file), and an enable finds it there.

## Refusals

- The agent platform is on record: the agent-platform definition renders the installation's servers.
- dex-app older than 3.2.3 (2.4.0 on the 2.x line): before it, Dex keeps a rotated client secret until someone restarts it, so a
  rotation would fail the servers' sign-ins.
- No server runs, a supplied value (the definition generates every credential), an input the schema rejects.
