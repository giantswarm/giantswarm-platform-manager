# The tools

Generated from the registered tools (`make tools-doc`); through muster every tool is `x_giantswarm-platform-manager_<name>`. `verify_installation` and `watch_action` are registered only with the live tools on (`live.enabled`).

| Tool | Kind |
|---|---|
| `approve_action` | write |
| `deny_action` | write, destructive |
| `disable_capability` | write, destructive |
| `enable_capability` | write |
| `get_action` | read-only |
| `get_info` | read-only |
| `list_actions` | read-only |
| `list_installations` | read-only |
| `merge_action` | write, destructive |
| `reconcile_capability` | write |
| `verify_capability` | read-only |
| `verify_installation` | write |
| `watch_action` | write |

## `approve_action`

```text
Approve an action's pull requests as a member of the owning team: the Team review's Approve button. Not your own.

Submits an approving review on every pull request of the action as you and records the decision on the Action. The actor then merges with merge_action.
```

```json
{
  "properties": {
    "action": {
      "description": "The Action's name (the action id of the review).",
      "type": "string"
    }
  },
  "required": [
    "action"
  ],
  "type": "object"
}
```

## `deny_action`

```text
Deny a pending action, or withdraw your own merged one: closes its open pull requests and records the reason.

The Deny button of the Team review; any member may deny, the actor included, and a failed action whose approval is undecided is denied the same way. A merged action is withdrawn by its actor alone, once it failed or was reverted, or a revert is found now on the default branch; with nothing taken back it is refused. The review's thread is told.
```

```json
{
  "properties": {
    "action": {
      "description": "The Action's name.",
      "type": "string"
    },
    "reason": {
      "description": "Why the action is denied or withdrawn; recorded on the Action and, for a withdrawal, in the review's thread.",
      "type": "string"
    }
  },
  "required": [
    "action",
    "reason"
  ],
  "type": "object"
}
```

## `disable_capability`

```text
Disable a platform capability on one installation: removes its files through an Action, approved like an enable.

Dry-run first: it names every file a commit deletes or edits, the files another capability keeps, and the cluster objects Flux does not prune, as a checklist in deletion order. A commit is refused while something depends on the capability (a federation, a portal entry, a paired value). Writes as you (your rights ∩ the App's): dryRun: true answers the plan and writes nothing; mode: "commit" opens the pull requests through an Action and its approval — wait for the answer.
```

```json
{
  "properties": {
    "capability": {
      "description": "The capability (default agent-platform); get_info lists the definitions and their input schemas.",
      "enum": [
        "agent-platform",
        "customer-portal",
        "cluster-mcp-servers"
      ],
      "type": "string"
    },
    "content": {
      "description": "Include each edited file as the commit writes it and as it is on record (default: true).",
      "type": "boolean"
    },
    "dryRun": {
      "description": "Answer the plan and write nothing (default false).",
      "type": "boolean"
    },
    "installation": {
      "description": "The one installation to take the capability off, by name.",
      "type": "string"
    },
    "keep": {
      "description": "Files the disable keeps, by path or repository:path, with the kustomization entries naming them; the definition's marker always goes.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "mode": {
      "description": "\"commit\": open the pull requests as you through an Action. Required unless dryRun.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "reason": {
      "description": "mode commit only, required: why, in a sentence a teammate judges the change by; the team's review and the pull requests show it.",
      "type": "string"
    }
  },
  "required": [
    "installation"
  ],
  "type": "object"
}
```

## `enable_capability`

```text
Enable a platform capability on an installation: the plan of files, pull requests and secrets a commit writes.

A set renders only the installations that have it on record, as reconcile_capability does. Dry-run first: the plan names the files, the pull requests in order, the generated secrets, the Dex clients, the secrets you supply and the customer's actions. No secret value ever appears in a dry run. Writes as you (your rights ∩ the App's): dryRun: true answers the plan and writes nothing; mode: "commit" opens the pull requests through an Action and its approval — wait for the answer.
```

