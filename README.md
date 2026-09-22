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
| `list_installations` | read | Every installation of the registry with, per capability, its state, the inputs on record and the last action, every read as you at call time. `installations` (names) and `customer` narrow the answer; `summary: true` answers the states and the last actions alone, without the record, the portals and the federation facts — the overview's call. Every read of a call runs at once, 32 in flight at most. |
| `enable_capability` | write | Enable a capability on one installation (`installation`) or a set (`installations`): with `dryRun: true` the plan — files per repository with the change each one is against the repository now, pull requests in dependency order, generated secrets by name, Dex clients and redirect URIs, the secrets the person supplies at commit (by field), customer actions, probes. `inputs` are the definition's typed inputs over the facts on record. With `mode: "commit"` and one `installation`: the gate, the Action in *pending approval*, the pull requests as the person (see [The commit](#the-commit)); `secrets` carries the supplied values by field. |
| `reconcile_capability` | write | The same render over a set (empty: every installation of the registry), every file compared with the repository: all *unchanged* is an empty diff. Installations without the capability on record are listed as *skipped*: a wave reconciles what is on record; a fresh enable is `enable_capability` with one installation. |
| `get_action`, `list_actions` | read | The Action records on the hub: one per enablement or reconcile a person commits — actor, installations, capability, inputs, pull requests, approval, rollout, probes, result. The record follows GitHub on every read, as you (at most once a minute per action): a pull request merged outside `merge_action` is recorded *merged* with its commit, time and `mergedBy`, and the action rolls out as after the merge; one closed unmerged fails it; a fileset gone from the default branch again moves it to *removed*, naming the objects left on the installation. See [The Action record](#the-action-record). |
| `verify_capability` | read | One installation against a capability's definition, grouped into the definition's features with one mark each — *as defined*, *planned*, *differs by input*, *drifted* — and expanded to its dimensions: the owning repositories' files, read as the person, against the render from the inputs on record (every difference names the file, the path and the input that drives it, the planned change it is — a key the capability's `removals.yaml` names — or drift), and the definition's anonymous HTTP probes. The live dimensions read *not checked* here: they are `verify_installation`'s. |
| `verify_installation` | read, **live registration** | The same installation's running objects against the definition's probes — HelmReleases Ready, workloads Available, Secrets and MCPServer objects present, conditions, logs, the live values against the render — read through muster's kubernetes tools **as the person**, with the ID token muster forwards to the second registration `giantswarm-platform-manager-live` (`muster.liveServer`). What the person may read decides what is checked: an object they may not read is *not checked, forbidden for them*, an installation they are not connected to answers with muster's own sign-in. The result is recorded on the installation's newest action and feeds `list_installations`: *drifted*, or *waiting for the customer* when the only red dimension is the one the customer's action holds up. A portal or `platformctl` shows the two verifies as one result. |
| `watch_action` | **live registration** | The rollout watch of an action whose pull requests are merged, **as the person calling** — the manager holds no token beyond a call, so the watch is a call (the portal's page, `platformctl action watch`, an agent), never a loop. Reads the Flux objects the definition names on the installation rolling out (the HelmReleases with their Ready condition and revision) through muster's kubernetes tools and answers the picture; once every one is Ready it runs the definition's probes — the live dimensions as the person, the anonymous HTTP probes direct — and the stage moves to *enabled*, *waiting for the customer* (the customer's own action is the only thing open) or *failed* (a probe is red, named). The report — pull requests, rollout per object, each probe, the open customer actions — goes into the review's thread and onto the Action (`status.rollout.installations[]`, `status.probes`, `status.result`). Nothing is waited for or hurried: call again while it is rolling out. Anyone signed in may watch; the reads are theirs, and the state follows the picture whoever read it. An action *waiting for the customer* or *enabled* is re-read: the customer's action done flips it to *enabled*. The live path carries no GitHub token, so the watch first has the App-pinned registration re-read the action as you through muster (`get_action`, muster putting your GitHub token on it): an action whose pull requests were merged outside the manager is watched all the same. |

## The commit

