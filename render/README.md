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
