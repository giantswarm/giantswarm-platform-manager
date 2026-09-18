# giantswarm-platform-manager

Giant Swarm's **installation manager**: enables, reconciles and verifies platform capabilities on
opted-in installations as the person, through MCP tools behind muster. Every write is a pull request
to the installation's GitOps repository, opened as the person; nothing is applied to a cluster
directly.

`giantswarm-platform-manager` is the working name. The `giantswarm-` prefix marks a manager specific
to the giantswarm org — like `giantswarm-repo-manager`, unlike `agent-manager`, `model-manager` and
`cluster-manager` — and *installation manager* is its alias in the platform's plans. The final name
is the team's; a rename is one repository rename plus the GitHub App's.

It ships as its own app on the hub installation, never as a component of the `agent-platform` meta
chart.

## Identities

The server holds no token of its own, verifies no Dex token and is no muster broker client.

- **The caller is the person.** Behind muster every request carries the person's GitHub user token —
  their one-time authorization of the GitHub App `giantswarm-platform-manager`, which muster stores,
  refreshes and puts on every call. The server verifies the bearer with `GET /user` (once per token,
  then from a bounded cache) and every GitHub call of the request runs with it. A request without a
  bearer, or with one GitHub refuses, is a bare `401` with the RFC 6750 challenge naming the
  protected-resource metadata; there is no other path in.
- **A write's effective rights are the person's own ∩ the App's.** The pull request is the person's;
  GitHub bounds it to what the person may do and to what the App is installed on and permitted.
- **Unattended runs, when they come, act as the platform's App identity**, never as a person.

The chart's `MCPServer` (with `muster.mcpServer.enabled` and `oauth.enabled`) renders `auth.type:
oauth` pinned to the App as the authorization server — issuer `https://github.com/apps/giantswarm-platform-manager`,
GitHub's authorize and token endpoints, the App's client credentials from a Secret, `grantScope:
subject` so every session of a person carries the same grant — the pattern of the `github` and `pro`
servers on the platform.

## Tools

Behind muster the tools appear as `x_giantswarm-platform-manager_<tool>`.

| Tool | Kind | What it does |
|---|---|---|
| `get_info` | read | The version, the caller (login and id), the pinned authorization server, the capability definitions with their input schemas, the write modes, the write tools, the approval channel configuration and the tools still to come. Call first. |
| `list_installations`, `enable_capability`, `reconcile_capability`, `verify_capability`, `get_action`, `list_actions` | planned | The extension points the next slices fill; `get_info` lists them as `plannedTools` until each is registered. |

Every write tool is registered through one framework, which owns two arguments:

- `dryRun: true` returns the rendered change and writes nothing;
- `mode: "commit"` opens the pull request as the person — the only write mode;
- `mode: "apply"` **is refused for every write tool, present and future**, before the tool runs: every
  target of this manager is GitOps-owned, and a change applied without its commit is drift the next
  reconcile reverts. `get_info` reports `capabilities: {commit: true, apply: false, applyRefused: true}`.

## Configuration

Flags, each with an environment variable (`--listen` / `LISTEN`, `--mcp-path` / `MCP_PATH`,
`--github-api-url` / `GITHUB_API_URL`, `--enable-oauth` / `OAUTH_ENABLED`, `--oauth-base-url` /
`OAUTH_BASE_URL`, `--oauth-authorization-server` / `OAUTH_AUTHORIZATION_SERVER`, `--approvals-url` /
`APPROVALS_URL`, `--approvals-channel` / `APPROVALS_CHANNEL`); `giantswarm-platform-manager -h` lists
them. The chart in [`helm/giantswarm-platform-manager`](helm/giantswarm-platform-manager/README.md)
sets them from its values.

## Development

- `make test` — the Go tests, among them the identity chain in `internal/e2e` against a fake GitHub:
  no bearer is a bare 401, a refused bearer is `invalid_token`, `get_info` names the person, the
  bearer is verified once per token, and the framework refuses `mode: apply`.
- `make scenario-test` — the muster half in muster's own scenario harness (`tests/scenarios`, needs
  the `muster` binary on `PATH`): a mock authorization server standing in for GitHub as the App, the
  consent once, the token on every call, a second session adopting the grant, a person without a
  grant told where to sign in. CI runs both in the `scenario-test` job (`.circleci/custom.yml`).
- `make helm-lint`, `make helm-template`, `make helm-schema` — the chart with its default, platform
  (`tests/oauth-values.yaml`) and lab (`tests/lab-oauth-values.yaml`) values.
- `make docker-build` — a local image; CI builds and publishes the multi-arch image and the chart on
  every tag.

Public repository: every fixture uses invented installation names and placeholder values.
