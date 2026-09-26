# cluster-mcp-servers definition

The content of the `cluster-mcp-servers` capability definition: a management cluster's own MCP servers
(mcp-kubernetes, mcp-prometheus, mcp-capi) with their Valkeys on an installation without the agent
platform. Data only; the render library (`render/clustermcpservers`, the servers' files from
`render/mcpservers`, which the agent-platform definition renders for the same servers) reads it.

| File | What it is |
|---|---|
| `schema.json` | JSON Schema (draft 2020-12, `additionalProperties: false`) of the inputs: the facts on record under `installation` and, per server, whether it runs and how it reaches Dex (`privateURLs`, `privateIPs`, `dexCA`). Whether a server runs is read from the record — its extras directory's kustomization — where the capability is on record; a fresh enable runs all three. The Dex settings are read back from the server's user values. |
| `removals.yaml` | The servers' inline Dex client secrets in the encrypted Dex values: the client reads a referenced Secret instead, dex-app ignores the inline value next to it, and a person deletes it (the definition decrypts nothing). |
| `migrations.yaml` | What an installation whose servers were set up by hand lacks and the first reconcile adds: the credentials revision, the Dex client Secret, the kustomization's entries and patches, the reference in the Dex patch. |
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
as `clientSecretRef` in `installations/<name>/apps/dex-app/configmap-values.yaml.patch`, a file whose other
clients and keys stay their owners'.

## Refusals

- The agent platform is on record: the agent-platform definition renders the installation's servers.
- dex-app older than 3.2.3: before it, Dex keeps a rotated client secret until someone restarts it, so a
  rotation would fail the servers' sign-ins.
- No server runs, a supplied value (the definition generates every credential), an input the schema rejects.
