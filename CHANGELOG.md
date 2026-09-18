# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- `enable_capability` and `reconcile_capability` with `dryRun: true`: the render of one installation or a set through the capability's definition from the facts on record and the typed `inputs` — the files with their change against the repository now, the pull requests in dependency order, the generated secrets by name, the Dex clients and redirect URIs, the secrets supplied at commit by field, the customer actions and the probes; a set skips the installations not opted in and lists them; no secret value in a dry run. `mode: commit` answers not implemented.
- The Action record: the `actions.platform-manager.giantswarm.io` CRD (status subresource), the Role and binding in the chart (`actions.enabled`, `actions.installCRD`), `get_action` and `list_actions` over it, and `lastAction` in `list_installations` with the action's state standing over the files'.

- `list_installations`: the installations registry — the catalog in the registry repository and the hub's Dev Portal app-config, read as the caller — with, per installation and capability, the state (*not opted in*, *not enabled*, *enabled* from the repositories; the Action-record states carried in the model), the inputs on record from the installation's `config.yaml.patch` and the last action; the opt-in declaration `management-clusters/<name>/platform-manager.yaml` read at call time, never cached, with the path and the owners' pull request that would add it when absent; `installations` and `customer` narrow the answer.
- The registry configuration: `--registry-repository`, `--registry-path`, `--hub` (chart values `registry.repository`, `registry.path`, `hub`); `get_info` reports them under `registry`.

- The MCP server behind muster in bearer-only mode: the person's GitHub user token through the App `giantswarm-platform-manager` as the bearer of every request, verified with `GET /user` and cached; no bearer or a refused one is a bare 401 with the protected-resource challenge.
- `get_info`: version, caller, pinned authorization server, capability definitions with input schemas, write modes, write tools, approval channel configuration, planned tools.
- The write framework: `dryRun` and `mode` on every write tool, `mode: apply` refused for every write tool before it runs, `mode: commit` as the caller.
- The Helm chart with the oauth-pinned `MCPServer`, the Dockerfile, and the muster scenario harness with a mock authorization server and a fake GitHub `/user` in CI.



[Unreleased]: https://github.com/giantswarm/giantswarm-platform-manager/tree/main
