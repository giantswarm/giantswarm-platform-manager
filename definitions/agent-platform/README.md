# agent-platform definition

The `agent-platform` capability as data: what a person decides, what the record supplies, what the fleet
policy decides once, and what the definition drops. The render library reads it.

| File | What it is |
|---|---|
| `schema.json` | JSON Schema (draft 2020-12, `additionalProperties: false`) of the inputs: the record under `installation` (read, never typed) and the one choice, `modelServing.enabled` (default false). Every leaf carries `x-source`, `x-feature` and `x-renders`. |
| `policy.yaml` | The fleet's decisions, keyed by the owning organisation: which components its installations run, the chat gateway's shape, the connector names a target's Dex registers for a hub (the organisation's plain name for the target's first hub of the organisation, the hub's own for every further one) and the Teleport cluster — and, keyed by installation, where a Slack app exists, which is where the gateway runs, with the installation's own default agent. |
| `removals.yaml` | Keys an enabled installation carries today that the definition does not render, each with the reason it is dropped: the template renders it, it becomes a referenced Secret, the platform does not read it, the customer-portal definition owns it, or it is a named migration (M12…) of a former input. |
| `migrations.yaml` | Keys the definition renders that an installation enabled before a migration lacks (a referenced client Secret, the portal's Component, the edge's JWT provider, …), each with the migration that adds it: a leaf the record lacks under one of them is a planned change, not drift; so is a comma-separated scalar (kagent's `oidc-extra-audience`) whose only change is entries named as `<path>[<entry>]`. |
| `features.yaml`, `probes.yaml` | The consistency features and their dimensions; the live probes the verify runs. |

## What the definition derives, from where

| Value | Source |
|---|---|
| Every hostname and URL, `global.domain`, the chart range, the managers' OAuth on the 3 line | the record: `config.yaml.patch` and the catalog (`installation.name`, `baseDomain`, `chartLine`, `musterClientId`, `provider`) |
| The private-address flags of muster and the MCP servers, a hub's tunnel to the installation | `installation.private`: reached through Teleport — a portal on record reaches the installation's Kubernetes API through the tunnel on its host, not at its API |
| Which components run (kagent, agent-manager, cluster-manager), the gateway's Slack/OBO/A2A/reviews shape, the cluster-manager's egress | `policy.yaml` by `installation.customer`; the egress by `installation.provider`. The cluster-manager is the 4 chart line's: a record on the 3 line whose organisation is granted it is refused at plan time, naming the component and `agentPlatform.kagentApiV2`, the key of `config.yaml.patch` that selects the line |
| Whether the cluster serves `certificates.k8s.io/v1beta1 PodCertificateRequest`, which kagent's Agent Substrate needs on the 4 line (below) | `installation.podCertificateRequest`: the cluster App on record in `management-clusters/<name>/cluster-app-manifests.yaml` — its values enable the three feature gates on every kubeadm component, or its chart enables them by default. Renders nothing; a 4-line record with kagent and `false` is refused at plan time, `true` renders the live probe `live-pod-certificate-request` |
| Which dex-app the installation runs, the prerequisite of the referenced Dex client secrets (below) | `installation.dexAppVersion`: `spec.version` of the App `dex-app` — the installation's own pin, a patch on the App in `management-clusters/<name>/collections/kustomization.yaml`, wins; without one, the fleet's shared base, `bases/collections/shared/base/dex-app.yaml` in the management-cluster-bases repository the kustomization's remote resource names, at the ref it names (`?ref=`). Absent where neither could be read, with the error on the report. Renders nothing; older than 3.2.2, the comparison runs and the commit is held (`commitRefused`) |
| Whether the chat gateway runs (klaus-gateway with its generated OBO keys and its supplied Slack credentials), and the agent it calls by default | `policy.yaml`'s `klausGateway.installations` by `installation.name`: a map of the installations a Slack app exists for, each entry's `defaultAgent` one of the installation's own agents (`extras/agent-platform/agents/`, kept, not rendered), `a2a.defaultAgent` where the entry names none |
| The gateway's Slack mode: `events` over the public route, or `socketmode` with the app-level token where Slack cannot reach the ingress | `installation.private`: a private installation connects out in socket mode and renders no route; the mode is the record's, not the policy's |
| The portals' Dex client (`backstage`, one redirect URI per portal), the audiences muster and the kagent UI accept (each portal's client id where its host's Dex patch carries it, and `backstage`, each once), the edge's JWT provider on the 4 line, the post-login allowlist where the gateway runs, the portal section in the installation's own portal (else the organisation's portal on a sibling that is not hand-kept; the hub's hand-kept Dev Portal takes no other installation's section) — with the agents' Flux identity (`agentPlatform.fluxServiceAccountName: kagent-flux`) where that portal follows a chart line before backstage 1.1.0, whose plugin composes the agent's HelmRelease itself | `installation.portals`: every portal whose `gs.installations` lists the installation (the hub's Dev Portal, the organisation's `customer-portal` installations); `handKept` where the portal's app-config carries a literal `app.extensions` list of its own — there the Component sets none of the portal's lists (`app.extensions`, `muster.installations`) and writes its object-shaped keys only; the portal's environment (`backstage.extraEnvVars`, the avatars image source among it) is the customer-portal definition's user values' on every portal, never the Component's. `chartLine` is the line its kustomization patches onto the fleet base's backstage OCIRepository; a plan with kagent on an installation whose hosted portal carries none is refused naming the file. An id the installation trusts besides is kept by the plan in the list it is on record in — `trustedAudiences`, `oidc-extra-audience`, the edge provider's `audiences` or Dex's `trustedPeers` — after the definition's entries and nowhere else |
| The hubs' token-exchange clients in this Dex; a hub's broker, identity providers, the targets' MCP servers, credentials Secrets, the tunnel and the hub's entries of teleport-fleet's tunnelport values (the hub as a consumer, its trust-bundle token, one tunnel per tunnelled app, edited into `kubernetes/envs/prod/values.yaml` beside every other hub's) | `installation.federation`: the portals' `clusterTokenBroker` names the hub, its `gs.installations` the targets; the broker client id is read back from the hub's patch; a private target's tunnel joins by the hub's published service-account issuer; a private target whose agent platform the hub's portal proxies (`federation.targets[*].platformProxied`: the portal's `agentPlatform.kagent.installations` lists it) is also tunnelled to its kagent and its agentgateway; one the portal reaches through the kubernetes tunnel alone is not (`https://irsa.<base domain>` on capa — another provider refuses a private target); the connector the exchange at a target's Dex names is the policy's `federation.connector.first` (`<customer>-simple-oidc`) where this hub is the target's first hub of the organisation — `federation.targets[*].hubs`, the organisation's hubs that broker into the target, the registry's hub first, then by name — and `federation.connector.further` (`<customer>-<hub>-oidc`) for every further hub, since the target's Dex registers one connector per hub and only one can carry the plain name; the Teleport cluster is `policy.yaml`'s |
| The broker's `github` grant target (`grantIssuer: https://github.com/login/oauth`, `github` among the broker client's audiences): the hub's Dev Portal backs its GitHub auth API with the person's own GitHub grant held by muster, without a GitHub App in the portal | `installation.hub`: the registry's hub; a customer aggregator brokers for its siblings without it — nobody there holds a GitHub grant |
| The serving slice (`components.kserve-llmisvc-*`, `components.modelServing`, `modelServing.serving`, `modelServing.modelsGateway`) | the one choice, `modelServing.enabled`, on the 4 chart line |
| The platform's own MCP servers, the login connector, `allowPrivateIPOIDC`, `forbidInlineSecrets`, the default model, muster's trusted issuers, resources | the shared-configs template — a hand-written copy is a removal, not an input |