```json
{
  "properties": {
    "capability": {
      "description": "The capability (default agent-platform); get_info lists the definitions and their input schemas.",
      "enum": [
        "agent-platform",
        "customer-portal",
        "cluster-mcp-servers"
      ],
      "type": "string"
    },
    "content": {
      "description": "Include each file's rendered and recorded content (default: true for one installation, false for a set). Above 1 MiB is refused.",
      "type": "boolean"
    },
    "dryRun": {
      "description": "Answer the plan and write nothing (default false).",
      "type": "boolean"
    },
    "forceJoin": {
      "description": "Only after a commit refused for the join: proceed although the reconcile takes servers, providers or redirects off an installation's muster.",
      "type": "boolean"
    },
    "inputs": {
      "description": "Your typed inputs of the definition (get_info lists the schema), laid over its defaults and what is on record. An unknown key is refused. No secrets.",
      "properties": {},
      "type": "object"
    },
    "installation": {
      "description": "The one installation to render, by name: a fresh enable, or the changes to one on record. The answer says why a commit would be refused.",
      "type": "string"
    },
    "installations": {
      "description": "The set to render (empty, with no installation: every one): each plan rolled up to what differs. Without the capability: skipped.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "mode": {
      "description": "\"commit\": open the pull requests as you through an Action. Required unless dryRun.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "order": {
      "description": "The set's rollout order when not the default (test installations, the hub, the customers): every installation exactly once.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "reason": {
      "description": "mode commit only, required: why, in a sentence a teammate judges the change by; the team's review and the pull requests show it.",
      "type": "string"
    },
    "rotate": {
      "description": "Generated values to rotate, by name as the plan lists them. Every other value of a rewritten file rotates with it; their workloads restart.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "secrets": {
      "description": "mode commit only: the values the plan's suppliedSecrets name: <field> for every installation of a set, <installation>/<field> for one.",
      "properties": {},
      "type": "object"
    }
  },
  "required": [],
  "type": "object"
}
```

## `get_action`

```text
One Action record by name: actor, installations, capability, inputs, pull requests, approval, rollout and result.

The record follows GitHub, read as you: a pull request merged or closed outside this manager is recorded, and a fileset gone from the default branch moves the action to removed, naming the objects left for a person to delete.
```

```json
{
  "properties": {
    "name": {
      "description": "The Action's name.",
      "type": "string"
    }
  },
  "required": [
    "name"
  ],
  "type": "object"
}
```

## `get_info`

```text
Who you are to this server, the capability definitions with their input schemas, and how writes are approved.

Call it first. Also the write tools, where the Action records live, and the identity forwarding this call carried for the live tools.
```

```json
{
  "properties": {},
  "required": [],
  "type": "object"
}
```

## `list_actions`

```text
List the Action records on the hub, newest first; narrow with installation and capability.

Each follows GitHub as get_action does.
```

```json
{
  "properties": {
    "capability": {
      "description": "Only actions of this capability.",
      "enum": [
        "agent-platform",
        "customer-portal",
        "cluster-mcp-servers"
      ],
      "type": "string"
    },
    "installation": {
      "description": "Only actions that include this installation.",
      "type": "string"
    }
  },
  "required": [],
  "type": "object"
}
```

## `list_installations`

```text
List every installation with each capability's state: enabled, pending approval, rolling out, drifted, failed, …

Read with your GitHub token now: an installation whose repositories you cannot read is listed as unreadable with the reason. Also each installation's inputs on record, last action and whether agent-platform workspaces are on. Narrow with installations or customer; summary: true answers states, workspaces and last actions alone, a fraction of the reads.
```

```json
{
  "properties": {
    "customer": {
      "description": "Answer only for this customer's installations, as the catalog names the customer.",
      "type": "string"
    },
    "installations": {
      "description": "Installation names to answer for; empty is every installation of the registry.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "summary": {
      "description": "Answer states, workspaces and last actions alone, without the record and inputs: an overview (default false).",
      "type": "boolean"
    }
  },
  "required": [],
  "type": "object"
}
```

## `merge_action`

```text
Merge an approved action's pull requests as its actor, in dependency order, each once its checks are green.

The Action moves to rolling out and the review's thread is told; follow it with watch_action. Before the approval it answers what the action waits for and posts the review if none is up. Pending checks stop the call: call again.
```

```json
{
  "properties": {
    "action": {
      "description": "The Action's name.",
      "type": "string"
    }
  },
  "required": [
    "action"
  ],
  "type": "object"
}
```

## `reconcile_capability`

```text
Reconcile a capability over the installations that have it (empty: all): enable_capability's plan, per installation.

A plan whose files are all unchanged means the installation matches the definition. Installations without the capability are skipped and listed; a fresh enable is enable_capability. Writes as you (your rights ∩ the App's): dryRun: true answers the plan and writes nothing; mode: "commit" opens the pull requests through an Action and its approval — wait for the answer.
```