`mode: "commit"` of the two write tools takes **one** `installation` (a set is the dry run's) and runs, in
this order, writing nothing before the gate:

1. **The gate** — the installation is on record readably as the person: its repositories known to the registry
   and readable as you, read at call time. Otherwise the commit is refused naming the installation and the
   reason, and the refusal is recorded as an Action in state *refused* (the installation's state read from its
   repositories stands).
2. **The plan**, as the dry run renders it; a definition's refusal, a file that could not be compared as the
   person, a generated value frozen where no rotation is possible (below), or a supplied secret left out of
   `secrets` (or one the plan does not ask for) refuses the commit before any write. Every file on record
   already: nothing to commit, no Action.
3. **The Action** — created in *pending approval* with the actor, the capability, the installation and the
   inputs (never a secret value: `secrets` is its own argument and lands nowhere but the encrypted files).
4. **The files**: the plan rendered again with the supplied values; a plain file must be byte-identical to the
   plan (a value never lands outside a secret file), the secret files get their generated values and are
   encrypted with gitops-commit's `sopsenc` for the recipients of the repository's `.sops.yaml` (read as the
   person; a repository without one refuses the commit). A secret file on record is **kept** as long as the
   render changes nothing outside its values: the manager decrypts nothing, so the two are compared as YAML
   with the values the record holds encrypted (the fields under the repository's `encrypted_regex`) and the
   values the commit fills in left out — same keys, same metadata, same plaintext fields → `unchanged`, the
   values in it *frozen* and its generated names `kept`; nothing is written. A reconcile of an installation
   the manager enabled therefore rewrites no secret and rotates nothing. A name **rotates** only when a file
   of the name has to be written — a file to create (a server's Dex client Secret next to its existing
   credentials file), an existing file whose plaintext skeleton the render changes (a field added to its
   template), or a plain file to write carrying a key pair's public half: one new value is drawn and written
   into every file that holds it, the kept files rewritten and encrypted anew — and every other value a
   rewritten file holds rotates with it, down to the files sharing those (the server's Valkey password into
   its Valkey Secret). The dry run says so (`generatedSecrets[].frozenIn`, `kept`, `rotates` with `forcedBy`,
   the file that forced it), the Action records the rotated names (`status.rotated`), the pull request names
   them. For the running installation a rotation means both sides roll: the server and the Dex client take
   the new value with their Secrets, and the client is unusable between the two rollouts. Unseen by the
   comparison: a literal the render changes under an encrypted field — the record holds it encrypted. A
   value frozen in a file the definition does not own whole (one with several owners) cannot rotate and
   refuses the commit naming the file.
5. **The pull requests** through gitops-commit, as the person, in dependency order (configs before
   management-clusters), one commit per repository, on branch `platform/<action>/<installation>`, titled in
   conventional-commit form — `feat(<installation>): enable <capability> (<action>)`, `fix(<installation>):
   reconcile <capability> (<action>)` — so the repositories' semantic-pull-request check passes as opened, the
   action id in the title and body. The Action records them and stays in *pending approval*: the approval, the merge
   and the rollout follow. A failure on the way moves the Action to *failed* and closes the pull requests
   opened so far as the person, branches deleted, recorded *closed* with the reason on the Action; one the
   remote refused to close stays open on the record, and `deny_action` — which takes a failed action too —
   closes it, records the reason and leaves the action failed.

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
(*create*, *update*, *unchanged*, *unknown* when the current file could not be read as the person), the
shared-kustomization includes, the generated secrets by name, kind and length — with `frozenIn`, the files
on record that hold the value already, `kept` when the value on record stands and no file of the name is
written, and `rotates` with `forcedBy`, the file that has to be written, when the commit draws a new value
into every file of the name (see [The commit](#the-commit)) — the secret values the person
supplies at commit by field (rendered as `SUPPLIED(<field>)` markers — no secret value ever appears in a dry
run), the Dex clients with their redirect URIs from the rendered dex patch, the customer actions (Secrets the
definition references and never renders) and the probes (the definition's live dimensions). Over the set:
the pull requests, one per repository in dependency order — an installation's configs before its
management-clusters, the hub's pair after, `teleport-fleet` last — with the files, changes and generated
secrets each carries; the wave's `order` (Giant Swarm's own test installations, the hub, then the
customers' installations); and `skipped` with the reason (*not enabled*, *unreadable*, *no repositories on
record*): a wave reconciles what is on record and never enables. An installation named as `installation` is
rendered whether or not its fileset is on record — a fresh enable, or the changes to it — with `commitRefused`
saying why a commit would be refused. `order` names another
rollout order for the set (every rendered installation exactly once).

`reconcile_capability` with `mode: "commit"` over a set is **one wave**: one Action, one review listing the
targets in the rollout order and the skipped, the pull requests per installation on their own branches.
`merge_action` (the actor's) advances it one stage per call — merge the first installation's pull requests
once green; `watch_action` (the live registration, anyone's) carries the installation rolling out to
*enabled*; only then does the next `merge_action` take the next installation's pull requests — and a red
probe stops it: that installation *failed*, the stages after it not started, their pull requests open, the
result naming where and why. A stage *waiting for the customer* holds the wave too: `merge_action` says
so, and the customer's action done flips the stage on the next watch. A wave carries no supplied secret
values; an installation whose secret files are not on record is enabled alone.

## The Action record

Every enablement or reconcile a person commits is an `Action` — `platform-manager.giantswarm.io/v1alpha1`,
namespaced, on the hub in the manager's namespace, read and written with the manager's own ServiceAccount:
the record is the manager's, not the person's. `spec` is written once (`actor`, `capability`, `kind`
enable|reconcile, `installations` in the wave's order, `inputs`, `markers` — per installation the
definition's enabled marker in the installation's repository); `status` is a subresource (`state`,
`pullRequests`, `approval`, `rollout`, `probes`, `result`, `syncedAt`/`syncedBy`, `orphans`). `get_action`
and `list_actions` read it; `mode: "commit"` creates it and moves its state;
`list_installations` carries the newest Action of an installation and capability as `lastAction`, and an
unfinished or failed action's state stands over the state read from the files.

The states: *pending approval*, *rolling out*, *waiting for the customer*, *enabled*, *drifted* and *failed*
are the installation's states an action produces; *refused* (the gate refused it before any write),
*denied* (a member withdrew it) and *removed* (below) are the action's own, and the installation's state
read from its repositories stands. A pull request is *open*, *merged* (with `mergeCommit`, `mergedAt` and
`mergedBy`) or *closed*.

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
  the meta chart line (`agentPlatform.kagentApiV2` selects `4`), `services.muster.clientId` — which,
  with the registry's name, base domain, customer and provider, the cluster App on record in
  `management-clusters/<name>/cluster-app-manifests.yaml` (whether the cluster serves
  `PodCertificateRequest`, the 4 line's prerequisite: the feature gates in its values, or its chart's
  default) and the portals' app-configs (which portals sign people in, whose broker exchanges tokens
  into it, whether a portal reaches it through the tunnel: `private`), are the definitions'
  `installation.*` inputs: read, never typed;
- the **enabled marker** of each capability: for `agent-platform`,
  `installations/<name>/apps/agent-platform/configmap-values.yaml.patch` in its configs repository;
  for `customer-portal`, the portal's `management-clusters/<name>/extras/backstage/backstage/app-config.yaml`
  in its management-clusters repository.

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
`APPROVALS_URL`, `--approvals-channel` / `APPROVALS_CHANNEL`, `--registry-repository` / `REGISTRY_REPOSITORY`,
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
  `platformctl installation enable <installation> <capability> --dry-run|--commit [--input k=v]… [--secret f=src]… [--content]`,
  `platformctl installation reconcile <installation>|--all <capability> --dry-run|--commit [--input k=v]… [--secret f=src]… [--content]`,
  `platformctl installation verify <installation> <capability>`,
  `platformctl action get <name>`, `platformctl action list [--installation <name>] [--capability <name>]`,
  `platformctl action approve <name>`, `platformctl action deny <name> --reason <text>`,
  `platformctl action merge <name>` and `platformctl action watch <name>` call the manager's tools and
  format the answers for a terminal;
  `--output json` prints the manager's answer as it is, for CI. `--input kagent.enabled=true` nests dotted
  keys into the tool's `inputs`; a value that parses as JSON is that value, anything else a string.
  `--dry-run` is the tool's `dryRun`; `--commit` its `mode: commit` — the pull requests opened as you, the
  Team review asked: for one installation the action, for `reconcile --all` the wave over the set, one
  action rolled out a stage per merge. `installation verify` calls `verify_capability` on the App-pinned
  registration and `verify_installation` on the live one and prints the two as one result — per dimension
  the side that checked it; when the live registration does not answer (not registered, not connected),
  the repository result stands and the live line says why. `--secret <field>=@<file>`, `<field>=env:<NAME>` or
  `<field>=-` (stdin, one field) supplies a secret the plan's `suppliedSecrets` name; the value is sent
  once in the call's `secrets`, never printed, and never taken from the command line — a value typed there
  is refused naming only the field. `verify` prints the definition's features with their marks and
  dimensions; `approve`, `deny` and `merge` are the review's tools called as you, the manager's answer
  saying what follows; `watch` is `watch_action` on the live registration — the rollout picture object by
  object, the dimensions that decided, the report, what follows — called again while it is rolling out.
- The calls go through `muster agent --mcp-server`, muster's own bridge: it takes the aggregator from
  muster's configuration (`--endpoint` names another) and signs you in to muster when needed. The bridge
  exposes muster's meta tools only, so every manager tool is called through its `call_tool` and the
  answer read out of the document `call_tool` returns. A manager you have not connected yet answers with
  its sign-in URL and exit code 3; `muster auth login --server giantswarm-platform-manager` is the same
  sign-in.

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

Public repository: every fixture uses invented installation names and placeholder values.