## The 4 chart line's prerequisites

The 4 line is selected by the record (`agentPlatform.kagentApiV2: true` in `installations/<name>/config.yaml.patch`)
and needs, beyond the 3 line's, a cluster that serves `certificates.k8s.io/v1beta1 PodCertificateRequest`: kagent's
Agent Substrate (meta chart 4.49.0 and later) issues each agent's pod certificates through it and distributes its
CA through `ClusterTrustBundle` projected volumes, and the meta chart refuses its install at render time where the
API is not served. That takes Kubernetes 1.35 with the feature gates `PodCertificateRequest`, `ClusterTrustBundle`
and `ClusterTrustBundleProjection` enabled on the API server, the controller manager and every kubelet — a fact of
the cluster App on record, `management-clusters/<name>/cluster-app-manifests.yaml` in the installation's
management-clusters repository, which the plan derives `installation.podCertificateRequest` from:

- the App's values (the ConfigMap its `userConfig` names) carry the three gates enabled in each of
  `cluster.internal.advancedConfiguration.controlPlane.apiServer.featureGates`,
  `.controlPlane.controllerManager.featureGates` and `.kubelet.featureGates` (entries of
  `{name, enabled, minKubernetesVersion}`; a list on record replaces the chart's default list, so it carries every
  gate the component needs), or
