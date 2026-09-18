# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).



## [Unreleased]

### Added

- The MCP server behind muster in bearer-only mode: the person's GitHub user token through the App `giantswarm-platform-manager` as the bearer of every request, verified with `GET /user` and cached; no bearer or a refused one is a bare 401 with the protected-resource challenge.
- `get_info`: version, caller, pinned authorization server, capability definitions with input schemas, write modes, write tools, approval channel configuration, planned tools.
- The write framework: `dryRun` and `mode` on every write tool, `mode: apply` refused for every write tool before it runs, `mode: commit` as the caller.
- The Helm chart with the oauth-pinned `MCPServer`, the Dockerfile, and the muster scenario harness with a mock authorization server and a fake GitHub `/user` in CI.



[Unreleased]: https://github.com/giantswarm/giantswarm-platform-manager/tree/main
