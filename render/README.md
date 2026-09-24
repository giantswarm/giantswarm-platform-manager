# render — the capability render library

`render` and its definitions (`render/agentplatform`, `render/customerportal`) turn an installation's inputs into the files of
its GitOps repositories: a `Result` with the files by repository and repository-relative path, and the
entries a shared `kustomization.yaml` must list.

## The boundary

The library has **no I/O**. It reads nothing from disk or the network, runs no `git`, no `sops`, no
`kubectl`, and generates no secret. Its only inputs are the decoded input document and the values a
person supplies; its only output is the `Result`. A Secret manifest is rendered in plaintext with a
placeholder (`GENERATED(<name>)`) per value the platform needs and nobody types; `File.Generated`
describes each placeholder (name, kind, length, and the encoding it receives) for the commit step —
gitops-commit's `sopsenc` — to fill in and encrypt for the repository's recipients. Two files carrying
the same name receive the same value: that is how a Dex client and the workload presenting its secret
agree. Where the consumer decodes the leaf — a Secret key under `data:`, or a chart that copies a value
under `data:` as it is — the declaration names the encoding and its placeholder is the name's, qualified
by it (`GENERATED(<name>.base64)`, `Generated.Encoded`): the portal's client secret lands raw in the
Dex client's Secret and base64 in the chart values the backstage chart copies under `data:`. A literal
or a supplied value such a leaf carries is encoded by the definition itself; a marker of a dry run stays
a marker.

Secret files are named so the fleet's `.sops.yaml` rules and the commit step's secret-file test match
them (`*secret*`, `*credential*`). Encrypted files are never read or edited: a Dex client is a plaintext
list entry with a `secretRef` and a new Secret file.

## The agent-platform definition

