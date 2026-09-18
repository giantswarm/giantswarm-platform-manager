# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- The wave: `reconcile_capability` with `mode: commit` over a set (`installations`; empty: every installation of the registry) is one action with one approval — one dry run listing the pull requests per installation and the installations *skipped: not opted in*, one review listing the targets in the rollout order and the skipped, one Action carrying every installation's state (`spec.skipped`, `status.rollout.installations[]`, `installation` on every recorded pull request), the pull requests per installation on `platform/<action>/<installation>`. `merge_action` rolls the wave out one stage per call: it merges the first installation's pull requests once green; the next call verifies the installation rolling out (`verify_capability` as the actor) and, green, merges the next; a red probe stops the wave — the installation *failed*, the stages after it not started, the remaining pull requests open, the result naming where and why. The default order is Giant Swarm's test installations, the hub, then the customers; `order` on the dry run and the commit names another. A wave carries no supplied secret values: a target whose supplied secret files are not on record refuses the wave before any write. A single-installation action is a wave of one: the call after its merge verifies it and moves it to *enabled*.
- `list_installations` answers the per-installation state of a running wave (*rolling out* for its installations, *failed* for the one a red probe stopped it at).
- The approval of an action through klaus-gateway's Team review: `mode: commit` posts one review to the capability-owning team's channel (`approvals.team`, `approvals.channel`; the notice channel `approvals.noticeChannel` told for a customer installation) naming the actor, the pull requests and the change, with the receipt on the Action; `approve_action` (the Approve button, called as the clicking member) submits an approving review on every pull request with that member's token and refuses the actor; `deny_action` records the reason, closes the pull requests as the member and moves the Action to *denied*; `merge_action` merges the approved pull requests as the actor in dependency order once their checks are green, moves the Action to *rolling out* and posts the outcome into the review's thread — before the decision it posts the review when none is up and re-posts one the gateway no longer holds. The requests carry the pod's projected ServiceAccount token (`approvals.audience`); without `approvals.gatewayURL`, `mode: commit` is refused.
- `denied` in the Action CRD's `status.state`; `head`, `headSha` on the recorded pull requests; `customer` and `change` in the spec; `noticeChannel`, `postedAt` on the approval; the actor's email.
- The commit step lists every include in its kustomization: `./agent-platform/` and the MCP servers under `resources:` of the installation's `extras/kustomization.yaml`, the portal's fragment under `components:` of the portal host's `extras/backstage/kustomization.yaml`. The dry run reads each kustomization as the person and shows it among the files (update, unchanged, or unknown when it is absent or unreadable — a kustomization other owners write is never created) and every include with its list and change; the commit writes the edited kustomization with the repository's other files and, as for any file, refuses while one is unknown.
- `enable_capability` and `reconcile_capability` with `mode: commit` for one installation: the opt-in gate (`management-clusters/<name>/platform-manager.yaml` read as the person at call time; absent, `optIn: false` or unreadable refuses naming the installation and the file and records the refusal as an Action in state *refused*), the Action created in *pending approval*, the files rendered with the supplied `secrets` (by field, never in the Action, a log or an answer), the secret files encrypted through gitops-commit's `sopsenc` for the repository's `.sops.yaml` recipients, and the pull requests through gitops-commit as the person in dependency order on `platform/<action>/<installation>` with the action id in every title and body; a failure on the way moves the Action to *failed* with the pull requests opened so far. The approval, merge and rollout follow in a later version.
- `refused` in the Action CRD's `status.state`.

### Changed

- The managed model key of kagent is rendered as `secrets/kagent-anthropic-key-secret.yaml` (the Secret keeps its name `kagent-anthropic-key`): a secret file's name carries `secret` or `credential` so that the commit encrypts it.
- The agent-platform definition renders every output its schema accepts except the hub outputs of `federation.targets`: the developer portal's section as the platform's own kustomize Component `extras/backstage/agent-platform/` in the portal host's tree (the app-config fragment with the agent-platform plugin's section, the installation's muster and the AI chat on Anthropic or Vertex; the chart values mounting it and naming the avatars host; the Vertex credentials Secret; the patch appending those sources to the portal HelmRelease), listed through `render.Include.Component`; the klaus-gateway component with its generated OBO keys and supplied Slack credentials; the cluster-manager component with its network policy; the private skills token as an optional supplied value.
- mcp-prometheus and mcp-capi render their Dex clients as referenced Secrets (`clientSecretRef`, `dex-client-mcp-{prometheus,capi}`); the definition targets dex-app 3.2.0 or later, and the render-consumption test pins it, backstage, klaus-gateway and cluster-manager.

- `enable_capability` and `reconcile_capability` with `dryRun: true`: the render of one installation or a set through the capability's definition from the facts on record and the typed `inputs` — the files with their change against the repository now, the pull requests in dependency order, the generated secrets by name, the Dex clients and redirect URIs, the secrets supplied at commit by field, the customer actions and the probes; a set skips the installations not opted in and lists them; no secret value in a dry run. `mode: commit` answers not implemented.
- The Action record: the `actions.platform-manager.giantswarm.io` CRD (status subresource), the Role and binding in the chart (`actions.enabled`, `actions.installCRD`), `get_action` and `list_actions` over it, and `lastAction` in `list_installations` with the action's state standing over the files'.

- `list_installations`: the installations registry — the catalog in the registry repository and the hub's Dev Portal app-config, read as the caller — with, per installation and capability, the state (*not opted in*, *not enabled*, *enabled* from the repositories; the Action-record states carried in the model), the inputs on record from the installation's `config.yaml.patch` and the last action; the opt-in declaration `management-clusters/<name>/platform-manager.yaml` read at call time, never cached, with the path and the owners' pull request that would add it when absent; `installations` and `customer` narrow the answer.
- The registry configuration: `--registry-repository`, `--registry-path`, `--hub` (chart values `registry.repository`, `registry.path`, `hub`); `get_info` reports them under `registry`.

- The MCP server behind muster in bearer-only mode: the person's GitHub user token through the App `giantswarm-platform-manager` as the bearer of every request, verified with `GET /user` and cached; no bearer or a refused one is a bare 401 with the protected-resource challenge.
- `get_info`: version, caller, pinned authorization server, capability definitions with input schemas, write modes, write tools, approval channel configuration, planned tools.
- The write framework: `dryRun` and `mode` on every write tool, `mode: apply` refused for every write tool before it runs, `mode: commit` as the caller.
- The Helm chart with the oauth-pinned `MCPServer`, the Dockerfile, and the muster scenario harness with a mock authorization server and a fake GitHub `/user` in CI.

### Security

- Bumped `google.golang.org/grpc` to v1.83.1 (GHSA-2v4p-qf9q-27wj, GHSA-vp52-pcj8-j9qc, GHSA-qc2q-p7wx-3px3) and the `go.opentelemetry.io/otel` modules to v1.45.0 (GHSA-8wmf-6v46-5gfg); both are indirect dependencies reached through sops.



[Unreleased]: https://github.com/giantswarm/giantswarm-platform-manager/tree/main
