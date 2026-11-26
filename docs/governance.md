# Governance

How the manager writes an installation's governance for the agent platform (Governing tool calls,
[giantswarm/giantswarm#38902](https://github.com/giantswarm/giantswarm/issues/38902)) and why one person never
changes it alone.

## What muster enforces

muster asks a person before an agent runs a tool the platform governs. The agent-platform meta chart renders its
configuration:

- the mode: `trusted` (default) runs what the connectors' own settings allow; `governed` asks before any tool of
  a governed server that is not on its safe list (new and renamed tools included) and before any of muster's own
  tools except its reads;
- the role floors: tools that ask agents in both modes on a governed server of that role (`governance.roleFloors`,
  a union with muster's built-in floors);
- the governed servers, each with a role and a safe list.

## Giant Swarm's own parts

muster and the chart are open source and carry nothing of Giant Swarm. The definition renders these parts on every
installation as fixed platform parts, not inputs (`definitions/agent-platform/governance.yaml`, `platform`):

| Part | Renders | Effect |
|---|---|---|
| Platform Manager role floors | `governance.roleFloors: {platformManager: [approve_action, merge_action]}` | a person is asked before an agent approves or merges an Action, in both modes |
| Platform Manager as governed server | `governedServers.giantswarm-platform-manager/giantswarm-platform-manager: {role: platformManager, safe: [...]}` | on `governed` every tool but the safe list asks agents |
| Connector admins | `muster.rbac.mcpServerEditor.subjects`: groups `giantswarm-ad:giantswarm-admins`, `giantswarm-github:giantswarm:giantswarm-admins` | whoever holds muster's `mcpserver-editor` Role edits every connector and its tool approval; the meta chart appends `tenants.platformTeam` |

The floors and the governed server are written where the meta chart takes governance (`GovernanceChart`, 4 line,
no chart hold below it); the connector admins on every 4-line installation, with the same groups as workflow
editors (`muster.rbac.workflowEditor.subjects`). A `governance.governedServers` input naming this manager's server
is refused; an installation's own server key may take the role `platformManager`, which only adds floors. Removing
`roleFloors` or the manager's entry out of band is a loosening change; recording them the first time is a
tightening one that asks the review once.

### Release order

A chart and muster that take governance on an installation without these parts let agents approve and merge
unasked. `GovernanceChart` and `GovernanceMuster` are set in the manager release that precedes the chart release
whose `agent-platform.muster.governanceFloor` is set, and every 4-line installation is reconciled before the
chart's version range admits it. `TestGovernanceGatesFollowTheChart` fails while the gates lag the sibling
agent-platform checkout. The drift tests skip without sibling checkouts; CI sets `AGENT_PLATFORM_CHECKOUT` and
`MUSTER_CHECKOUT`.

### Upgrading an installation by hand

An installation the manager does not reconcile carries the same parts in its agent-platform values when it moves
to the chart with tool approval (the chart's defaults are placeholders):

```yaml
muster:
  rbac:
    mcpServerEditor:
      subjects:
        - {apiGroup: rbac.authorization.k8s.io, kind: Group, name: giantswarm-ad:giantswarm-admins}
        - {apiGroup: rbac.authorization.k8s.io, kind: Group, name: giantswarm-github:giantswarm:giantswarm-admins}
    workflowEditor:
      subjects:
        - {apiGroup: rbac.authorization.k8s.io, kind: Group, name: giantswarm-ad:giantswarm-admins}
        - {apiGroup: rbac.authorization.k8s.io, kind: Group, name: giantswarm-github:giantswarm:giantswarm-admins}
governance:
  roleFloors:
    platformManager: [approve_action, merge_action]
governedServers:
  giantswarm-platform-manager/giantswarm-platform-manager:
    role: platformManager
    safe: [get_info, list_installations, list_actions, get_action, verify_capability]
```

Write `governance.mode: governed` where an installation wrote `governance: governed`. Keep the editor lists
non-empty: muster's chart fails the upgrade on an empty list while its old `system:authenticated` binding exists.

## Inputs

Both are read back from the configmap patch on record
(`installations/<name>/apps/agent-platform/configmap-values.yaml.patch`), so a reconcile keeps them.

| Input | Renders | Default |
|---|---|---|
| `governance.mode` | the meta chart's `governance.mode`, written only as `governed` | `trusted`, not written |
| `governance.governedServers` | top-level `governedServers`: overrides by source, each `{role, safe}` or `null` | none: the chart's servers apply |

A source is one the chart knows (`agent-manager`, `kagent`, `model-manager`, `cluster-manager`, `vm-manager`,
`toolchain-manager`, `workspace-manager`, `skill-manager`, `mcp-kubernetes`) or a muster server key (`<name>` or
`<namespace>/<name>`). Helm merges the maps: an omitted source, role or safe list keeps the chart's; a typed safe
list replaces it; `null` drops the source.

```sh
--input governance.mode=governed
--input 'governance.governedServers={"skill-manager": {"safe": ["list_skills", "get_skill"]}}'
```

Refused, typed or read back:

- any governance choice while `GovernanceChart` (`render/agentplatform/governance.go`) is unset, an installation
  off the 4 chart line, or a `versions.chart` hold below it;
- `governed` under a `versions.components.muster` hold below `GovernanceMuster`;
- what muster refuses to start on or what would drop floors: an unknown role; a role other than the chart's for
  one of its sources; a server key without a role or naming a chart source under another key; a floor of any role
  on a safe list; any override of `giantswarm-platform-manager`; a safe entry that is no tool name
  (`^[A-Za-z0-9_.-]{1,128}$`) or repeated, or more than 64 per list; an entry with neither role nor safe list.

muster's built-in roles and floors and the chart's `governedServers` are copied into
`definitions/agent-platform/governance.yaml`; `TestGovernanceMatchesMusterAndTheChart` compares them with sibling
checkouts (`MUSTER_CHECKOUT`, `AGENT_PLATFORM_CHECKOUT`).

## What counts as a governance change

The plan compares the effective governance on both sides: the mode (absent is `trusted`), role floors, every
server as muster governs it, `muster.muster.governance`, a `components.muster` hold below `GovernanceMuster`, and
who may loosen approvals (the platform team and the subjects of muster's editor bindings). A change that leaves
the effective governance as it is, such as an explicit `trusted` dropped, is none.

### The reviewed-governance record

The plan compares the configs repository with what the team reviewed: a `GovernanceRecord` per installation in
the manager's namespace (`governancerecords.platform-manager.giantswarm.io`). Only the manager's ServiceAccount
writes it; a ValidatingAdmissionPolicy (`actions.governanceRecordPolicy`, default on, Kubernetes 1.30+) refuses
every other identity, cluster admins and GitOps controllers included. `merge_action` writes it when it merges a
stage of an approved governance change (`governance_record_written`).

- **Compare-and-swap.** Each Action records the record it was planned against (`spec.governanceBase`).
  `merge_action` merges only while the record is still that one (`governance_record_moved_since_plan`
  otherwise): deny the action and commit again for a new review.
- **Unreviewed difference.** Every plan, dry run included, compares the configs repository with the record. A
  difference the record does not explain (edit outside the manager, revert by hand, older action merged late)
  shows the change against the record led by a loosening line, logs `governance_on_record_changed_outside`, and
  makes the next commit ask the review, even when it types the governance back.
- **No record fails closed.** Where governance is in play, every commit asks the review ("no reviewed
  governance is on record"), showing the governance whole against the chart's defaults, until a reviewed commit
  merges (`governance_unrecorded`). A record that cannot be read counts as none (`governance_record_unread`); one
  that could not be written is logged (`governance_record_not_written`) and the next commit asks again.
- **Disable.** `disable_capability` that deletes governance is a loosening change; its record is the governance
  before the disable, marked `disabled`, and a later enable is compared with it.
- **Moved between plan and pull requests.** The commit re-reads the default branch; governance merged meanwhile
  becomes a loosening change of the action (`status.governance`, `governance_record_moved`).
- **Revert.** A revert of a reviewed change found by a read of the Action is audited
  (`governance_change_reverted`); the record keeps what was reviewed.

## A second person approves every change

A plan that changes governance is recorded on the Action (`spec.governance`, per installation: `lines` old → new
and `loosens`). The hub's Action CRD must have the field, or the commit is refused before any pull request opens.

- The action always asks the Team review, on Giant Swarm's test installations too.
- `approve_action` refuses the actor, by login and GitHub id.
- The approver must be an active member of `approvals.governanceTeam` (`org/team`) and a GitHub account of type
  `User`; the manager reads the membership as the approver. Team unset or membership unreadable: nothing is
  approved.
- `merge_action` merges nothing before that decision; a stage merged outside the manager decides nothing for the
  stages after it.
- An agent can propose such a change but never approve or merge it.

The review leads with the governance, loosening lines first:

```text
*Governance:* this changes the platform's governance; a second person approves it, on test installations too.
*It loosens governance:* rowan: mode governed → trusted
• rowan: mode governed → trusted
```

Loosening lines are never cut: a commit whose lines exceed Slack's 1000 characters is refused, so split it. The
change lines are cut at 1400 characters with the configs pull request named. A change loosens governance, or may,
when:

- the mode leaves `governed`, a role floor goes, a source is no longer governed, or a role changes;
- a safe entry is added (against the chart's list for a first override); one named like a write anywhere in its
  name, camelCase included (`kubernetes_delete`, `drainNode`), or a floor of any role is marked;
- muster's own governance changes, or a muster hold drops below the release that enforces it;
- who may loosen approvals widens: a subject added to `tenants.platformTeam`,
  `muster.rbac.mcpServerEditor.subjects` or `muster.rbac.workflowEditor.subjects` (a removal tightens);
- the record differs from what the last reviewed action left, or the values cannot be read on either side.

## Audit

One line per installation whose governance the action changes (`audit=true`), with the action, installation,
actor, caller, decision, decider, change and whether it loosens.

| Event | When |
|---|---|
| `governance_change_committed` | the commit, wave or disable opened the pull requests |
| `governance_change_self_approval_refused` | the actor tried to approve their own change |
| `governance_change_approval_refused` | approver not an active team member, no person, or membership unreadable |
| `governance_change_approved` | a second person approved (approver login and id, how membership was read) |
| `governance_change_merged` | `merge_action` merged a stage (pull requests and heads) |
| `governance_change_merged_without_approval` | a pull request merged outside the manager before approval; read at the next read of the action |
| `governance_on_record_changed_outside` | a plan found the record's governance other than what the last reviewed action left |
| `governance_record_moved` | the default branch changed between plan and pull requests |
| `governance_record_written` | the installation's record now holds an approved change |
| `governance_record_not_written`, `governance_record_unread` | the record could not be written or read |
| `governance_unrecorded` | governance in play on an installation without a record |
| `governance_change_reverted` | a reviewed change was found reverted on the default branch |

## What the manager does not cover

- Write access to `giantswarm/<customer>-configs` can merge a values change outside the manager. Branch
  protection there (enforced for admins, re-approval after a push, required CODEOWNERS review) is asked of the
  owners; the manager detects the change at the next plan and asks the review then.
- Kubernetes write rights on muster's HelmRelease or ConfigMap change the configuration directly.
- A second GitHub account a person holds and the team lists counts as a second person: team membership is the
  control.

## This manager's safe list

Tools agents call unasked on `governed`: `get_info`, `list_installations`, `list_actions`, `get_action` and
`verify_capability` (read-only; the three list/get tools also refresh Action records from GitHub as the caller and
decide nothing).

Everything else asks, because muster matches tool names: `enable_capability`, `reconcile_capability` and
`disable_capability` even for a dry run (the same tool commits with `mode: commit`), `verify_installation`
(records its result), `watch_action` (moves stages, posts to review threads), `deny_action` (closes pull
requests). `approve_action` and `merge_action` are floors, never safe.