Inputs are exactly the keys of `definitions/agent-platform/schema.json`, validated against it: an
unknown key refuses the render naming the key; a supplied secret value that is needed and empty refuses
naming the field. The fleet policy (`definitions/agent-platform/policy.yaml`) says which components an
owning organisation's installations run and which installations a Slack app exists for; where one does,
klaus-gateway renders (and cluster-manager where the organisation's list names it) as data
(`components.go`): the gateway's route, OBO links and Slack with their Secrets referenced (the OBO keys
generated, the Slack credentials supplied as `klausGateway.slack.<key>`), the manager's installation and, on
the 3 chart line, the OAuth section a manager needs without the template's global identity block. The
developer portal's section (`portal.go`) is a directory of the platform's own, `extras/backstage/agent-platform/`
in the portal host's tree, a kustomize Component the portal's `extras/backstage/kustomization.yaml` lists
(`Include.Component`): the platform's Backstage configuration as an extra app-config file the chart mounts from a
ConfigMap, the chart values that mount it, with the AI chat on (`aiChat.enabled`, `aiChat.model`,
`aiChat.provider`, the person's other choice) the chat's credentials Secret (on Anthropic's API the key, supplied
as `aiChat.anthropic.apiKey`, as the chart value the chart exposes as `ANTHROPIC_API_KEY`; on Vertex AI the service
account's JSON, supplied as `aiChat.google.credentialsJson`, as the chart value the chart mounts at
`/app/google/credentials.json` — with the Slack credentials the only values a person supplies at commit; where the
portal's own app-config carries the chat by hand, `installation.portals[*].handKeptChat`, its environment supplies the
credential already and the Component renders the chat's blocks without a Secret and asks for no value, so a wave
reconciles the portal), and a patch appending those sources to the portal HelmRelease's `valuesFrom` — last, so the
platform's values win. The fragment carries the platform's section, and on a portal the customer-portal
definition renders the shared extension list with the platform's section and the installation's muster entry;
Backstage and Helm replace lists wholesale, so on a hand-kept portal (a literal `app.extensions` on record) the
Component writes its object-shaped keys alone and the portal's own lists stand, and the portal's environment
(`backstage.extraEnvVars`) is the customer-portal definition's on every portal. With the chat on, the fragment
carries the `aiChat` block — on Anthropic's API with the key from the chart's environment, or on Vertex AI
(`aiChat.provider: vertex`) with the provider, the Google project, region and the mounted credentials file, the
project and region in the Component's values too, which the chart exports — with the model and the chat's two MCP
servers (the portal's own actions server at `/api/mcp-actions/v1` with the signed-in person's Backstage token, the
installation's muster with the sign-in provider), the actions server's tool naming (`mcpActions`) and the actions
the service lists for it (`backend.actions`), and on a rendered portal the shared list with the chat
(`#extensionsAgentPlatformAiChat`, or its `GrafanaDashboards` pair). Where kagent runs and the organisation's
portal follows a chart line before backstage 1.1.0 (`installation.portals[*].chartLine`, the ref its kustomization
patches onto the fleet base's OCIRepository), the fragment names the agents' Flux identity
(`agentPlatform.fluxServiceAccountName: kagent-flux`): that plugin composes the agent's HelmRelease itself, and
the management clusters' Flux multi-tenancy policy refuses one without `spec.serviceAccountName`; from 1.1.0 agents
are created through agent-manager and the key is not read, so it is not written. The definition targets dex-app
3.2.0 or later, where every MCP server's Dex client reads a `clientSecretRef`.

The installation's own MCP servers (`servers.go`: mcp-kubernetes, mcp-prometheus, mcp-capi) each get an
`extras/mcp-<name>/` directory: a kustomization over the fleet base, the OAuth credentials Secret under the
chart's key contract (the Dex client secret shared with the Dex-side client Secret, the encryption key, the
Valkey password where the chart takes it from this Secret), the valkey-auth Secret the fleet base's Valkey
reads, and the server's *credentials revision*: one generated value held by both credentials Secrets and by
the Secret `mcp-<name>-credentials-revision` in the Flux namespace, which the kustomization patches the
server's HelmRelease and its Valkey's to read (`valuesFrom` with `targetPath`) into the charts' checksum
values (`existingSecretChecksum`, the keyed charts' `storage.valkey.existingSecretChecksum`, the Valkey
chart's `auth.usersExistingSecretChecksum`). A rotation rewrites the credentials files, draws the revision
anew and so rolls the server and its Valkey; a reconcile without one keeps it. A private installation adds
the server's user values (the private-address flags) as a ConfigMap the HelmRelease reads. muster's credentials get
the same revision: held by `muster-oauth-credentials`, `muster-valkey-credentials` and the Secret
`muster-credentials-revision` in the Flux namespace, and handed to the HelmRelease of every workload that reads
them (`musterConsumers` in `render.go`) through the platform patch's `components.<name>.valuesFromRefs` (the meta
chart renders them as the children's `valuesFrom`): muster's onto the muster chart's `existingSecretChecksum`
values, its Valkey's onto the Valkey chart's users Secret mark, and, where each runs, klaus-gateway's (its routing
store is muster's Valkey) and the agent-manager's, cluster-manager's and model-manager's (their OAuth resource
servers read the platform client's secret) onto the pod annotation `muster-credentials-revision`. Each of these
reads its value once at start, so a rotation restarts all of them; the runtime feature probes each one's
Deployment Available (`live-platform-workloads`). The result names each revision for the values of the Secrets
it is held by (`Result.Revisions`, recorded with `Result.Revision` where the Secrets are rendered): a rotation
asked for by name draws the value's revision with it, so its readers roll.

A hub's `federation.targets` render the hub side (`hub.go`): the token-exchange broker's targets and the
agentgateway's identity providers in the configmap patch, the targets' MCP servers with exchange auth,
and a credentials Secret per target carrying the hub's client id in the target's Dex — the fleet's
`muster-token-exchange-<target>` for the registry's hub, `muster-token-exchange-<target>-<hub>` for every
other hub — and its generated client secret, named after the client (`<client>-client-secret`): the same
id and name the target's own render gives its Dex-side client and Secret (`installation.federation.hubs`
with `registryHub`), so the two filesets agree on the client and the value they share. A private target adds the tunnel:
the tunnelport release and a RemoteApp per tunnelled app (the target's Dex, each federated group's MCP
server, the API server) under the hub's `extras/agent-platform/tunnelport/`, muster's `extraCaFile`
trust in the SPIFFE bundle, and in `giantswarm/teleport-fleet` the hub's entries of the tunnelport
values (`kubernetes/envs/prod/values.yaml`, read by the fleet's `templates/tunnelport.yaml`, which
renders every tunnel's Teleport objects): the hub as a consumer (`installNamespace: agent-platform`,
`issuer: https://irsa.<hub base domain>` — the oidc join by the hub's published service-account
issuer, which capa publishes; a hub whose provider publishes none the definition knows refuses a
private target), the hub's trust-bundle token (`tunnelport-trust-bundle-token-<hub>`, the one its
tunnelport release names) and one tunnel per tunnelled app (`name: <app>-<target>`, `appLabels`
pinning the app on `app`, `cluster` and the fleet's Teleport tenancy label `customer: giantswarm`,
one token for this hub — `<app>-<target>-bot-token` for the target's first hub, `<app>-<target>-bot-token-<hub>`
for every further one, the rule the connector names follow, since every hub that brokers into the
target shares the tunnel and each needs a token of its own; the hub's RemoteApp names the same token;
the SVIDs' DNS SANs are the template's, templated off the join attributes).
The values file has several
owners: the definition renders its entries as a values document of their own (the golden) and the
plan edits them into the file on record — a consumer or a trust-bundle token replaces the one of its
name or is appended; a tunnel of the same name is merged, the hub's labels and the hub's token in,
every other hub's token kept in place (`tunnelport.tunnels[<tunnel>].tokens`); every other consumer,
token and tunnel stays and is named `kept` — and never creates the file:
without the template on record its values mean nothing, so a values file without `tunnelport` is
`unknown`, which refuses the commit. The trust-bundle singleton (bot, role, workload identity) is
the template's. The Teleport rendering (`teleport.go` and the teleport-fleet subtree of the goldens)
is co-owned by Shield in `CODEOWNERS`.

The golden filesets under `render/agentplatform/testdata/<shape>/golden/` are the reference output for
a public customer, a Giant Swarm-owned installation, a hub with a private target (tunnel and tunnelport
values) and a multi-cluster customer with one aggregator (invented names, placeholder values) and are
diffed on every pull request; `go test ./render/... -update` rewrites them after an intended change.

## Probes and customer actions

A `Result` also says what the running installation has to show for the render to count as working, as data.
`Probes` are the checks of the installation, one or more per `kind: live` dimension of
`definitions/agent-platform/features.yaml`, in the order they are run: the HelmReleases Ready, kagent's
workloads and its Postgres cluster, the oauth2-proxy Secret and the Flux ServiceAccount, the default
ModelConfig's `Accepted` condition, `/api/agents` answered 403, `/oauth2/start` redirected to Dex as client
`kagent`, Dex's `/auth` answering 302 for every rendered client with a redirect URI, Dex holding the current
secret of every client the dex patch gives a secret reference, muster's protected-resource metadata, the installation's own MCPServer objects, no audience mismatch in oauth2-proxy's
log, and the drift of live values against the rendered files. Each probe carries its kind, its target (an
object by namespace, resource and name, or a URL), its expectation and the feature it marks; everything
kagent's is probed only when kagent is enabled. Nothing here connects to a cluster or an endpoint: the verify
slice executes the probes and shows the marks.

A `Drift` probe names the places it compares (`Expect.Compare`: a live path — a YAML document in a string
field as `field:path`, an argument list as `[args]` with a prefix — against a path of the rendered values file);
without any, it compares a HelmRelease's whole user values (its `valuesFrom` ConfigMaps, then `spec.values`)
against the rendered values file.

A `SecretLoaded` probe names a Secret a workload reads only when its containers start, and the workload's
pods and container (`Expect.Pods`, `Expect.Container`): every running container must have started at or after
the Secret's data last changed, the time of the managed-fields entry that owns the data. Dex reads a referenced
client secret into its environment at start, so a rotated secret stays unread until the container restarts
(dex-app 3.2.3 restarts it on the change, earlier versions never do) and the client's sign-ins fail with
`invalid_client` meanwhile; `render.DexSecretLoadedProbe` is that check for one client, and both definitions
render one per client Secret their dex patch references. The check reads when the Secret changed, never its
value: the kubernetes tool the verify reads through masks every Secret value, so a probe that sends the secret
to Dex's token endpoint could not be run as the person. It errs one way only: a change of the Secret's labels by
the manager that owns its data moves the time too and asks for a restart Dex did not need.

`Actions` are what a person outside the platform team still has to do, as a note with a state. The model
key is one, on every installation that runs kagent — the definition renders no file for it and nobody supplies
it at commit: `WaitingForCustomer` until the Secret `kagent-anthropic-key` (key `ANTHROPIC_API_KEY`) exists in
namespace `kagent` or a ModelConfig is added in the portal; the same step is the plan's customer action. The action names
the live dimension it holds up (`live-model-configs`): the ModelConfig probe expects `Accepted=True` either
way, and while the customer's move is pending the verify reads the installation as *waiting for the
customer* rather than *drifted*.

The goldens carry both as `probes.yaml` and `actions.yaml` per shape; `TestProbesAreLiveDimensions` holds every
probe to a live dimension of the feature it names and every live dimension to at least one probe.

## The render-consumption test

The goldens prove what the definition renders; `TestRenderConsumption` (`render/agentplatform/consumption_test.go`)
proves that the charts on the other side read it. Its shapes are its own table: three agent-platform golden
shapes (the chat gateway's among them), each with the customer-portal definition's input for the same installation
(`render/agentplatform/testdata/consumption/<shape>.portal.yaml`, the supplied values as dry-run markers), and a
portal-only installation without the platform. For every shape it writes both definitions' filesets into one
tree (a path both render fails the test: one file, one owner), builds each emitted `extras/<x>/` directory over
the public fleet base with `kustomize`, and renders every HelmRelease that yields with `helm template`: the
portal's tree first — `extras/backstage/` as the customer-portal definition renders it, its directory over the
fleet base with the HelmRelease's values sources patched in and, with the platform, the agent-platform
definition's Component listed next to it — then the servers' charts and their Valkeys, the agent-platform meta
chart with the emitted configmap patch and, in turn, every child HelmRelease the meta chart renders that names
an emitted Secret (muster, kagent, valkey, agent-manager, klaus-gateway, cluster-manager, the connectivity
chart), plus dex-app with the emitted dex patch: the agent-platform definition's in the platform shapes, the
portal's own without the platform. A ConfigMap a HelmRelease takes values from that the same build emitted (the
portal's app-config and values, the platform's `agent-platform-values-backstage`) is rendered with its data, and
the HelmRelease's own Secret sources (the portal's `user-secrets-backstage`, `plugin-keys-backstage`,
`github-app-credentials-backstage`) count as read by the release. The test then asserts the portal Deployment
mounts the platform's app-config fragment, passes it as `--config`, and that the fragment lists the installation
under `agentPlatform.kagent.installations` — and, without the platform, that it mounts none.
Values a shared template supplies on an installation — the Konfiguration ConfigMap a HelmRelease takes its
values from — come from a stand-in per chart in `render/agentplatform/testdata/consumption/<chart>.values.yaml`,
carrying only the Secret-selecting values and what the chart refuses to render without.

Over everything rendered it collects each Secret reference — `secretKeyRef`, `envFrom`, secret and projected
volumes with their mount paths, a HelmRelease's `valuesFrom`, a kagent ModelConfig's API key — and asserts:
every emitted Secret is read by a rendered consumer in its namespace; every key read from an emitted Secret
exists, `optional: true` included (a missing optional key is a silently disabled feature); a Secret loaded whole
with `envFrom` carries every key the chart's own Secret would (the chart is rendered once more with dummy inline
credentials from `<chart>.inline-secret.values.yaml` to read that contract); a Secret mounted whole carries every
`<mountPath>/<key>` the release's manifests mention; and every Dex static client the patch references gets its
secret loaded by the dex Deployment from an emitted `dex-client-*` Secret. It holds `musterConsumers` to the
charts: every workload whose pods read `muster-oauth-credentials` or `muster-valkey-credentials` (a chart's test
hook aside) is the Deployment of a consumer that runs, every consumer that runs has its Deployment rendered
reading them, and on the 4 line the consumer's child is rendered once more with the revision Secret's
`targetPath`s set where Flux merges them, which must change the Deployment's pod template. Every failure names
the chart, the Secret, the key or the client, so a Renovate bump that breaks a contract reads as a diagnosis.

