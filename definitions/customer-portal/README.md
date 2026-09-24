# customer-portal definition

The content of the `customer-portal` capability definition: its closed input set, the keys the renderer
drops, the consistency features and the anonymous probes. Data only; the render library reads it.

| File | What it is |
|---|---|
| `schema.json` | JSON Schema (draft 2020-12, `additionalProperties: false`) of the inputs. Every leaf input carries `x-source` (registry, person or generated), `x-feature` and `x-renders`: the key paths of the fileset it produces. `x-files` names the repository file each fileset key is read back from; a person input carries `x-readback` (file; key, one path or a list the first on record answers from, a list step an index or a `[key=value]` selector; kind `value`, `present`, `host`, `installation` — the installation whose base domain the URL's host is under the service label `prefix` names — or `file`; prefix) where the record can answer it, and says why where it cannot. A default is declared only where every portal agrees. |
| `removals.yaml` | Keys a portal installation carries today that no input renders, each with the reason it is dropped and its kind: the agent-platform definition renders it as its Component (`other-definition`: every such key is one that definition renders, and the portal's app-config keeps it until the Component's fragment owns the portal's lists), or it is a section of the hub's Dev Portal that no shape renders yet (`hub`: a commit that would remove one with a value is refused). |
| `migrations.yaml` | Keys the definition renders that a portal enabled before a migration lacks, each with the migration that adds it: the portal's Dex client Secret, its user secrets and its plugin keys as SOPS-encrypted files of its directory with their kustomization entries, and the `type` every rendered Secret states (M33), the agent-platform definition's Component in its kustomization (M3). A leaf the record lacks under one of them is a planned change, not drift. |
| `features.yaml` | The consistency features and the dimensions each one rolls up, one mark per feature, every dimension exactly once. A file dimension's key names the files and YAML paths it observes, in the grammar the file's header documents; the comparison routes every difference to the dimension that names it most specifically, to the kind's dimension marked `catchAll: true` when none does, and reports what no dimension names under the feature `other`. The `kind: live` dimensions are the definition's probes of the running portal: the render carries one or more probes per live dimension as data (`Result.Probes`), and the verify slice executes them. |
| `probes.yaml` | The anonymous HTTP probes: the home page, the start of the sign-in, Dex's answer to the portal's client. Templates over the installation's base domain and codename and the portal's domain. |

## The fileset

The definition renders the portal into the installation's management-clusters repository, under
`management-clusters/<name>/extras/backstage/` (`backstage:kustomization:`, `backstage:app-config:`,
`backstage:user-values:`, `backstage:file:`): the kustomization over the fleet's backstage base with the
portal's directory and, with the platform enabled, the agent-platform definition's Component; the portal's
directory over the fleet's main base with the chart's release range and the HelmRelease's values sources
patched in, the app-config and values ConfigMaps (the values carry the portal's route and its environment,
`backstage.extraEnvVars`), the Secrets `user-secrets-backstage`,
`plugin-keys-backstage`, `github-app-credentials-backstage` (github on) and `dex-client-backstage`, and the
tunnel's SPIFFE bundle reference (tunnel on). On an installation without the platform it also renders the
portal's Dex client into `installations/<name>/apps/dex-app/configmap-values.yaml.patch` of the configs
repository (`dex-configmap:`).

Key paths in `x-renders`, `removals.yaml` and `migrations.yaml` are normalised: a list index is `[*]`, a list entry
`[x]` by its identity, a per-installation map key is `<name>`. A path covers every key beneath it.

## Rules the inputs encode

- The installation facts (`installation.*`) are read, never typed: the codename, base domain, provider, region,
  pipeline and whether the agent platform is enabled come from the installations registry; a federated
  installation's entry (`federation.installations[*]`) carries the same kind of facts, whether it runs the agent
  platform among them. Every hostname of the fileset other than the portal's own derives from them; the
  portal's is `portal.domain`.
- The dex-app configmap patch of an installation is one file with several owners: a definition owns the
  clients it renders and nothing else. Without the platform this definition renders the patch with the portal's
  client. With the platform enabled (`installation.agentPlatform`) the agent-platform definition renders it and
  carries the portal's client through its `portal.domain` input; both build the entry with
  `render.PortalDexClient`, so the entry is the same byte for byte whichever definition writes it. The plan
  keeps every part of the installation's current patch that no definition owns — its login connectors under
  `oidc.customer`, Dex's `ingress` tuning, a built-in or extra static client of another owner — after the
  definition's, and names it as kept. The client's Secret `dex-client-backstage` in Dex's namespace is this
  definition's in either case, the file `dex-client-backstage-secret.enc.yaml` in the portal's directory; the
  agent-platform definition references it by name in its patch entry and renders no file for it, so on an
  installation with both capabilities no repository path and no Kubernetes object is rendered by two definitions.
  The plaintext patch is authoritative only where the installation's encrypted Dex values (`secret-values.yaml.patch`) carry no
  `oidc.extraStaticClients` and no `dexK8SAuthenticator.trustedPeers` list: the values merge takes a list whole from the encrypted side,
  so the portal's client rendered into the plaintext list would never reach Dex. The record reads which of those lists the encrypted
  file carries (its keys are plaintext; nothing is decrypted) and the commit is held while one of them is — `commitRefused` names the
  file, the lists and the shadowed clients; the entries are carried over by hand first, then the lists dropped from the encrypted values.
- A portal over several installations of one customer lists them under `federation.installations` with their
  facts on record: each gets a cluster entry and an installation entry, and a provider on its Dex whose client
  credentials are supplied at commit (`federation.<name>.clientId`, `clientSecret`); one that runs the agent
  platform (`agentPlatform`) has its avatars host in the portal's environment (below).
  `federation.signInInstallation` names the installation whose Dex signs people in; `federation.tokenBroker` the
  one whose muster brokers cluster tokens for the others — then the portal has the sign-in installation's
  provider alone, the broker's client credentials are supplied as `federation.tokenBroker.clientId`,
  `clientSecret`, and every other installation's entry carries `clusterTokenAudience`. An installation's entry lists `installation.providers` where it runs clusters on more
  than its own provider. The set follows the portal's record and is never typed outside a dry run: every `gs.installations` entry
  but the host, by name, each with the registry's base domain, region and pipeline (the record's where the registry
  has none) and the providers the record lists, whether the agent platform is on record there read from its marker;
  an entry the registry does not know refuses the comparison naming it. An installation the portal reaches through the hub's tunnel
  (`private`, read from its cluster entry on record) has its cluster entry at the tunnel Service,
  `https://kubernetes-<name>.agent-platform.svc.cluster.local:8443` with `skipTLSVerify`, instead of its public API.
- Credentials are generated by the engine and written as SOPS-encrypted Secrets the chart takes its values
  from: the session secret, the Dex client secret (one value, shared with the Dex client's Secret), the
  telemetry salt and the plugin-to-plugin signing key pair. What a person supplies at commit is named by
  field: the GitHub App's `plugins.github.appId`, `clientId`, `clientSecret`, `privateKey` and `webhookSecret` (the id
  is no credential, but it lives only in the encrypted file, so it is supplied like them and never set in the
  inputs), Sentry's `plugins.sentry.appDsn`, `backendDsn` and `reportUri`, and the service-account token of the
  installation's Grafana, `plugins.grafana.token`, where the Grafana plugin is wired.
- The Grafana plugin links the installation's own Grafana, `https://grafana.<base domain>`. The `grafana:` section
  is always rendered with that one host under the installation's name: the plugin's config schema requires the
  section, and a portal without it does not start. `plugins.grafana.enabled` says whether the plugin is wired —
  the proxy entry `/grafana/api` against the installation's Grafana with the supplied token's Secret value — and
  reads back from the proxy entry's presence. The host is a registry input (`plugins.grafana.domain`), derived
  from the base domain and never typed; its read-back shows the Grafana a portal names on record next to the
  installation's, and a portal that names another one is off the definition at the host.
- Every leaf of `user-secrets-backstage` is base64, encoded exactly once: the chart copies `authSessionSecret`,
  `telemetrydeck.salt`, each `dexAuthCredentials` entry's `clientID` and `clientSecret`, the three sentry values
  (`app.dsn`, `backend.dsn`, `reportURI`) and `grafana.apiToken` under the `data:` of its Secrets
  (`<name>-secrets`, `<name>-dex-auth-credentials-secret`) as they are, and the pod loads them with `envFrom`,
  so each value has to be the base64 of what the portal reads. The generated ones — the session secret, the
  telemetry salt and the portal's own client secret (the Dex client's Secret carries the same secret raw) — sit
  at their base64 placeholders, which the commit fills with the value's base64; the portal's own client id
  (`backstage`), a federated installation's and the broker's credentials, the Sentry values and the Grafana
  token are encoded by the render (`render.Base64Leaf`), the supplied ones supplied raw. The GitHub App's values
  and the plugin keys land in the chart's `stringData:` and stay as they are. The render-consumption test renders
  the chart with every shape's committed values and holds every key under `data:` to the value generated or
  supplied, decoded as valid UTF-8.
