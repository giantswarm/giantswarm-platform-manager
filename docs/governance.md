# Governance

How the manager writes an installation's governance for the agent platform (Governing tool calls,
[giantswarm/giantswarm#38902](https://github.com/giantswarm/giantswarm/issues/38902)) and why one person never
changes it alone.

## What muster enforces

muster asks a person before an agent runs a tool the platform governs. Its governance configuration, rendered by
the agent-platform meta chart, has three parts:

- the mode: `trusted` (the default) runs what the connectors' own settings allow; `governed` asks a person
  before an agent runs any tool of a governed server that is not on that server's safe list, new and renamed
  tools included, and before any of muster's own tools except its reads;
- the role floors: tools that ask agents in both modes on a governed server of that role. muster builds in its
  own roles' floors and takes more from the configuration (`governance.roleFloors`, a union: nothing removes a
  built-in floor);
- the governed servers, each with a role and a safe list.

## Giant Swarm's own parts

muster and the agent-platform chart are open source and carry nothing of Giant Swarm (decision #92 of the epic).
This manager is Giant Swarm's, so the Giant Swarm flow lives here: the agent-platform definition renders these
parts on every installation it manages, as fixed platform parts. They are no inputs, and no input changes or
loosens them (`definitions/agent-platform/governance.yaml`, `platform`):

| Part | Renders | What it does |
|---|---|---|
| The Platform Manager's role floors | `governance.roleFloors: {platformManager: [approve_action, merge_action]}` | muster asks a person before an agent approves or merges an Action of this manager, in both modes; the manager cannot tell an agent from a person, since muster forwards the person's identity |
| The Platform Manager as a governed server | `governedServers.giantswarm-platform-manager/giantswarm-platform-manager: {role: platformManager, safe: [...]}`: muster's key for the MCPServer `giantswarm-platform-manager` in the manager's release namespace, `giantswarm-platform-manager`, outside muster's (`agent-platform`) | on `governed` every tool of this manager but its safe list asks agents; the floors hold in both modes |
| The connector admins | `muster.rbac.mcpServerEditor.subjects`: the groups `giantswarm-ad:giantswarm-admins` and `giantswarm-github:giantswarm:giantswarm-admins` | whoever holds muster's `mcpserver-editor` Role edits every connector in muster's namespace, sets its tool approval and alone lets an agent run a tool the connector asks for; the meta chart appends `tenants.platformTeam` |

The role floors and the governed server are written where the meta chart takes the governance (the release
`GovernanceChart`, the 4 line, no chart hold below it); the connector admins on every installation on the 4 line,
which every chart there takes, with the same groups as the workflow editors (`muster.rbac.workflowEditor.subjects`,
muster's `workflow-editor` Role, who create, change and delete muster's workflows; decision #93). A
`governance.governedServers` input naming the Platform Manager's server, under any key, is refused; read back from
the record the platform's entry is rendered again. A server key of an installation's own may take the role
`platformManager`, which only adds its floors.

Neither muster nor the chart builds these parts in, so the plan reads a record without them as without them:
removing `roleFloors` or the Platform Manager's entry out of band is a loosening governance change, and putting
them on record the first time is a tightening one that asks the review once.

### Release order

The open-source chart and muster no longer govern the Platform Manager by themselves: a chart and muster that take
the governance on an installation whose values lack these parts let agents approve and merge Actions unasked. So
`GovernanceChart` and `GovernanceMuster` are set in the manager's release that precedes the chart release whose
`agent-platform.muster.governanceFloor` is set, and every 4-line installation is reconciled before the chart's
version range admits that release. `TestGovernanceGatesFollowTheChart` fails while the sibling agent-platform
checkout sets the floor and the gates are not set to it. The drift tests skip without sibling checkouts; CI should
set `AGENT_PLATFORM_CHECKOUT` and `MUSTER_CHECKOUT`.

### Upgrading an installation by hand

An installation the manager does not reconcile (one enabled by hand) carries the same parts in its agent-platform
values when it moves to the chart with tool approval; the chart's own defaults are placeholder groups and no
governed server of Giant Swarm's:

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

## The inputs

The agent-platform definition takes the mode and the overrides of the governed servers as inputs. Both are read
back from the configmap patch on record (`installations/<name>/apps/agent-platform/configmap-values.yaml.patch`),
so a reconcile keeps them.

| Input | Renders | Default |
|---|---|---|
| `governance.mode` | the meta chart's `governance.mode`, written only as `governed` | `trusted`, the chart's default, not written |
| `governance.governedServers` | the meta chart's top-level `governedServers`: overrides by source, each `{role, safe}` or `null` | none: the chart's servers and safe lists apply |

A source is one the meta chart knows (`agent-manager`, `kagent`, `model-manager`, `cluster-manager`,
`vm-manager`, `toolchain-manager`, `workspace-manager`, `skill-manager`, `mcp-kubernetes`) or a muster server key
(`<name>` or `<namespace>/<name>`). Helm merges the maps:

- a source you leave out keeps the chart's entry;
- a role or safe list you leave out keeps the chart's;
- a safe list you type replaces the chart's list;
- `null` drops the source, and muster governs nothing for it.

```sh
--input governance.mode=governed
--input 'governance.governedServers={"skill-manager": {"safe": ["list_skills", "get_skill"]}}'
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
- what muster refuses to start on, or what would drop floors past it:
  - a role muster does not know (the schema);
  - a role other than the meta chart's for one of its sources: muster applies a role's floors by the server's
    role, so a server's role is what the server is and an override may not change it;
  - a server key of its own without a role, or one naming a chart source under another key
    (`<namespace>/agent-manager`, `<cluster>-mcp-kubernetes`);
  - a floor of any role on any safe list, the platform's role floors included;
  - any override of `giantswarm-platform-manager`, Giant Swarm's own;
  - a safe entry that is no tool name (`^[A-Za-z0-9_.-]{1,128}$`), a repeated one, more than 64 per list;
  - an entry with neither role nor safe list.

muster's built-in roles and floors and the chart's own `governedServers` are copied into
`definitions/agent-platform/governance.yaml` beside Giant Swarm's platform parts;
`TestGovernanceMatchesMusterAndTheChart` compares them with sibling checkouts of muster and agent-platform where
present (`MUSTER_CHECKOUT`, `AGENT_PLATFORM_CHECKOUT`). A muster or chart from before decision #92 that still
carries the Platform Manager's role or entry must agree with the platform's.

## What counts as a governance change

The plan compares the effective governance on both sides: the mode (absent is `trusted`), the role floors (a
floor that goes loosens), every server as muster governs it (a source the values leave alone is the chart's entry), muster's own `muster.muster.governance`, and,
once `GovernanceMuster` is set, a `components.muster` hold below it, and who may loosen approvals: the platform
team and the subjects of muster's editor bindings (absent editor subjects read as the Giant Swarm groups the
definition renders). A change that leaves the effective
governance as it is, such as an explicit `trusted` dropped, is none.

The plan compares the configs repository with what the team reviewed, kept per installation in a
reviewed-governance record (decision #99): a `GovernanceRecord` in the manager's namespace, named after the
installation, beside the Action records (`crd-governancerecords.yaml`; only the manager's ServiceAccount may write
it). It holds the reviewed governance (the snapshot of the governance values), the Action whose merge wrote it,
the approver and the configs pull request's merged head. `merge_action` writes it, and nothing else does: when it
merges an installation's stage of an Action that changes the governance there and that the team approved, the
governance that Action's commit leaves becomes the record (`governance_record_written`). An action that needs no
review, one merged outside the manager, a revert and the Actions' order or states never change it.

Every plan, the dry run and the commit included, compares the governance in the configs repository with the
record, whether or not the plan writes the values file. A difference the record does not explain (an edit
outside the manager, a revert by hand, an older action merged after a newer one) is an unreviewed change:

- the plan shows the change against the record, led by a loosening line that names the reviewed action;
- `governance_on_record_changed_outside` is logged;
- the next commit asks the team's review, even when it types the governance back; a commit with nothing to write
  answers "nothing to commit" and names the difference. The reviewed commit, once merged, is the record.

A revert of a reviewed governance change that a read of the Action finds (the watch, or `deny_action`'s
withdrawal) is audited (`governance_change_reverted`); the record keeps what was reviewed, so the next plan
shows the revert.

An installation without a record is fail closed: where its governance is in play (the values carry the meta
chart's `governance`), every commit asks the team's review, led by "no reviewed governance is on record", until a
reviewed commit is merged and becomes the record (`governance_unrecorded`). That is the migration of the
installations from before the record: their first commit is reviewed. A record that cannot be read counts as
none (`governance_record_unread`), and a record that could not be written after a merge is logged
(`governance_record_not_written`) and leaves the next commit to ask the review again.

A `disable_capability` that deletes values carrying a governance is a loosening governance change. Its record,
once reviewed and merged, is the governance before the disable, so an enable that brings the platform back trusted
asks the review too.
Between the plan and the pull requests the commit or wave reads the values on the default branch again: a
governance merged in between is recorded as a loosening governance change of the action (`status.governance`),
so the review shows it and only a member of the reviewers' team approves it (`governance_record_moved`).

## A second person approves every change

A commit whose plan changes the governance is recorded with the change on the Action (`spec.governance`, per
installation: `lines` old → new and `loosens`). The hub must store it; an Action CRD without the field is refused
before any pull request opens. The change rules are:

- such an action always asks for the Team review, also where every target is one of Giant Swarm's test
  installations, which otherwise need none;
- `approve_action` refuses the actor, by login and by GitHub id;
- the approver must be an active member of the governance reviewers' team, a GitHub team the chart names
  (`approvals.governanceTeam`, `org/team`), and a GitHub account of type `User`. The manager reads the membership
  as the approver. When the team is unset or the membership cannot be read, nothing is approved;
- `merge_action` merges nothing before that decision; a stage merged outside the manager decides nothing for
  the stages after it;
- an agent can propose such a change but never approve or merge it: muster asks a person before an agent's
  `approve_action` and `merge_action`.

The review leads with the governance, before the reason, and with what loosens it before the change itself:

```text
*Governance:* this changes the platform's governance; a second person approves it, on test installations too.
*It loosens governance:* rowan: mode governed → trusted
• rowan: mode governed → trusted
```

What loosens is never cut. A commit whose loosening lines do not fit in what Slack shows (1000 characters) is
refused: split it. The change lines that follow are cut at 1400 characters, the configs pull request named for the
rest. A change loosens governance, or may, when any of these hold:

- the mode leaves `governed`;
- a role floor goes;
- a source is no longer governed;
- a role changes;
- a safe entry is added, against the chart's list for a first override; one named like a write anywhere in its
  name, camelCase words included (`kubernetes_delete`, `port_forward`, `drainNode`) or a floor of any role is marked;
- muster's own governance changes, or a muster hold drops below the release that enforces it;
- who may loosen approvals widens: a subject added to `tenants.platformTeam` (which the meta chart adds to the
  connector-admin and workflow-editor bindings) or to `muster.rbac.mcpServerEditor.subjects` or
  `muster.rbac.workflowEditor.subjects` (decision #97); a subject removed tightens;
- the record differs from what the last reviewed action left;
- the agent-platform values cannot be read on either side.

Values from the record are escaped for Slack: no value carries a line, markup or a link into the review.

## Audit

Each step logs one line per installation whose governance the action changes (`audit=true`). Every line carries
the action, the installation, the actor, the caller, the decision and who decided it, the change and whether it
loosens.

| Event | When |
|---|---|
| `governance_change_committed` | the commit, wave or disable opened the pull requests |
| `governance_change_self_approval_refused` | the actor tried to approve their own change |
| `governance_change_approval_refused` | the approver is no active member of the team, no person, or their membership could not be read |
| `governance_change_approved` | a second person approved it; with the approver's login and id and how their membership was read |
| `governance_change_merged` | `merge_action` merged a stage of it; with the pull requests and their heads |
| `governance_change_merged_without_approval` | a pull request of it was merged outside the manager before the team approved, in any state of the action; read at the next read of the action |
| `governance_on_record_changed_outside` | a plan found the record's governance other than what the last reviewed action left |
| `governance_record_moved` | the values on the default branch changed between the plan and the pull requests |
| `governance_record_written` | `merge_action` merged an approved governance change: the installation's record now holds it |
| `governance_record_not_written`, `governance_record_unread` | the record could not be written or read; the next commit asks the review |
| `governance_unrecorded` | a plan found governance in play on an installation without a record |
| `governance_change_reverted` | a read of the Action found a reviewed governance change reverted on the default branch |

## What the manager does not cover

These limits are outside the manager:

- someone with write access to `giantswarm/<customer>-configs` can merge a change to the values outside the
  manager. Branch protection on those repositories (enforced for admins, re-approval after a new push, required
  CODEOWNERS review) is asked of their owners. The manager does not prevent such a change. It detects it at the
  next plan of the installation and asks the review then; a merge of one of its own Actions' pull requests
  outside it is audited when the action is read next.
- whoever holds Kubernetes write rights on muster's HelmRelease or ConfigMap can change the configuration
  directly.
- a second GitHub account that a person holds and that the team lists counts as a second person: team
  membership is the control.

## This manager's own safe list

The definition governs this manager (`giantswarm-platform-manager`, role `platformManager`, above). Its safe
list, the tools agents call unasked on `governed`, is:

| Tool | Why it is safe |
|---|---|
| `get_info` | who the caller is, the definitions and their schemas |
| `list_installations` | the installations and their capability states, read as the caller |
| `list_actions` | the Action records |
| `get_action` | one Action record |
| `verify_capability` | compares the repositories with a definition and writes nothing |

`get_action`, `list_actions` and `list_installations` bring the Action records up to date with GitHub as the
caller as they read them: an action can move to rolling out, failed or removed, and the facts are posted to its
review thread or the team's standup channel. They follow what GitHub says and decide nothing.

These tools stay asking:

- `enable_capability`, `reconcile_capability` and `disable_capability` ask agents even for a dry run: muster
  matches tool names, and the same tool commits with `mode: commit`. A separate plan tool would let agents
  plan unasked.
- `verify_installation` records its result on the Action.
- `watch_action` moves stages and posts to the review thread.
- `deny_action` closes pull requests.
- `approve_action` and `merge_action` are floors and are never safe.
