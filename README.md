# giantswarm-platform-manager

Giant Swarm's **platform manager**: enables, reconciles and verifies platform capabilities on the
registry's installations as the person, through MCP tools behind muster. Every write is a pull request
to the installation's GitOps repository, opened as the person; nothing is applied to a cluster
directly.

The `giantswarm-` prefix marks a manager specific to the giantswarm org — like
`giantswarm-repo-manager`, unlike `agent-manager`, `model-manager` and `cluster-manager`. The name is
final: *platform manager*, this repository, chart, GitHub App and muster registration
`giantswarm-platform-manager`, the CLI `platformctl`.

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
| `list_installations` | read | Every installation of the registry with, per capability, its state, the inputs on record and the last action, every read as you at call time. `installations` (names) and `customer` narrow the answer; `summary: true` answers the states and the last actions alone, without the record, the portals and the federation facts — the overview's call. Every read of a call runs at once, 32 in flight at most. |
| `enable_capability` | write | Enable a capability on one installation (`installation`) or a set (`installations`): with `dryRun: true` the plan — files per repository with the change each one is against the repository now, pull requests in dependency order, generated secrets by name, Dex clients and redirect URIs, the secrets the person supplies at commit (by field), customer actions, probes. `inputs` are the definition's typed inputs over the facts on record. With `mode: "commit"` and one `installation`: the gate, the Action in *pending approval*, the pull requests as the person (see [The commit](#the-commit)); `secrets` carries the supplied values by field. |
| `reconcile_capability` | write | The same render over a set (empty: every installation of the registry), every file compared with the repository: all *unchanged* is an empty diff. Installations without the capability on record are listed as *skipped*: a wave reconciles what is on record; a fresh enable is `enable_capability` with one installation. `rotate` names generated values to rotate on request (see [Rotation on request](#rotation-on-request)). A plan whose agent-platform values patch takes off the installation's muster what it serves beyond the shared defaults — the broker's exchange targets, the identity providers, the MCP servers it lists, a public-registration redirect URI that moves to another installation of the set — carries it as `join`, naming each entry, the warning of a hub join; the commit is refused (`commitRefused`) while the muster still serves in-cluster servers of its own, until `forceJoin: true` (`platformctl --force-join`) proceeds with the warning. |
| `disable_capability` | write | The mirror of `enable_capability` on one `installation`: with `dryRun: true` the removal — the marker and every file of the definition's directories as deletions — what a person wrote there beside the render too, unless a capability that stays renders it, with the kustomization entries that name it —, the files a capability that stays keeps edited down to its own entries, the kustomizations that list them, the Dex clients of the installation's patch whose Secret is in a removed file or whose redirect URIs name the platform's hosts with their trusted-peer ids, under `leftOnRecord` every client it cannot attribute and why, the files `keep` names kept with their entries (`kept`), and, where the definition's Kustomization does not prune, a checklist of the cluster objects a person deletes afterwards, the remote bases' objects included, in deletion order, each line naming what its deletion takes with it (a base that cannot be read refuses the dry run). A capability another installation federates, a portal entry points at, or a peered pairing still uses is refused with the reference named (`commitRefused`). With `mode: "commit"` the same Action, approval and merge path as an enable; once merged the action is *disabled*, its checklist on `status.orphans`, and `list_installations` and `verify_capability` read the capability *not enabled*. |
| `get_action`, `list_actions` | read | The Action records on the hub: one per enablement or reconcile a person commits — actor, installations, capability, inputs, pull requests, approval, rollout, probes, result. The record follows GitHub on every read, as you (at most once a minute per action): a pull request merged outside `merge_action` is recorded *merged* with its commit, time and `mergedBy`, and the action rolls out as after the merge; one closed unmerged fails it; a fileset gone from the default branch again moves it to *removed*, naming the objects left on the installation. See [The Action record](#the-action-record). |
| `verify_capability` | read | One installation against a capability's definition, grouped into the definition's features with one mark each — *as defined*, *planned*, *differs by input*, *drifted* — and expanded to its dimensions: the owning repositories' files, read as the person, against the render from the inputs on record (every difference names the file, the path and the person's input that drives it — a choice the schema names, or one typed for the call, so the file expresses another choice than the one on record — the planned change it is — a key the capability's `removals.yaml` or `migrations.yaml` names — or drift: a leaf no choice drives, the ones the installation's facts derive included, which only a reconcile resolves), and the definition's anonymous HTTP probes. The live dimensions read *not checked* here: they are `verify_installation`'s. |
| `verify_installation` | read, **live** | The same installation's running objects against the definition's probes — HelmReleases Ready, workloads Available, Secrets and MCPServer objects present, conditions, logs, Dex started since every client Secret it reads last changed, the live values against the render — read through muster's kubernetes tools **as the person**, with the platform ID token muster forwards next to the App user token (`X-Muster-Id-Token`, the `MCPServer`'s `auth.forwardIdentity` with `live.enabled`, muster ≥ 5.31.0); a call without a valid one is refused, naming the header. What the person may read decides what is checked: an object they may not read is *not checked, forbidden for them*, an installation they are not connected to answers with muster's own sign-in. Handed `verify_capability`'s `inputs`, a missing object the plan creates and the record lacks yet (`inputs.planned`: a migration's Secret, a Dex client the dex patch on record lacks) reads *planned*, naming the migration. The result is recorded on the installation's newest action and feeds `list_installations`: *drifted*, or *waiting for the customer* when the only red dimension is the one the customer's action holds up. A portal or `platformctl` shows the two verifies as one result. |
| `watch_action` | **live** | The rollout watch of an action whose pull requests are merged, **as the person calling** — the manager holds no token beyond a call, so the watch is a call (the portal's page, `platformctl action watch`, an agent), never a loop. Reads the Flux objects the definition names on the installation rolling out (the HelmReleases with their Ready condition and revision) through muster's kubernetes tools and answers the picture; once every one is Ready it runs the definition's probes — the live dimensions as the person, the anonymous HTTP probes direct — and the stage moves to *enabled*, *waiting for the customer* (the customer's own action is the only thing open) or *failed* (a probe is red, named). A HelmRelease whose last release Flux reports failed — `Released=False` after an install or upgrade Helm gave up on, rolled back or not, or `Stalled=True` — will not become Ready on its own: the stage is *failed* on its probe, Flux's reason and message named, and a later watch re-reads it. The report — pull requests, rollout per object, each probe, the open customer actions — goes into the review's thread and onto the Action (`status.rollout.installations[]`, `status.probes`, `status.result`). Nothing is waited for or hurried: call again while it is rolling out. Anyone signed in may watch; the reads are theirs, and the state follows the picture whoever read it. An action *waiting for the customer* or *enabled* is re-read: the customer's action done flips it to *enabled*. The watch first re-reads the action's pull requests from GitHub as you, as `get_action` does, with the GitHub token muster puts on the call next to your forwarded identity: an action whose pull requests were merged outside the manager is watched all the same. Before it reads the installation it reads, with the same token, whether the stage's pull requests are still on the default branch: a revert leaves every object healthy on the previous values, so the probes cannot tell it — see [The Action record](#the-action-record). |

## The commit

`mode: "commit"` of the two write tools takes **one** `installation` (a set is the dry run's) and runs, in
this order, writing nothing before the gate:

1. **The gate** — the installation is on record readably as the person: its repositories known to the registry
   and readable as you, read at call time. Otherwise the commit is refused naming the installation and the
   reason, and the refusal is recorded as an Action in state *refused* (the installation's state read from its
   repositories stands).
2. **The plan**, as the dry run renders it; a definition's refusal, a file that could not be compared as the
   person, a generated value frozen where no rotation is possible (below), a section of the hub's Dev Portal on
   record that a `customer-portal` plan would remove (`hubSections`), a `rotate` name the plan does not list,
   or a supplied secret left out of `secrets` (or one the plan does not ask for) refuses the commit before any
   write. The plan's own refusals — the dex-app on record, the release candidates dropped while one is ahead of
   the stable release, the frozen values, the hub's portal, the registry the installation's muster still serves
   that a join as a target would drop (`join`, unless `forceJoin`) — are one list
   the commit, the wave's pre-check and the dry run's `commitRefused` share. Every file on record
   already: nothing to commit, no Action.
3. **The Action** — created in *pending approval* with the actor, the capability, the installation and the
   inputs (never a secret value: `secrets` is its own argument and lands nowhere but the encrypted files).
4. **The files**: the plan rendered again with the supplied values; a plain file must be byte-identical to the
   plan (a value never lands outside a secret file), the secret files get their generated values and are
   encrypted with gitops-commit's `sopsenc` for the recipients of the repository's `.sops.yaml` (read as the
   person; a repository the plan writes a generated value into refuses the commit without one; a repository
   that gets plain files only — teleport-fleet's tunnelport values — needs none). A secret file on record is **kept** as long as the
   render changes nothing outside its values: the manager decrypts nothing, so the two are compared as YAML
   with the values the record holds encrypted (the fields under the repository's `encrypted_regex`) and the
   values the commit fills in left out — same keys, same metadata, same plaintext fields → `unchanged`, the
   values in it *frozen* and its generated names `kept`; nothing is written. The labels and annotations of an
   encrypted file take no part: the manager never rewrites an encrypted file for them, since a rewrite draws
   every value it holds anew, and the kustomization carries them instead (Flux's
   `reconcile.fluxcd.io/watch` on the credentials revision Secrets is a patch in the directory's
   kustomization). A reconcile of an installation the manager enabled therefore rewrites no secret and
   rotates nothing. A value on record stays **kept** wherever the render puts it: a file of the name
   that lacks it — a file to create (a server's Dex client Secret next to its existing credentials file, as
   the 4 line renders it over a 3-line record), or a file on record the render adds the key to and changes
   nothing else of (mcp-prometheus's credentials Secret taking the Valkey password its Valkey Secret holds)
   — takes the value from the record by the caller's vault before the commit, since the manager decrypts
   nothing: the dry run lists each **carry** (`generatedSecrets[].carries`, the record's file and key path
   it comes from and the file and key path it goes to, with the `beekeeper secret copy` or sops step;
   `platformctl installation dex-split` writes a Dex client's Secret this way and moves the client out of
   the dex-app patch with it), and the commit is refused until the carry is on record (`commitRefused`).
   A value no file on record holds that a file on record lacks is drawn there by the vault the same way
   (`beekeeper secret set`, a carry without a `from`), and every other file to write takes it from there; a
   credentials revision a file on record lacks waits instead (`pendingIn`): the file stands, the revision
   joins it with the next rotation asked for. A file to create whose values no record holds is written at
   commit. A name **rotates** only when a file of the name is written anew whole — an existing file whose
   plaintext skeleton the render changes beyond the keys it lacks (a field of its template changed), or a
   plain file to write carrying a key pair's public half: one new value is drawn and written
   into every file that holds it, the kept files rewritten and encrypted anew — and every other value a
   rewritten file holds rotates with it, down to the files sharing those (the server's Valkey password into
   its Valkey Secret). A name also rotates when the person asks for it by name ([Rotation on
   request](#rotation-on-request)). The dry run says so (`generatedSecrets[].frozenIn`, `kept`, `rotates`
   with `forcedBy`, the file that forced it or `request`, and `cause`, why that file is written whole), the
   Action records the rotated names
   (`status.rotated`), the pull request names them. For the running installation a rotation means both sides roll: the server and the Dex client take
   the new value with their Secrets, and the client is unusable between the two rollouts. The roll follows
   the commit: each MCP server's credentials carry a *credentials revision* (`<installation>-mcp-<name>-credentials-revision`,
   a generated value held by the server's credentials Secret, its Valkey's and a third Secret
   `mcp-<name>-credentials-revision` beside the HelmReleases), so a rewrite of either credentials file draws
   it anew, and the server's and its Valkey's HelmReleases read it (`valuesFrom` with `targetPath`) into
   their charts' checksum values, whose pod-template annotations restart the pods; a reconcile without a
   rotation keeps the revision and changes no pod template; muster's own credentials carry the same revision
   (`<installation>-muster-credentials-revision`, the Secret `muster-credentials-revision`, handed through the meta
   chart's `components.<name>.valuesFromRefs` to every workload that reads them: muster, its Valkey and, where each
   runs, klaus-gateway and the agent-, cluster- and model-manager). Unseen by the
   comparison: a literal the render changes under an encrypted field — the record holds it encrypted. A
   value frozen in a file the definition does not own whole (one with several owners) cannot rotate and
   refuses the commit naming the file. Where the capability is on record (its marker file exists), a
   rotation no request reaches refuses the commit too, naming the value and the file that forces it: a new
   value ends every session and client that holds the old one (a new `*-oauth-encryption-key` signs out
   everyone who uses the server), so a reconcile rotates only what a person asked for with `rotate`, and
   what that request's rewritten files hold with it. The first enable of an installation set up by hand
   still rotates a value a new file shares with one on record, the dry run naming it.
5. **The pull requests** through gitops-commit, as the person, in dependency order — a pull request whose
   files create an object another one's files reference merges before it: the management-clusters Secret
   before the configs dex patch that names it through `secretRef`, teleport-fleet's tunnelport tokens before
   the release and the RemoteApps that name them; among the rest, configs, management-clusters, the hub's
   pair, teleport-fleet — one commit per repository, on branch `platform/<action>/<installation>`, titled in
   conventional-commit form — `feat(<installation>): enable <capability> (<action>)`, `fix(<installation>):
   reconcile <capability> (<action>)` — so the repositories' semantic-pull-request check passes as opened, the
   action id in the title and body. The Action records them and stays in *pending approval*: the approval, the merge
   and the rollout follow. An action on Giant Swarm's test installations alone — the hub's customer's, not the
   hub: the wave's first stage — needs no Team review: the approval is recorded *not required*, the Action reads *ready to merge*, the actor merges
   with `merge_action` once green, and the merge is told to the team's standup channel — who, what, why and
   what changed (`approvals.standupChannel`; unset, such a commit is refused). The hub and every customer
   installation keep the review. A failure on the way moves the Action to *failed* and closes the pull requests
   opened so far as the person, branches deleted, recorded *closed* with the reason on the Action; one the
   remote refused to close stays open on the record, and `deny_action` — which takes a failed action too —
   closes it, records the reason and leaves the action failed.

A commit takes `reason` — why the actor makes the change, in a sentence; without one nothing is committed.
**The review** in the team's channel (`approvals.channel`) is what a teammate judges the action by from
Slack alone: who asks to enable or reconcile which capability on which installations (whose customer, the
account engineer the catalog records), the reason quoted, and *what changes* — a line per component from
the comparison (`spec.changes`, `plan.Summary`): a component created whole reads *new*, versions read old →
new, list entries added and removed, a kustomization's patches by their target with the JSON patch
operations they add, drop or change (op and path), values keys by path without their values, a Secret's keys
added and removed, the credentials that rotate (the ones asked for marked *on request*) — with the pull requests as
links; a wave whose installations change alike reads once, else per installation. The Account Engineers'
channel (`approvals.noticeChannel`) never sees the review: once a stage on a customer installation reaches
*enabled* — the watch or the live verify — it is told once, without buttons, who applied what where, why,
what changed and the account engineer (`status.rollout.installations[].noticedAt`).

The answer is the Action, the pull requests and the plan; no secret value appears in it, in a log or in a
pull request. The manager holds no token of its own: `GITHUB_API_URL` is the GitHub the person's token goes to.

## The dry run

`enable_capability` and `reconcile_capability` with `dryRun: true` render an installation through the
capability's definition (the [render library](render/README.md)) from the facts on record — the
registry's and the installation's `config.yaml.patch` — with the person's typed `inputs` over them. The
schema is the contract: a typed `installation.*` key overrides the record, an unknown key refuses with its
name, a required choice left out (`kagent.enabled`, `portal.enabled`, `toolAccess.agentManager`,
`federation.targets`/`hubs`) refuses naming it — nothing is chosen for the person. A refusal is the
installation's answer in the plan, not a tool error, so a set still answers for the others.

The plan per installation: its state, the effective inputs, the files with their repository
(the registry's, not the definition's `giantswarm/<customer>-…` names), path, rendered content and change
(*create*, *update*, *unchanged*, *unknown* when the current file could not be read as the person) — an
encrypted file kept *unchanged* names as `unseen` the literals the render puts under a field the record
holds encrypted (a credentials Secret's client id), with the render's value: the comparison decrypts
nothing, so the value on record stands whether or not it is that one; an encrypted file the commit writes over
whose skeleton is not the render's (kept by hand, or by an earlier shape) names what it loses there, read without
decrypting: `dropped`, each value under a readable key the render carries no leaf for (the comparison plans it as
*Removed:* by name), and `replaced`, each encrypted text the render writes a document of its own in (a Secret's
`stringData.values`), whose keys no one reads, so a value it holds that the render does not carry is lost unless the
definition asks for it at commit —, the shared-kustomization includes, the generated secrets by name, kind and length — with `frozenIn`, the files
on record that hold the value already, `kept` when the value on record stands and the commit writes no
file of the name, `carries` for the files that take the value from the record by the caller's vault before
the commit, `pendingIn` for the files on record that stand without a credentials revision, and `rotates`
with `forcedBy`, the file written anew whole, and `cause`, when the commit draws a new value
into every file of the name (see [The commit](#the-commit)) — the secret values the person
supplies at commit by field (rendered as `SUPPLIED(<field>)` markers — no secret value ever appears in a dry
run), the Dex clients with their redirect URIs from the rendered dex patch, the customer actions (Secrets the
definition references and never renders) and the probes (the definition's live dimensions). Over the set:
the pull requests, one per repository in dependency order — a pull request whose files create an object
another one's files reference merges before it (a Secret a Dex client's `secretRef` names, a Teleport token
a RemoteApp or the tunnelport release names), and says which ones it follows and why (`after`); the pull
requests nothing references keep the repositories' order: an installation's configs, its
management-clusters, the hub's pair, `teleport-fleet` — with the files, changes and generated secrets each
carries, every file saying what it `creates` and `references`; the wave's `order` (Giant Swarm's own test installations, the hub, then the
customers' installations); and `skipped` with the reason (*not enabled*, *unreadable*, *no repositories on
record*): a wave reconciles what is on record and never enables. An installation named as `installation` is
rendered whether or not its fileset is on record — a fresh enable, or the changes to it — with `commitRefused`
saying why a commit would be refused. `order` names another
rollout order for the set (every rendered installation exactly once).

`reconcile_capability` with `mode: "commit"` over a set is **one wave**: one Action, one review listing the
targets in the rollout order and the skipped, the pull requests per installation on their own branches.
`merge_action` (the actor's) advances it one stage per call — merge the first installation's pull requests
once green; `watch_action` (anyone's, as the person) carries the installation rolling out to
*enabled*; only then does the next `merge_action` take the next installation's pull requests — and a red
probe stops it: that installation *failed*, the stages after it not started, their pull requests open, the
result naming where and why. A stage *waiting for the customer* holds the wave too: `merge_action` says
so, and the customer's action done flips the stage on the next watch. A wave takes the supplied secret
values its targets' plans ask for in `secrets`, in the same call: a key `<field>` goes to every target
that asks for the field (the two sides of a pairing hold one value), `<installation>/<field>` to that
target alone; a value missing or one no target asks for refuses the wave whole, by field, before
anything is written. `platformctl installation reconcile <installation>... --commit --secret
<field>=beekeeper:<ref>` (repeatable) resolves each reference through `beekeeper secret copy <ref> --
<consumer>`, so every value of a wave is passed in one run and never printed.

### Rotation on request

`reconcile_capability` (and `enable_capability`, which shares its plan) takes `rotate`: generated values
by name as the dry run lists them (`generatedSecrets[].name`, `<installation>-muster-valkey-password`).
Each one the plan lists rotates whatever its files: the dry run shows it `rotates` with `forcedBy:
"request"` and `frozenIn` the files on record that hold it, and those files read *update*. The definition
names, for each value of a component's credentials Secrets, the credentials revision its consumers roll
on (`render.Result.Revisions`, rendered next to the revision: muster's four credentials — its Dex client
secret, registration token, OAuth encryption key and Valkey password — on
`<installation>-muster-credentials-revision` on the 4 line, each MCP server's on its own); a value asked
for draws its revision with it, also `forcedBy: "request"`, so every workload that reads the value
restarts. The rest follows the rotation above: every file on record that holds a rotating name is
rewritten with every value it holds — a rotation of muster's Valkey password rewrites
`muster-valkey-credentials.yaml`, `muster-oauth-credentials.yaml` and `muster-credentials-revision.yaml`,
which share the revision, and `dex-client-muster-secret.yaml` with the client secret — and nothing else
rotates. The commit records the names asked for (`spec.rotate`); the review marks them (*rotates
valkey-password (on request)*), the change and the pull request name them apart from the rotations a file forced.

Names carry their installation, so over a set a name applies to each installation whose plan lists it
and leaves the others as they are; a name no plan of the set (or of the one installation) lists is
refused, naming it, before anything is written — the dry run as well. A name frozen in a file with other
owners is refused like any rotation through such a file. A value no revision covers (kagent's
oauth2-proxy credentials, the gateway's OBO keys, the hub's token-exchange credentials) rotates in its
files and rolls nothing with it; dex-k8s-authenticator's client secret is not a generated value (it lives
in the dex-app encrypted patch, which the manager does not write) and cannot be rotated here.

## The Action record

Every enablement or reconcile a person commits is an `Action` — `platform-manager.giantswarm.io/v1alpha1`,
namespaced, on the hub in the manager's namespace, read and written with the manager's own ServiceAccount:
the record is the manager's, not the person's. `spec` is written once (`actor`, `capability`, `kind`
enable|reconcile, `installations` in the wave's order, `inputs`, `markers` — per installation the
definition's enabled marker in the installation's repository, `rotate` — the generated values asked to
rotate); `status` is a subresource (`state`,
`pullRequests`, `approval`, `rollout`, `probes`, `result`, `syncedAt`/`syncedBy`, `orphans`, `withdrawal`). `get_action`
and `list_actions` read it; `mode: "commit"` creates it and moves its state;
`list_installations` carries the newest Action of an installation and capability as `lastAction`, and an
unfinished or failed action's state stands over the state read from the files.

The states: *pending approval*, *ready to merge* (no Team review needed: the actor merges), *rolling out*, *waiting for the customer*, *enabled*, *drifted* and *failed*
are the installation's states an action produces; *refused* (the gate refused it before any write),
*denied* (a member withdrew it before its merge), *reverted*, *withdrawn* and *removed* (below) are the
action's own, and the installation's state read from its repositories stands. A pull request is *open*,
*merged* (with `mergeCommit`, `mergedAt` and `mergedBy`, and `revert` once it was read reverted) or *closed*.

**A merged pull request reverted on the default branch reads *reverted*, never *enabled*.** A revert
leaves every object of the installation healthy on the previous values, so the probes cannot tell it:
`watch_action` reads, before the installation, whether the stage's pull requests are still on the default
branch, as the person with their GitHub token. A pull request is reverted when every file its merge commit
changed carries its content from before the merge again — a file it added absent, one it modified or
removed as it was; a later change that rewrote a file is not a revert. The action moves to *reverted*, the
pull request's `revert` naming the reverting commit and the pull request GitHub links it to, the stage and
the result naming them, the review's thread told; a read that fails decides nothing, and the watch refuses
a reverted action. The merge's change is read once per commit and kept (the commit, and for a file it
modified or removed its parent's tree); each watch costs the repositories' trees at HEAD, validated as the
person, and a revert found two requests more to name it.

**The actor withdraws a merged action** with `deny_action` and the reason: one *failed* after its approval
(a stage failed and rolled back, a wave stopped on a red probe), one *reverted*, and one that still reads
*rolling out*, *waiting for the customer*, *enabled* or *drifted* once the revert is read at the call (none
found: nothing was taken back, and the withdrawal is refused). Any pull request still open is closed, the
action moves to the terminal state *withdrawn* with `status.withdrawal` (by, reason, at), the approval as
decided, and the review's thread is told. Approving stays a second person's; withdrawing a merged action is
the actor's alone.

**The record follows GitHub, not only the manager's own steps.** Every read of a record — `get_action`,
`list_actions`, `list_installations` (the portal's page), the approval tools before they decide, and
`watch_action` through the App-pinned registration — reads the action's open pull requests and, for every
stage whose pull requests are merged, its marker, as the person reading, with their token, at most once a
minute per action (`syncedAt`, `syncedBy`). A pull request merged outside `merge_action` — by a person with
the repository's own merge path — is recorded *merged* with the merge commit, the time and the login that
merged it; once every pull request of the stage in flight is merged the action moves to *rolling out* as
after `merge_action`, the approval recorded as *merged without approval by <login>* when the team had not
decided, the review's thread told, and the rollout watch and the probes follow. A pull request closed
unmerged moves the action to *failed*, naming it and any pull request left open (`deny_action` closes them).
When the fileset an action wrote is gone from the repositories' default branch again — the marker absent
after the merge: a revert — the action moves to the terminal state *removed*, and because the fleet's
Kustomization over the extras tree does not prune, the record names the objects the definition rendered on
the installation that stay until a person deletes them — the HelmReleases the definition's probes name and
every manifest among its files, by kind, namespace and name (`status.orphans`), read from the render of the
inputs on record; the manager deletes nothing on the cluster. The manager holds no token of its own, so
nothing resyncs unattended: the record is at most a minute behind GitHub whenever anyone reads it.

The chart renders the CRD,
a Role over `actions` and `actions/status` in the release namespace and its binding (`actions.enabled`,
`actions.installCRD`), and hands the namespace to the server as `ACTIONS_NAMESPACE`; without it
`get_action`, `list_actions` and `mode: "commit"` refuse with the reason and `get_info` reports
`actions.configured: false`.

Every write tool is registered through one framework, which owns two arguments:

- `dryRun: true` returns the rendered change and writes nothing;
- `mode: "commit"` opens the pull request as the person — the only write mode;
- `mode: "apply"` **is refused for every write tool, present and future**, before the tool runs: every
  target of this manager is GitOps-owned, and a change applied without its commit is drift the next
  reconcile reverts. `get_info` reports `capabilities: {commit: true, apply: false, applyRefused: true}`.

## The registry

`list_installations` knows every installation from two sources, both read as the caller at call
time:

- **the installations catalog** — `catalog/installations.yaml` in the registry repository
  (`registry.repository` / `--registry-repository`, `giantswarm/github` by default): a Backstage
  catalog file with one `Resource` of `spec.type: installation` per installation. The manager reads
  its name, the labels `giantswarm.io/customer`, `giantswarm.io/provider`, `giantswarm.io/pipeline`
  and `giantswarm.io/region`, the annotations `giantswarm.io/base` (the base domain is
  `<name>.<base>`) and `giantswarm.io/account-engineer`, and the links of type `CCR` (the
  `<customer>-configs` repository) and `CMC` (the `<customer>-management-clusters` repository). An
  installation whose entry names no repositories is listed and read no further.
- **the Dev Portal's app-config** — `management-clusters/<hub>/extras/backstage/backstage/app-config.yaml`
  in the hub's management-clusters repository, as the catalog names it: the `gs.installations` block
  (base domain, providers, auth provider). The hub is `hub` / `--hub`, the installation this manager
  runs on.

Because the catalog lives in a repository of its own, **the GitHub App `giantswarm-platform-manager`
must be installed on the registry repository with contents read** — next to the customer
`-configs`/`-management-clusters` pairs, the hub pair and `teleport-fleet` — and the caller must be
able to read it. Without that, `list_installations` refuses with the requirement; the manager never
steps in with a token of its own.

Per installation the manager then reads, as the caller:

- the **facts on record** in `installations/<name>/config.yaml.patch` of its configs repository —
  the meta chart line (`agentPlatform.kagentApiV2` selects `4`; a fresh enable of an organisation the
  policy grants a line-4 component writes it there), `services.muster.clientId` — which,
  with the registry's name, base domain, customer and provider, the cluster App on record in
  `management-clusters/<name>/cluster-app-manifests.yaml` (whether the cluster serves
  `PodCertificateRequest`, the 4 line's prerequisite: the feature gates in its values, or its chart's
  default — the chart at the App's version, or at the version the release its values name lists for
  it in `giantswarm/releases`) and the portals' app-configs (which portals sign people in, whose broker exchanges tokens
  into it, whether a portal reaches it through the tunnel: `private`), are the definitions'
  `installation.*` inputs: read, never typed;
- the **enabled marker** of each capability: for `agent-platform`,
  `installations/<name>/apps/agent-platform/configmap-values.yaml.patch` in its configs repository;
  for `customer-portal`, the portal's `management-clusters/<name>/extras/backstage/backstage/app-config.yaml`
  in its management-clusters repository.

**How the repositories are read.** Every read runs with the caller's token, and the caller's GitHub
quota — 5,000 requests per hour, shared with every other client of their account — pays for it. So a
call reads each repository it touches as one recursive Git tree at `HEAD`, fetched with a conditional
request: GitHub answers `304 Not Modified` to an unchanged listing, which costs the quota nothing and
still is the permission check, since a repository the caller may not see is `404` whatever the cache
holds. A file's existence — the markers — is answered from the tree; its content is read as a blob by
SHA and kept in a cache shared across calls and callers, a blob's SHA being its content, so an
unchanged file is fetched once and a repository that changed costs its listing and its changed blobs.
A caller's validation of a tree stands for a few seconds (the burst a portal page fires: a list and
the verifies of one installation), and a merge through `merge_action` ends it for the repository merged
into. Against a warm cache a list or a verify costs the caller no request at all; a call GitHub refuses
for a spent quota answers the reset time. The reads that stay one request each, as the caller: the
bearer's verification (`GET /user`, cached a quarter of an hour), and the pull request, branch and
merge calls of the commit, the approval and the record's resync.

Per capability the answer carries `enabled` — the marker is on record, whoever put it there: the
manager, or the installation's people by hand before the manager existed — and the state as one word:
*not enabled* (marker absent) or *enabled* (marker present). *Pending approval*, *rolling out*, *waiting for the customer*,
*drifted* and *failed* come from the Action record and the last verify once those exist; the answer's
`states` block separates the two groups. A *removed* action lets the files' state stand: *not
enabled*, with `lastAction.result: removed`. An installation whose repositories the caller cannot
read is *unknown* and listed under `unreadable`, with the reason.

## Configuration

Flags, each with an environment variable (`--listen` / `LISTEN`, `--mcp-path` / `MCP_PATH`,
`--github-api-url` / `GITHUB_API_URL`, `--enable-oauth` / `OAUTH_ENABLED`, `--oauth-base-url` /
`OAUTH_BASE_URL`, `--oauth-authorization-server` / `OAUTH_AUTHORIZATION_SERVER`, `--approvals-url` /
`APPROVALS_URL`, `--approvals-channel` / `APPROVALS_CHANNEL`, `--approvals-standup-channel` /
`APPROVALS_STANDUP_CHANNEL`, `--registry-repository` / `REGISTRY_REPOSITORY`,
`--registry-path` / `REGISTRY_PATH`, `--hub` / `HUB_INSTALLATION`); `giantswarm-platform-manager -h` lists
them. The chart in [`helm/giantswarm-platform-manager`](helm/giantswarm-platform-manager/README.md)
sets them from its values.

## platformctl

`platformctl` is the laptop and CI surface, built from `cmd/platformctl` in this repository and attached
to every release as `platformctl-<os>-<arch>` (Linux, macOS and Windows on amd64 and arm64, each with its
cosign bundle) next to the image and the chart. It has no logic of its own:

- `platformctl template <shape> --inputs <file> [--out <dir>]` renders a capability's fileset locally by
  importing the [render library](render/README.md) — no token, no network. The inputs document carries
  `input` (the definition's inputs) and `secrets` (the values you supply); it is the document of the golden
  filesets, and the output is their tree — `<owner>/<repo>/<path>` per file, `includes.txt` with the shared
  kustomization entries — so `template` reproduces the goldens byte for byte. Shapes: `agent-platform`.
- `platformctl installation list [<installation>…] [--customer <name>]`,
  `platformctl installation enable <installation> <capability> --dry-run|--commit --reason <text> [--input k=v]… [--secret f=src]… [--rotate <name>]… [--content]`,
  `platformctl installation reconcile <installation>…|--all <capability> --dry-run|--commit --reason <text> [--input k=v]… [--secret f=src]… [--rotate <name>]… [--content]`,
  `platformctl installation disable <installation> <capability> --dry-run|--commit --reason <text> [--keep <path>]… [--content]`,
  `platformctl installation verify <installation> <capability>`,
  `platformctl action get <name>`, `platformctl action list [--installation <name>] [--capability <name>]`,
  `platformctl action approve <name>`, `platformctl action deny <name> --reason <text>`,
  `platformctl action merge <name>` and `platformctl action watch <name>` call the manager's tools and
  format the answers for a terminal;
  `--output json` prints the manager's answer as it is, for CI. `--input kagent.enabled=true` nests dotted
  keys into the tool's `inputs`; a value that parses as JSON is that value, anything else a string.
  `--dry-run` is the tool's `dryRun`; `--commit --reason <text>` its `mode: commit` with the `reason` — the
  pull requests opened as you, the Team review asked: for one installation the action, for `reconcile` over a set — two or more installations
  named (the tool's `installations`), or `--all` for every installation of the registry — the wave over the
  set, one action rolled out a stage per merge. `installation verify` calls `verify_capability` and then
  `verify_installation` on the one registration and prints the two as one result — per dimension the side
  that checked it; when `verify_installation` does not answer (not served, no identity forwarded, not
  connected), the repository result stands and the live line says why. `--secret <field>=@<file>`, `<field>=env:<NAME>` or
  `<field>=-` (stdin, one field) supplies a secret the plan's `suppliedSecrets` name — the fields whose file
  the commit creates or rewrites; a field whose encrypted files stand on record is listed as on record and
  needs no value, the value there stands; the value is sent
  once in the call's `secrets`, never printed, and never taken from the command line — a value typed there
  is refused naming only the field. `--rotate <name>` (repeatable) is the tool's `rotate`: the dry run
  prints each value *rotates on request*, apart from the rotations a file forced. `verify` prints the definition's features with their marks and
  dimensions; `approve`, `deny` and `merge` are the review's tools called as you, the manager's answer
  saying what follows (`deny` on a merged action is its actor's withdrawal); `watch` is `watch_action` — the rollout picture object by
  object, the dimensions that decided, the report, what follows — called again while it is rolling out.
- The calls go through `muster agent --mcp-server`, muster's own bridge: it takes the aggregator from
  muster's configuration (`--endpoint` names another) and signs you in to muster when needed. The bridge
  exposes muster's meta tools only, so every manager tool is called through its `call_tool` and the
  answer read out of the document `call_tool` returns. A manager you have not connected yet answers with
  its sign-in URL and exit code 3; `muster auth login --server giantswarm-platform-manager` is the same
  sign-in.
- `platformctl installation dex-split <installation> --configs <checkout> --management-clusters <checkout>
  [--vault sops|beekeeper] [--hub <mc> --hub-management-clusters <checkout>] [--write]` is the laptop
  side of the encrypted-list gate (`DexSecretRefusal`), and it calls no muster. An installation whose encrypted
  `installations/<mc>/apps/dex-app/secret-values.yaml.patch` carries `oidc.extraStaticClients`, the
  authenticator's `trustedPeers` or a built-in client's inline `clientSecret` is moved into the shape the
  definitions render, in two changes merged in order with Dex checked between them:
  1. **management-clusters:** one SOPS Secret per inline secret (namespace `giantswarm`, key `secret`)
     at the definitions' path, listed in its kustomization. The paths are
     `extras/agent-platform/secrets/` for muster, kagent and `muster-*`, `extras/mcp-<x>/` for `mcp<X>`,
     and the portal's `extras/backstage/backstage/dex-client-backstage-secret.enc.yaml`. Any other
     client goes to `extras/dex/`, which the split lists in the extras kustomization. A hub's
     token-exchange client (`muster-token-exchange-<mc>[-<hub>]`) goes under
     `extras/agent-platform/secrets/` whether or not the platform's directory is on the installation:
     cluster-mcp-servers renders the pair's Dex side there, the file the agent-platform definition
     renders for the same pairing, and the split writes the kustomization chain to it (the secrets
     kustomization listing the Secret, the directory's listing `./secrets`, the extras' listing
     `./agent-platform/`), so a reconcile after the split reads the pair's value as kept. A client
     an earlier split put under `extras/dex/` moves on to that path, unchanged, and is unlisted there.
  2. **configs:** after `flux-extras` has applied those Secrets. The plaintext `configmap-values.yaml.patch`
     lists every client with `clientSecretRef`/`secretRef` and the `trustedPeers`. The encrypted patch
     loses the lists and the moved secrets and keeps the rest, such as the authenticator's own secret
     and the login connectors.

  Ids, names, redirect URIs, `public` flags, peers and values never change. Without `--write` the
  command is a dry run, and it prints only key paths, files and the configuration fields the plaintext
  patch carries anyway:
  - per client: the source key path, the destination file and Secret, and the plaintext entry
  - what the encrypted patch drops and keeps
  - the token-exchange pairing
  - the files per repository

  A split installation reports nothing to do, and a re-run after `--write` changes nothing. A run
  that stopped half-way is completed by the next: a Secret already present with the same value stays,
  one with another value is refused. dex-app older than 3.2.2 is refused, naming the pin to add to the
  collections kustomization. `--hub` and `--hub-management-clusters` compare the
  `muster-token-exchange-<mc>` value with the hub's `<mc>-token-exchange-credentials`, which must stay
  one value; `--write` refuses when they differ.

  `--vault sops` (the default, a person) runs the local sops with the person's age identity.
  `--vault beekeeper` (an agent, beekeeper 0.114.0 or newer) moves each value with `beekeeper secret
  copy`, compares with `beekeeper secret compare` and drops the moved keys with `beekeeper secret
  unset`, so no secret reaches the agent. The configuration fields come from `beekeeper secret
  reveal`, which refuses the call when its classifier takes a leaf for a secret.

Installing it and keeping it current:

- Download `platformctl-<os>-<arch>` from a [release](https://github.com/giantswarm/giantswarm-platform-manager/releases),
  or `go install github.com/giantswarm/giantswarm-platform-manager/cmd/platformctl@latest`.
- `platformctl self-update` installs the latest release over the running binary once its cosign bundle
  verifies as a CircleCI build of this repository (the Sigstore public-good trust root, the CircleCI
  issuer, this repository as the source). A release without a bundle, or a download that does not match
  its signature, is refused and the binary stays as it is. `--check` only reports both versions and exits
  with 125 when a newer release is out. A build without a release version (`platformctl version` says
  `dev`) is refused: install a release or use `go install`.
- Every command but `version`, `self-update`, `help` and `completion` prints one line on stderr while a
  newer release is out. GitHub's answer is cached for an hour under the user's cache directory; the
  look-up gives up after 2 s and never fails the command. `PLATFORMCTL_NO_UPDATE_CHECK=1` silences it.
- `platformctl completion zsh|bash|fish|powershell` prints the shell's completion script: the
  subcommands, the flags and the capability names (from the registry built into the binary, no network).
  For zsh: `platformctl completion zsh > "${fpath[1]}/_platformctl"`, then open a new shell.
- Exit codes: 0 done, 1 the tool refused or the call failed, 2 usage, 3 sign-in required, 125 a newer
  release (`self-update --check`).

## Render library

`render/` turns an installation's capability inputs into the files of its GitOps repositories, with no I/O of its own; see [render/README.md](render/README.md).

## Development

- `make test` — the Go tests, among them the scenarios in `internal/e2e` against a fake GitHub: the
  identity chain (no bearer is a bare 401, a refused bearer is `invalid_token`, `get_info` names the
  person, the bearer is verified once per token, the framework refuses `mode: apply`) and
  `list_installations` over an invented registry — an installation in each state the repositories
  can show, one whose repositories the caller may not read, one the portal alone knows, the markers
  read on every call, the filters, a registry the caller cannot read, a call without a caller.
- `make scenario-test` — the muster half in muster's own scenario harness (`tests/scenarios`, needs
  the `muster` binary on `PATH`): a mock authorization server standing in for GitHub as the App, the
  consent once, the token on every call, a second session adopting the grant, a person without a
  grant told where to sign in. CI runs both in the `scenario-test` job (`.circleci/custom.yml`).
- `make helm-lint`, `make helm-template`, `make helm-schema` — the chart with its default, platform
  (`tests/oauth-values.yaml`) and lab (`tests/lab-oauth-values.yaml`) values.
- `make docker-build` — a local image; CI builds and publishes the multi-arch image and the chart on
  every tag.
- `make build-platformctl` — the CLI for this machine; CI cross-compiles it and attaches the binaries to
  the release (`go-build-platformctl` and `upload-platformctl` in `.circleci/custom.yml`).

## Proving a release candidate

A merge to `main` is tagged as a release candidate (`vX.Y.Z-rc.N`, a GitHub pre-release with the image,
the chart and `platformctl-<os>-<arch>`) and promoted to the stable `vX.Y.Z`. The hub's manager follows
stable releases only (its OCIRepository's semver `>=0.17.0 <1.0.0`), so no running manager dry-runs a
candidate before its promote. The candidate's proof is offline:

1. **The candidate's own CLI.** Build it at the tag (`git checkout vX.Y.Z-rc.N && make build-platformctl`),
   or download `platformctl-<os>-<arch>` from the pre-release. `go install …@<tag>` does not build: the
   module's `replace` directives refuse it.
2. **The offline render of the management-clusters declarations.** Every agent-platform golden fileset is
   rendered with that CLI and compared with the golden tree, byte for byte — no token, no network:

   ```sh
   for d in render/agentplatform/testdata/*/; do
     [ -f "$d/input.yaml" ] || continue
     ./platformctl template agent-platform --inputs "$d/input.yaml" --out "out/$(basename "$d")"
     diff -r "out/$(basename "$d")" "$d/golden"
   done
   ```

3. **The unit and golden tests** at the tag: `make test` (the render goldens of every definition among
   them; `go test ./render/... -update` is never part of a proof), and `make test-chart` where `helm` is on
   `PATH`.
4. **The live dry run, where a manager follows candidates.** Where a manager serves the candidate (a
   manager pinned to the RC's chart, a local lab), `platformctl installation reconcile <installation>
   <capability> --dry-run` against it answers with no `commitRefused` and only the changes the candidate
   intends.

Proven means: every diff in step 2 is empty, `make test` passes, and a dry run of step 4, where one ran,
shows only the intended changes. Once that proof is green, the promote runs as routine
(`devctl release promote giantswarm/giantswarm-platform-manager`); no one is asked for it.

No test installation's manager follows candidates today: the test installations' managers follow the same
stable range as the hub and are claimed by other proving windows, so pointing one at the RC line would take
those windows' manager from under them. An RC lane is a GitOps change of its own (an OCIRepository with a
prerelease semver on one test installation's manager) and gets its own issue when it is wanted; until
then the offline proof above is this repository's RC proof.

Public repository: every fixture uses invented installation names and placeholder values.
