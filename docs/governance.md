# Governance

How the manager writes an installation's governance for the agent platform (Governing tool calls,
[giantswarm/giantswarm#38902](https://github.com/giantswarm/giantswarm/issues/38902)) and why one person never
changes it alone.

## What muster enforces

muster asks a person before an agent runs a tool the platform governs. Its governance configuration has two
parts, both rendered by the agent-platform meta chart:

- the mode: `trusted` (the default) runs what the connectors' own settings allow; `governed` asks a person
  before an agent runs any tool of a governed server that is not on that server's safe list, new and renamed
  tools included, and before any of muster's own tools except its reads;
- the governed servers, each with a role and a safe list. A role's floors ask agents in both modes, for
  example this manager's `approve_action` and `merge_action` (role `platformManager`).

## The inputs

The agent-platform definition takes both as inputs. Both are read back from the configmap patch on record
(`installations/<name>/apps/agent-platform/configmap-values.yaml.patch`), so a reconcile keeps them.

| Input | Renders | Default |
|---|---|---|
| `governance.mode` | the meta chart's top-level `governance`, written only as `governed` | `trusted`, the chart's default, not written |
| `governance.governedServers` | the meta chart's top-level `governedServers`: overrides by source, each `{role, safe}` or `null` | none: the chart's servers and safe lists apply |

A source is one the meta chart knows (`agent-manager`, `kagent`, `model-manager`, `cluster-manager`,
`vm-manager`, `toolchain-manager`, `workspace-manager`, `skill-manager`, `mcp-kubernetes`,
`giantswarm-platform-manager`) or a muster server key (`<name>` or `<namespace>/<name>`). Helm merges the maps:

- a source you leave out keeps the chart's entry;
- a role or safe list you leave out keeps the chart's;
- a safe list you type replaces the chart's list;
- `null` drops the source, and muster governs nothing for it.

```sh
--input governance.mode=governed
--input 'governance.governedServers={"giantswarm-platform-manager": {"safe": ["get_info", "list_installations"]}}'
```

The definition refuses, typed or read back:

- any governance choice while no meta chart release renders muster's governance configuration. `GovernanceChart`
  in `render/agentplatform/governance.go` stays unset until that release is cut: a guessed version would write
  keys a chart ignores, so an installation would read as governed while its muster is not (decision #89 of the
  epic). Set it, and `GovernanceMuster`, when the releases exist.
- an installation off the 4 chart line;
- a `versions.chart` hold below `GovernanceChart`;
- `governed` under a `versions.components.muster` hold below `GovernanceMuster`, where the meta chart fails the
  render;
- whatever muster refuses to start on:
  - a role muster does not know (the schema);
  - a floor of the source's role on its safe list (the typed role, else the chart's role for the source);
  - an empty or repeated safe entry;
  - an entry with neither role nor safe list.

## A second person approves every change

A commit whose plan changes either key is recorded with the change on the Action (`spec.governance`, per
installation: `lines` old → new and `loosens`). The change rules are:

- such an action always asks for the Team review, also where every target is one of Giant Swarm's test
  installations, which otherwise need none;
- `approve_action` refuses the actor, as for every action, so a second person of the team decides;
- `merge_action` merges nothing before that decision;
- an agent can propose such a change but never approve or merge it: muster asks a person before an agent's
  `approve_action` and `merge_action`.

The review leads with the governance, before the reason:

```text
*Governance:* this changes the platform's governance; a second person approves it, on test installations too.
• rowan: mode governed → trusted (the chart's default)
*It loosens governance:* rowan: mode governed → trusted (the chart's default)
```

The same change also reads in *What changes*, with its values, as its own line. A change loosens governance, or
may, when any of these hold:

- the mode leaves `governed`;
- a source is dropped or put back to the chart's entry;
- a role changes or is left to the chart's;
- a safe entry is added (one named like a write, such as `create_…` or `set_…`, is marked);
- a safe list is replaced or put back to the chart's.

The manager does not know the chart's own entries, so a change against them counts as loosening. If the
agent-platform values cannot be read on either side, the plan counts it as a loosening change.

## Audit

Each step logs one line per installation whose governance the action changes. Every line carries the action,
the installation, the actor, the caller, the decision and who decided it, the change and whether it loosens.

| Event | When |
|---|---|
| `governance_change_committed` | the commit opened the pull requests |
| `governance_change_self_approval_refused` | the actor tried to approve their own change |
| `governance_change_approved` | a second person approved it |
| `governance_change_merged` | `merge_action` merged pull requests of it |
| `governance_change_merged_without_approval` | the pull requests were merged outside the manager before any decision; only the repositories' branch protection stood in the way |

## What the manager does not cover

These limits are outside the manager:

- someone with write access to `giantswarm/<customer>-configs` can merge a change to the values outside the
  manager. Branch protection on those repositories (enforced for admins, re-approval after a new push, required
  CODEOWNERS review) is asked of their owners. The manager audits such a merge but cannot prevent it.
- whoever holds Kubernetes write rights on muster's HelmRelease or ConfigMap can change the configuration
  directly.
- `disable_capability` removes the whole platform, muster included, and keeps its own review rules: no agent
  path is left to govern.

## This manager's own safe list

The meta chart governs this manager (`giantswarm-platform-manager`, role `platformManager`) with an empty safe
list, so on `governed` every tool but the floors asks agents. The tools that only read, and are proposed for its
safe list, are:

| Tool | Why it is safe |
|---|---|
| `get_info` | who the caller is, the definitions and their schemas |
| `list_installations` | the installations and their capability states, read as the caller |
| `list_actions` | the Action records |
| `get_action` | one Action record |
| `verify_capability` | compares the repositories with a definition and writes nothing |

`get_action` and `list_actions` bring an Action's status up to date with GitHub as they read it. They change no
decision.

These tools stay asking:

- `enable_capability`, `reconcile_capability` and `disable_capability` ask agents even for a dry run: muster
  matches tool names, and the same tool commits with `mode: commit`. A separate plan tool would let agents
  plan unasked.
- `verify_installation` records its result on the Action.
- `watch_action` moves stages and posts to the review thread.
- `deny_action` closes pull requests.
- `approve_action` and `merge_action` are floors and are never safe.
