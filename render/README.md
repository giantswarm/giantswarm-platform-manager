# render — the capability render library

`render` and its definitions (`render/agentplatform`, `render/customerportal`) turn an installation's inputs into the files of
its GitOps repositories: a `Result` with the files by repository and repository-relative path, and the
entries a shared `kustomization.yaml` must list.

## The boundary

The library has **no I/O**. It reads nothing from disk or the network, runs no `git`, no `sops`, no
`kubectl`, and generates no secret. Its only inputs are the decoded input document and the values a
person supplies; its only output is the `Result`. A Secret manifest is rendered in plaintext with a
placeholder (`GENERATED(<name>)`) per value the platform needs and nobody types; `File.Generated`
describes each placeholder (name, kind, length) for the commit step — gitops-commit's `sopsenc` —
to fill in and encrypt for the repository's recipients. Two files carrying the same name receive the
same value: that is how a Dex client and the workload presenting its secret agree.

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
generated, the Slack credentials supplied as `klausGateway.slack.<key>` — the only values a person supplies at
commit), the manager's installation and, on
the 3 chart line, the OAuth section a manager needs without the template's global identity block. The
developer portal's section (`portal.go`) is a directory of the platform's own, `extras/backstage/agent-platform/`
in the portal host's tree, a kustomize Component the portal's `extras/backstage/kustomization.yaml` lists
(`Include.Component`): the platform's Backstage configuration as an extra app-config file the chart mounts from a
ConfigMap, the chart values that mount it and name the installation's avatars host, the Google credentials of a
Vertex chat (supplied as `portal.aiChat.google.credentialsJson`), and a patch appending those sources to the
portal HelmRelease's `valuesFrom` — last, so the platform's values win; Helm replaces lists, so a portal that
sets `backstage.extraEnvVars` itself loses that list to the platform's. Where kagent runs and the organisation's
portal follows a chart line before backstage 1.1.0 (`installation.portals[*].chartLine`, the ref its kustomization
patches onto the fleet base's OCIRepository), the fragment names the agents' Flux identity
(`agentPlatform.fluxServiceAccountName: kagent-flux`): that plugin composes the agent's HelmRelease itself, and
the management clusters' Flux multi-tenancy policy refuses one without `spec.serviceAccountName`; from 1.1.0 agents
are created through agent-manager and the key is not read, so it is not written. A private skills repository's token is
the one optional supplied value (`portal.skillsToken`): given, it renders `kagent-skills-token` in namespace
`kagent`. The definition targets dex-app 3.2.0 or later, where every MCP server's Dex client reads a
`clientSecretRef`.

A hub's `federation.targets` render the hub side (`hub.go`): the token-exchange broker's targets and the
agentgateway's identity providers in the configmap patch, the targets' MCP servers with exchange auth,
and a credentials Secret per target whose generated client secret is named after the pair
(`<hub>-token-exchange-<target>-client-secret`) — the same name the target's own render gives its
Dex-side copy, so the two filesets agree on the value they share. A private target adds the tunnel:
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
one token for this hub; the SVIDs' DNS SANs are the template's, templated off the join attributes).
The values file has several
owners: the definition renders its entries as a values document of their own (the golden) and the
plan edits them into the file on record — an entry replaces the one of its name or is appended,
every other consumer, token and tunnel stays and is named `kept` — and never creates the file:
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
`kagent`, Dex's `/auth` answering 302 for every rendered client with a redirect URI, muster's
protected-resource metadata, the installation's own MCPServer objects, no audience mismatch in oauth2-proxy's
log, and the drift of live values against the rendered files. Each probe carries its kind, its target (an
object by namespace, resource and name, or a URL), its expectation and the feature it marks; everything
kagent's is probed only when kagent is enabled. Nothing here connects to a cluster or an endpoint: the verify
slice executes the probes and shows the marks.

A `Drift` probe names the places it compares (`Expect.Compare`: a live path — a YAML document in a string
field as `field:path`, an argument list as `[args]` with a prefix — against a path of the rendered values file);
without any, it compares a HelmRelease's whole user values (its `valuesFrom` ConfigMaps, then `spec.values`)
against the rendered values file.

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
proves that the charts on the other side read it. Its shapes are its own table: the two agent-platform golden
shapes, each with the customer-portal definition's input for the same installation
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
secret loaded by the dex Deployment from an emitted `dex-client-*` Secret. Every failure names the chart, the
Secret, the key or the client, so a Renovate bump that breaks a contract reads as a diagnosis.

The charts are pinned in `render/agentplatform/testdata/consumption/charts.yaml` (chart, OCI repository,
version); the org's Renovate preset bumps each `version:` through its `registry:` line, and
`renovate-custom.json5` keeps the meta chart and its connectivity chart below 4.0.0 — the 4.x line needs inputs
the definition does not carry, and the fleet base has the same ceiling. The test asserts each pin lies in the
range the OCIRepository follows. A shape whose input lifts `chart.semver` to a line the pin file does not carry
(the Giant Swarm-owned shape asks for 4.x) renders that line's values and is skipped naming the reason: it is
not proven here; klaus-gateway and cluster-manager, offered to Giant Swarm-owned installations only, are pinned
for the day such a shape stays on the pinned line. An emitted Secret a consumer does not read because of a tracked defect in the
consumer is listed in `known-gaps.yaml` with its issue: the test reports it instead of failing, and fails once
the Secret is read so the entry leaves with the fix.

The test needs `helm`, `kustomize` and the network (gsoci charts, the fleet base on GitHub) and runs only with
`RENDER_CONSUMPTION=1`; `make test-render-consumption` runs it, and the `render-consumption` CircleCI job runs
it on every push next to the unit tests.