```json
{
  "properties": {
    "capability": {
      "description": "The capability (default agent-platform); get_info lists the definitions and their input schemas.",
      "enum": [
        "agent-platform",
        "customer-portal",
        "cluster-mcp-servers"
      ],
      "type": "string"
    },
    "content": {
      "description": "Include each file's rendered and recorded content (default: true for one installation, false for a set). Above 1 MiB is refused.",
      "type": "boolean"
    },
    "dryRun": {
      "description": "Answer the plan and write nothing (default false).",
      "type": "boolean"
    },
    "forceJoin": {
      "description": "Only after a commit refused for the join: proceed although the reconcile takes servers, providers or redirects off an installation's muster.",
      "type": "boolean"
    },
    "inputs": {
      "description": "Your typed inputs of the definition (get_info lists the schema), laid over its defaults and what is on record. An unknown key is refused. No secrets.",
      "properties": {},
      "type": "object"
    },
    "installation": {
      "description": "The one installation to render, by name: a fresh enable, or the changes to one on record. The answer says why a commit would be refused.",
      "type": "string"
    },
    "installations": {
      "description": "The set to render (empty, with no installation: every one): each plan rolled up to what differs. Without the capability: skipped.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "mode": {
      "description": "\"commit\": open the pull requests as you through an Action. Required unless dryRun.",
      "enum": [
        "commit"
      ],
      "type": "string"
    },
    "order": {
      "description": "The set's rollout order when not the default (test installations, the hub, the customers): every installation exactly once.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "reason": {
      "description": "mode commit only, required: why, in a sentence a teammate judges the change by; the team's review and the pull requests show it.",
      "type": "string"
    },
    "rotate": {
      "description": "Generated values to rotate, by name as the plan lists them. Every other value of a rewritten file rotates with it; their workloads restart.",
      "items": {
        "type": "string"
      },
      "type": "array"
    },
    "secrets": {
      "description": "mode commit only: the values the plan's suppliedSecrets name: <field> for every installation of a set, <installation>/<field> for one.",
      "properties": {},
      "type": "object"
    }
  },
  "required": [],
  "type": "object"
}
```

## `verify_capability`

```text
Compare an installation's repositories with a capability's definition, each feature as defined, planned or drifted.

Names every difference by file and path and the input that drives it, a planned change a newer definition removes, or drift, and the plan a commit would write. The running installation is verify_installation's.
```

```json
{
  "properties": {
    "capability": {
      "description": "The capability (default agent-platform); get_info lists the definitions and their input schemas.",
      "enum": [
        "agent-platform",
        "customer-portal",
        "cluster-mcp-servers"
      ],
      "type": "string"
    },
    "content": {
      "description": "Include the rendered content of every file (default false); true answers the files as a commit would write them.",
      "type": "boolean"
    },
    "inputs": {
      "description": "Your typed inputs of the definition (get_info lists the schema), laid over its defaults and what is on record. An unknown key is refused. No secrets.",
      "properties": {},
      "type": "object"
    },
    "installation": {
      "description": "The installation to verify, by name.",
      "type": "string"
    }
  },
  "required": [
    "installation"
  ],
  "type": "object"
}
```

## `verify_installation`

```text
Verify the running installation against a capability's definition through muster, as you: its probes and objects.

What you may read decides what is checked: an object you may not read is not checked, never a failure; not connected in muster, it answers muster's sign-in. Reads are bounded (2 minutes in all), and what did not answer is not checked. The result is recorded on the installation's newest action and feeds list_installations. The repositories are verify_capability's.
```

```json
{
  "properties": {
    "capability": {
      "description": "The capability (default agent-platform); get_info lists the definitions and their input schemas.",
      "enum": [
        "agent-platform",
        "customer-portal",
        "cluster-mcp-servers"
      ],
      "type": "string"
    },
    "inputs": {
      "description": "The inputs object of verify_capability's answer; default: the newest action's on record. Without either, live dimensions read not checked.",
      "properties": {},
      "type": "object"
    },
    "installation": {
      "description": "The installation to verify, by name: the management cluster muster reads for you.",
      "type": "string"
    }
  },
  "required": [
    "installation"
  ],
  "type": "object"
}
```

## `watch_action`

```text
Follow a merged action's rollout on the installation, as you. Call again while it is rolling out.

Reads the HelmReleases through muster, then runs the probes: the stage moves to enabled, waiting for the customer, or failed with the red probe named; a failed one is re-read once the cause is fixed. On a wave the actor merges the next stage with merge_action once one is enabled. A reverted stage moves the action to reverted; its actor withdraws it with deny_action. The report goes into the review's thread. Anyone signed in may watch.
```

```json
{
  "properties": {
    "action": {
      "description": "The Action's name.",
      "type": "string"
    }
  },
  "required": [
    "action"
  ],
  "type": "object"
}
```
