# render — the capability render library

`render` and its definitions (`render/agentplatform`) turn an installation's inputs into the files of
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
owning organisation may enable. What this version does not render (the hub outputs of
`federation.targets`, the portal's files, klaus-gateway and cluster-manager) it refuses with
`ErrNotRendered` naming the input rather than emitting an incomplete fileset.

The golden filesets under `render/agentplatform/testdata/<shape>/golden/` are the reference output for
a public customer and a Giant Swarm-owned installation (invented names, placeholder values) and are
diffed on every pull request; `go test ./render/... -update` rewrites them after an intended change.

## The render-consumption test

The goldens prove what the definition renders; `TestRenderConsumption` (`render/agentplatform/consumption_test.go`)
proves that the charts on the other side read it. For every golden shape it writes the fileset out, builds each
emitted `extras/<x>/` directory over the public fleet base with `kustomize`, and renders every HelmRelease that
yields with `helm template`: the servers' charts and their Valkeys, the agent-platform meta chart with the
emitted configmap patch and, in turn, every child HelmRelease the meta chart renders that names an emitted
Secret (muster, kagent, valkey, agent-manager, the connectivity chart), plus dex-app with the emitted dex patch.
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
not proven here. backstage is not pinned: the definition renders no portal files yet, so no emitted Secret has
the portal as its consumer.

The test needs `helm`, `kustomize` and the network (gsoci charts, the fleet base on GitHub) and runs only with
`RENDER_CONSUMPTION=1`; `make test-render-consumption` runs it, and the `render-consumption` CircleCI job runs
it on every push next to the unit tests.
