# agent-platform definition

The `agent-platform` capability as data: what a person decides, what the record supplies, what the fleet
policy decides once, and what the definition drops. The render library reads it.

| File | What it is |
|---|---|
| `schema.json` | JSON Schema (draft 2020-12, `additionalProperties: false`) of the inputs: the record under `installation` (read, never typed) and the one choice, `modelServing.enabled` (default false). Every leaf carries `x-source`, `x-feature` and `x-renders`. |
| `policy.yaml` | The fleet's decisions, keyed by the owning organisation: which components its installations run, the chat gateway's shape, the hub connector's name and the Teleport cluster — and, keyed by installation, where a Slack app exists, which is where the gateway runs, with the installation's own default agent. |
| `removals.yaml` | Keys an enabled installation carries today that the definition does not render, each with the reason it is dropped: the template renders it, it becomes a referenced Secret, the platform does not read it, the customer-portal definition owns it, or it is a named migration (M12…) of a former input. |
| `features.yaml`, `probes.yaml` | The consistency features and their dimensions; the live probes the verify runs. |

## What the definition derives, from where

| Value | Source |
|---|---|
| Every hostname and URL, `global.domain`, the chart range, the managers' OAuth on the 3 line | the record: `config.yaml.patch` and the catalog (`installation.name`, `baseDomain`, `chartLine`, `musterClientId`, `provider`) |
| The private-address flags of muster and the MCP servers, a hub's tunnel to the installation | `installation.private`: reached through Teleport — a portal on record reaches the installation's Kubernetes API through the tunnel on its host, not at its API |
| Which components run (kagent, agent-manager, cluster-manager), the gateway's Slack/OBO/A2A/reviews shape, the cluster-manager's egress | `policy.yaml` by `installation.customer`; the egress by `installation.provider`. The cluster-manager is the 4 chart line's: a record on the 3 line whose organisation is granted it is refused at plan time, naming the component and `agentPlatform.kagentApiV2`, the key of `config.yaml.patch` that selects the line |
| Whether the cluster serves `certificates.k8s.io/v1beta1 PodCertificateRequest`, which kagent's Agent Substrate needs on the 4 line (below) | `installation.podCertificateRequest`: the cluster App on record in `management-clusters/<name>/cluster-app-manifests.yaml` — its values enable the three feature gates on every kubeadm component, or its chart enables them by default. Renders nothing; a 4-line record with kagent and `false` is refused at plan time, `true` renders the live probe `live-pod-certificate-request` |
| Whether the chat gateway runs (klaus-gateway with its generated OBO keys and its supplied Slack credentials), and the agent it calls by default | `policy.yaml`'s `klausGateway.installations` by `installation.name`: a map of the installations a Slack app exists for, each entry's `defaultAgent` one of the installation's own agents (`extras/agent-platform/agents/`, kept, not rendered), `a2a.defaultAgent` where the entry names none |
| The gateway's Slack mode: `events` over the public route, or `socketmode` with the app-level token where Slack cannot reach the ingress | `installation.private`: a private installation connects out in socket mode and renders no route; the mode is the record's, not the policy's |
| The portals' Dex client (`backstage`, one redirect URI per portal), the audiences muster and the kagent UI accept (each portal's client id where its host's Dex patch carries it, the ids the installation's own patch trusts today, and `backstage`, each once), the edge's JWT provider on the 4 line, the post-login allowlist where the gateway runs, the portal section in the organisation's own portal | `installation.portals`: every portal whose `gs.installations` lists the installation (the hub's Dev Portal, the organisation's `customer-portal` installations); `installation.portalAudiences`: the union of the installation's own `trustedAudiences`, `oidc-extra-audience`, edge JWT `audiences` and Dex `trustedPeers` on record, without the ids the definition renders itself |
| The hubs' token-exchange clients in this Dex; a hub's broker, identity providers, the targets' MCP servers, credentials Secrets, the tunnel and the hub's entries of teleport-fleet's tunnelport values (the hub as a consumer, its trust-bundle token, one tunnel per tunnelled app, edited into `kubernetes/envs/prod/values.yaml` beside every other hub's) | `installation.federation`: the portals' `clusterTokenBroker` names the hub, its `gs.installations` the targets; the broker client id is read back from the hub's patch; a private target's tunnel joins by the hub's published service-account issuer; a private target that runs the agent platform (`federation.targets[*].agentPlatform`, its enabled marker) is also tunnelled to its kagent and its agentgateway for the hub's Dev Portal (`https://irsa.<base domain>` on capa — another provider refuses a private target); the Teleport cluster is `policy.yaml`'s |
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
portals' Dex client id is the constant `backstage`; its secret is each installation's own; a portal that still signs in through another client is trusted by that client's id as well); every Dex client is
a referenced Secret in Dex's namespace (dex-app 3.2.0 or later). What a person supplies at commit is
named by field and follows the policy and the record: the Slack app's `klausGateway.slack.bot-token` and
`signing-secret` where the gateway runs, plus its `app-token` where the installation is private (socket mode),
nothing else — an installation without a Slack app commits with no supplied secret.
The model provider key is never supplied: on every installation kagent references the Secret
`kagent-anthropic-key` (key `ANTHROPIC_API_KEY`, namespace `kagent`), which the definition renders no file for.
Creating it is the one action the plan names for the installation's people, and until it exists the verify
reads the runtime feature as *waiting for the customer* rather than drifted.

Key paths in `x-renders` and `removals.yaml` are normalised: a list index is `[*]`, a per-installation map key is
`<name>`. A path covers every key beneath it.