The charts are pinned in `render/agentplatform/testdata/consumption/charts.yaml` (chart, OCI repository,
version); the org's Renovate preset bumps each `version:` through its `registry:` line, and
`renovate-custom.json5` keeps each pin on its meta chart line — the 3 line's meta chart, connectivity chart and
agent-manager pins on 3.x and 0.x, the 4 line's on 4.x — by a regex over the current version, since the pins are
docker tags to Renovate and docker versioning knows no ranges; the meta chart and its connectivity chart are
bumped as one group per line, because the meta chart pins the connectivity chart at its own version. The test
asserts each pin lies in the range the HelmRelease follows on the shape's line and fails naming the line
otherwise; klaus-gateway and cluster-manager, offered to Giant Swarm-owned installations only, are pinned for
the 4 line. An emitted Secret a consumer does not read because of a tracked defect in the
consumer is listed in `known-gaps.yaml` with its issue: the test reports it instead of failing, and fails once
the Secret is read so the entry leaves with the fix.

The test needs `helm`, `kustomize` and the network (gsoci charts, the fleet base on GitHub) and runs only with
`RENDER_CONSUMPTION=1`; `make test-render-consumption` runs it, and the `render-consumption` CircleCI job runs
it on every push next to the unit tests.

The customer-portal definition has a render-consumption test of its own (`render/customerportal/consumption_test.go`,
the same name, gate and Make target): it renders the backstage chart at the pin the agent-platform's `charts.yaml`
carries and holds the `live-pods-running` probe's selector to the labels of the pods the chart renders — the
portal's Deployment matches, no other workload does — so a chart that relabels its pods fails the test rather
than reading *no pod matches* on every installation.