- The portal's agent-platform section is the agent-platform definition's: its fragment, values and Google
  credentials are files of its own Component next to the portal's, listed by this definition's kustomization.
  The portal includes the shared extensions list without the platform's section, with the Grafana dashboards card
  where the plugin is wired (`#extensionsGrafanaDashboards`: the card is disabled in the app until a portal opts in
  through its list); the Component's fragment includes the platform's pair over it, reading the wiring off the
  record. That holds once the fragment on record owns the portal's lists (`platformSection.componentLists`: it
  carries `app.extensions`), which it does where the portal is not hand-kept. Until then — a hand-kept portal, whose
  Component writes its object-shaped keys alone, or a Component not rendered yet — the portal's app-config keeps the
  section as the record carries it (`platformSection.appConfig`: the muster registry, `agentPlatform.kagent`,
  `agentPlatform.skills`, the chat's `aiChat`, `mcpActions` and `backend.actions`) and includes the list with the
  platform's section itself (`#extensionsAgentPlatform`, `#extensionsAgentPlatformAiChat` where the fragment or the
  app-config carries the chat, `platformSection.aiChat`, each with its `GrafanaDashboards` pair). So moving a
  hand-kept portal onto the definition takes three actions and the running portal keeps its platform section at
  every step: this definition's commit replaces the literal extension list with the include and keeps the rest;
  the agent-platform reconcile that follows finds the portal no longer hand-kept and takes the lists over, the skill
  repositories and the chat read back from the app-config; this definition's next reconcile hands the section over,
  its keys planned as `other-definition` removals (*Moved*), the include as *Changed*. `platformSection` is read on
  every comparison from the portal's app-config and the fragment on record (`backstage:agent-platform/app-config` in
  `x-files`), never typed. The portal's environment stays the portal's: `backstage.extraEnvVars`
  is one list Helm replaces wholesale across the HelmRelease's values sources (the shared base's default, the
  portal's user-values, the Component's values), so the user-values are its one owner — the avatars host of
  every installation the portal shows that runs the platform as the CSP image source
  (`BACKSTAGE_AVATARS_IMG_SRC`, space-separated: the portal's own first with `installation.agentPlatform`, then
  the federated ones with `federation.installations[*].agentPlatform` in list order, so the browser loads the
  avatar images a sibling's platform serves to a portal whose own installation runs none),
  `NODE_EXTRA_CA_CERTS` on the mounted SPIFFE bundle with the tunnel on (Node reads extra CA certificates
  through that variable alone; without it the mount is inert), no list without either so the shared base's
  `'self'` stands — and the Component sets none.
- The hub's Dev Portal — the pages it serves over muster, its incident links, CircleCI proxy and scaffolder, its
  GitHub and container registry access, its Postgres database — is out of the definition until a hub shape exists:
  those sections are `hub` removals. The comparison plans their removal like any other's, but a commit whose plan
  removes one the record carries with a value is refused for the installation — `commitRefused` names the sections
  by file (`hubSections` lists their keys), and a wave over a set with that installation is refused whole — so a
  customer-portal commit never strips the hub's portal. A section on record without a value (`scaffolder: null`)
  goes without a refusal. The way out is the hub's shape as an input of the definition, or the portal kept by hand.
  An entry of the hub's registry of installations (`gs.installations.<name>`) that a portal neither is nor
  federates is an ordinary `not-rendered` removal.
- `get_info` lists the definition with its schema and features, `list_installations` answers its state (marker:
  the portal's app-config in the management-clusters repository), `platformctl template` renders it, and
  `enable_capability`, `reconcile_capability` (one installation or the wave) and `verify_capability` take
  `capability: customer-portal` as they take `agent-platform`: the plan is rendered from this schema over the
  facts on record — the schema names the facts it takes under `installation` (`agentPlatform` is the
  agent-platform capability's enabled state on the installation, under `installation` and under each
  `federation.installations` entry) — and compared against the portal's own files.
- The plugin-to-plugin signing keys are one generated key pair (`backstage-plugin-keys`, ES256): the commit
  step draws an ECDSA P-256 pair once and writes the private half (PKCS #8 PEM) and the public half (SPKI PEM)
  into `plugin-keys-secret.enc.yaml`; a pair on record is frozen as a whole.