- for a component whose list the record does not set, the App's chart enables them by default: the cluster chart
  8.3.0 and later ([giantswarm/cluster#1005](https://github.com/giantswarm/cluster/pull/1005)), which the provider
  charts ship from `cluster-aws` 10.3.0, `cluster-azure` 9.3.0 and `cluster-cloud-director` 7.3.0 (the table in
  `substrate.go`; a chart not in it — `cluster-vsphere` until it releases the pin, `cluster-eks` — counts only where
  the record sets the gates).

With kagent rendered on the 4 line and the fact `false`, the plan refuses — `installation.podCertificateRequest: …`
naming the gates, the path and the charts — and the dry run says a commit would be refused (`commitRefused`); the
3 line renders whatever the fact says. With the fact `true`, the runtime feature's live dimension
`live-pod-certificate-request` discovers the API through the installation's apiserver: served, as defined; while
the control plane and the nodes still roll after the gates were set, the check reads `rolling: …` and the feature
is marked, the meta chart's install waiting for the same API.

Credentials are generated by the engine as SOPS-encrypted Secrets under `extras/agent-platform/secrets/`, every
value named per installation so that no secret is ever shared between installations or organisations (the
portals' Dex client id is the constant `backstage`; a portal that still signs in through another client is trusted by that client's id as well); every Dex client is
a referenced Secret in Dex's namespace (dex-app 3.2.0 or later; next to a hand-made inline secret, which an installation enabled by hand still carries, 3.2.2). The portal's client Secret
`dex-client-backstage` is not this definition's: the customer-portal definition renders it into the portal's directory
(`extras/backstage/backstage/dex-client-backstage-secret.enc.yaml`), a hand-kept portal keeps it by hand, and this definition's
dex patch entry references it by name and renders no file for it — so on an installation with both capabilities no repository
path and no Kubernetes object is rendered by two definitions. That is a prerequisite of the plan: with `installation.dexAppVersion` on record older than 3.2.2, the comparison still shows every difference and the commit is held — `commitRefused` names the version, the file it was read from and the collections kustomization to pin `dex-app` in first; an empty fact holds nothing. What a person supplies at commit is
named by field and follows the policy and the record: the Slack app's `klausGateway.slack.bot-token` and
`signing-secret` where the gateway runs, plus its `app-token` where the installation is private (socket mode),
nothing else — an installation without a Slack app commits with no supplied secret.
The model provider key is never supplied: on every installation kagent references the Secret
`kagent-anthropic-key` (key `ANTHROPIC_API_KEY`, namespace `kagent`), which the definition renders no file for.
Creating it is the one action the plan names for the installation's people, and until it exists the verify
reads the runtime feature as *waiting for the customer* rather than drifted.

Key paths in `x-renders` and `removals.yaml` are normalised: a list index is `[*]`, a per-installation map key is
`<name>`. A path covers every key beneath it.
