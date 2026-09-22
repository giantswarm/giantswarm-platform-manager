# customer-portal definition

The content of the `customer-portal` capability definition: its closed input set, the keys the renderer
drops, the consistency features and the anonymous probes. Data only; the render library reads it.

| File | What it is |
|---|---|
| `schema.json` | JSON Schema (draft 2020-12, `additionalProperties: false`) of the inputs. Every leaf input carries `x-source` (registry, person or generated), `x-feature` and `x-renders`: the key paths of the fileset it produces. `x-files` names the repository file each fileset key is read back from; a person input carries `x-readback` (file; key, one path or a list the first on record answers from, a list step an index or a `[key=value]` selector; kind `value`, `present`, `host`, `installation` — the installation whose base domain the URL's host is under the service label `prefix` names — or `file`; prefix) where the record can answer it, and says why where it cannot. A default is declared only where every portal agrees. |
| `removals.yaml` | Keys a portal installation carries today that no input renders, each with the reason it is dropped: the agent-platform definition renders it as its Component, or it is a section of the hub's Dev Portal that no shape renders yet. |
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

Key paths in `x-renders` and `removals.yaml` are normalised: a list index is `[*]`, a per-installation map key
is `<name>`. A path covers every key beneath it.

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
- A portal over several installations of one customer lists them under `federation.installations` with their
  facts on record: each gets a cluster entry and an installation entry, and a provider on its Dex whose client
  credentials are supplied at commit (`federation.<name>.clientId`, `clientSecret`); one that runs the agent
  platform (`agentPlatform`) has its avatars host in the portal's environment (below).
  `federation.signInInstallation` names the installation whose Dex signs people in; `federation.tokenBroker` the
  one whose muster brokers cluster tokens for the others — then the portal has the sign-in installation's
  provider alone, the broker's client credentials are supplied as `federation.tokenBroker.clientId`,
  `clientSecret`, and every other installation's entry carries `clusterTokenAudience`. An installation's entry lists `installation.providers` where it runs clusters on more
  than its own provider.
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
- The `dexAuthCredentials` leaves, the `sentry` leaves and the Grafana token of `user-secrets-backstage` are
  base64: the chart copies each entry's `clientID` and `clientSecret`, the three sentry values (`app.dsn`,
  `backend.dsn`, `reportURI`) and `grafana.apiToken` under its Secret's `data:` as they are and the pod loads
  that Secret with `envFrom`, so the values have to be the base64 of the id, of the secret, of the DSN, of the
  URI and of the token. The portal's own entry carries the base64 of `backstage` and of the generated client
  secret (the Dex client's Secret carries the same secret raw); a federated installation's and the broker's
  credentials, the Sentry values and the Grafana token are supplied raw and encoded by the render.
- The portal's agent-platform section is the agent-platform definition's: its fragment, values and Google
  credentials are files of its own Component next to the portal's, listed by this definition's kustomization
  and never written into the portal's files. The portal includes the shared extensions list without the
  platform's section, with the Grafana dashboards card where the plugin is wired (`#extensionsGrafanaDashboards`:
  the card is disabled in the app until a portal opts in through its list); the Component's fragment includes the
  platform's pair over it, reading the wiring off the record. The portal's environment stays the portal's: `backstage.extraEnvVars`
  is one list Helm replaces wholesale across the HelmRelease's values sources (the shared base's default, the
  portal's user-values, the Component's values), so the user-values are its one owner — the avatars host of
  every installation the portal shows that runs the platform as the CSP image source
  (`BACKSTAGE_AVATARS_IMG_SRC`, space-separated: the portal's own first with `installation.agentPlatform`, then
  the federated ones with `federation.installations[*].agentPlatform` in list order, so the browser loads the
  avatar images a sibling's platform serves to a portal whose own installation runs none),
  `NODE_EXTRA_CA_CERTS` on the mounted SPIFFE bundle with the tunnel on (Node reads extra CA certificates
  through that variable alone; without it the mount is inert), no list without either so the shared base's
  `'self'` stands — and the Component sets none.
- The hub's Dev Portal — the pages it serves over muster, its incident links, proxies and scaffolder, its
  registry of federated installations — is out of the definition until a hub shape exists; those keys are
  `not-rendered` removals.
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
