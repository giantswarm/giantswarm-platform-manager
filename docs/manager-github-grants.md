# Manager GitHub grants

Each manager can fetch the caller's GitHub App user grant from muster's RFC 8693 broker when a commit or private skill needs it. Ordinary reads require no GitHub grant. Each manager has its own provider App name, grant target and confidential broker client; its client may exchange for that target only.

| Input | Manager minimum | GitHub App issuer | OAuth client Secret |
| --- | --- | --- | --- |
| `agentManager.github.tokenExchange.enabled` | `3.0.0-rc.10` | `https://github.com/apps/giantswarm-agent-manager` | `giantswarm-agent-manager-oauth-client` |
| `clusterManager.github.tokenExchange.enabled` | `0.37.0-rc.1` | `https://github.com/apps/giantswarm-cluster-manager` | `giantswarm-cluster-manager-oauth-client` |
| `modelManager.github.tokenExchange.enabled` | `1.20.0-rc.10` | `https://github.com/apps/giantswarm-model-manager` | `giantswarm-model-manager-oauth-client` |
| `skillManager.github.tokenExchange.enabled` | `0.2.0-rc.1` | `https://github.com/apps/giantswarm-agent-skills` | `giantswarm-skill-manager-oauth-client` |

The cluster-, model- and skill-manager inputs require an explicit `versions.components.<manager>` hold at or above the listed minimum. This selects an exchange-capable release over the meta chart's defaults. muster must be at least `5.63.2`; the meta chart must include the grant-broker connectivity changes in giantswarm/agent-platform#1375.

GPM generates `<manager>-github-broker` in `agent-platform`, with `client-id` and `client-secret`. Both the manager and muster reference it. The OAuth client Secrets in the table belong to the existing Apps and must already be bound; GPM does not generate App credentials.

Skill-manager reuses the read-only `giantswarm-agent-skills` App. Its installation credential remains `skillManager.github.app.secretName`, for example `giantswarm-agent-skills-app`, alongside its separate OAuth client reference. Listings withhold private sources without requesting consent; reading or pinning a private skill can request the person's grant.

The rendered `github.enabled` accompanies `github.tokenExchange`. Readback suppresses the legacy GitHub choice when the exchange object is present, so turning exchange off does not restore a pin. Former top-level `githubTokenExchange` values are read as migration inputs and removed from the rendered values.

Start with `platformctl installation reconcile <installation> agent-platform --dry-run` and the manager's exchange input and version hold. Review the entire diff and preserve an explicit range below agent-manager 3.x until its old values are migrated together with the version selection. Production rollout under giantswarm/giantswarm#39239 requires the real-App lab proof and confirmation of each installation dry run.

Manager-to-broker grant release is the temporary MCP deviation recorded in giantswarm/giantswarm#39213, with the upstream exit tracked in giantswarm/giantswarm#39102.
