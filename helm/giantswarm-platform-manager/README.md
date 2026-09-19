# giantswarm-platform-manager

Giant Swarm's installation manager — an MCP server behind muster that enables, reconciles and verifies platform capabilities on opted-in installations as the person, every write a pull request to the installation's GitOps repository

The chart deploys the server as one Deployment behind a ClusterIP Service. The
server holds no state and no token of its own: behind muster every request
carries the person's GitHub user token — their authorization of the GitHub App
`giantswarm-platform-manager`, pinned as the authorization server on the
`MCPServer` the chart renders with `muster.mcpServer.enabled` and
`oauth.enabled` — and every write lands as that person.

Giant Swarm-specific: it manages the giantswarm org's installations and ships
as its own app on the hub installation, not as a component of the
`agent-platform` meta chart.

The live surface — the second registration muster forwards the person's own
ID token to, whose `verify_installation` and `watch_action` read an installation as the person —
is two switches, turned on in two releases: `live.enabled` serves the path in
the Deployment (`live.issuer`, `live.audiences`, the key set over HTTPS), and
`muster.liveServer.enabled`, in the release after it, renders the `MCPServer`
that registers the path with muster. The chart refuses to render the
registration without the path: muster probes a registration once, a probe
that reaches a pod without the path leaves the registration failed until it
is restarted, and a probe that finds the path answering reads *Auth Required*
— *Connected* once the first person's session connects.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| global | object | `{}` | Platform-wide values an umbrella chart shares with every component and Helm forwards here; this chart reads none of them. |
| replicaCount | int | `1` | Number of replicas. The server holds no state; more than one is fine. |
| image.registry | string | `"gsoci.azurecr.io"` | Image registry. |
| image.repository | string | `"giantswarm/giantswarm-platform-manager"` | Image repository. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.tag | string | `""` | Image tag. Defaults to the chart appVersion. |
| imagePullSecrets | list | `[]` | Image pull secrets. |
| nameOverride | string | `""` | Override the chart name. |
| fullnameOverride | string | `""` | Override the fully qualified app name. |
| mcp.path | string | `"/mcp"` | MCP streamable-HTTP endpoint path. |
| github.apiURL | string | `""` | GitHub API base URL the caller's calls go to; empty is api.github.com. |
| registry.repository | string | `"giantswarm/github"` | Repository (`owner/repo`) holding the installations catalog, read as the caller: the GitHub App `giantswarm-platform-manager` must be installed on it with contents read. |
| registry.path | string | `"catalog/installations.yaml"` | Path of the installations catalog in that repository: a Backstage catalog file, one `Resource` of type `installation` per installation. |
| hub | string | `""` | Name of the hub installation this manager runs on. Its management-clusters repository holds the Dev Portal's app-config the registry also reads, and the hub side of every capability lands in its repositories. Empty leaves `list_installations` refusing with the reason; `get_info` reports `registry.configured: false`. |
| actions.enabled | bool | `true` | Keep the Action records — one `Action` (`platform-manager.giantswarm.io/v1alpha1`) per enablement or reconcile a person commits: actor, installations, capability, inputs, pull requests, approval, rollout, probes, result — as custom resources in the release namespace, read and written with the pod's ServiceAccount (the CRD, a Role and its binding are rendered). Off: no ServiceAccount token is mounted, `get_action` and `list_actions` refuse with the reason and `get_info` reports `actions.configured: false`. |
| actions.installCRD | bool | `true` | Render the `actions.platform-manager.giantswarm.io` CRD from the chart's templates (so upgrades follow the chart). Off when the CRD is delivered separately. |
| approvals.gatewayURL | string | `""` | klaus-gateway's base URL as reached from the pod (for example `http://klaus-gateway.agent-platform.svc:8080`): the team-review endpoint an action's approval request goes to. Empty refuses `mode: commit` (nothing is committed that no one can approve); `get_info` reports `approvals.configured: false`. The requests carry the pod's projected ServiceAccount token in the gateway's audience; the gateway's `reviews.allowedCallers` must name this ServiceAccount. |
| approvals.team | string | `""` | The team whose members decide, as the gateway names it. |
| approvals.channel | string | `""` | The capability-owning team's channel the reviews land in (a Slack channel ID). |
| approvals.noticeChannel | string | `""` | The channel told of a review that targets a customer installation, without buttons (the Account Engineers' channel ID). Must differ from `channel`; empty sends no notice. |
| approvals.audience | string | `"klaus-gateway"` | Audience of the projected ServiceAccount token the requests carry: the gateway's TokenReview audience. |
| live.enabled | bool | `false` | Serve the live surface: a second MCP endpoint at `live.path` muster forwards the person's own ID token to, whose tools `verify_installation` and `watch_action` read an installation through muster as the person. The Deployment serves the path; `muster.liveServer.enabled` registers it with muster and needs this on. Turn the path on in a release of its own and register it in the next: muster probes a registration once, and a probe that reaches the previous pod — still behind the Service while the release that turns the path on rolls out — finds no path there and leaves the registration failed until it is restarted by hand (giantswarm/muster#1295). Off: the live dimensions of every verify read not checked, and `get_info` reports `live.configured: false`. |
| live.path | string | `"/mcp/live"` | MCP streamable-HTTP endpoint path of the live surface, next to `mcp.path` on the same Deployment. |
| live.issuer | string | `""` | Issuer of the forwarded tokens: the platform identity provider's, `https://dex.<base domain>/dex` on an installation. Required with `live.enabled`. |
| live.audiences | list | `[]` | OAuth clients whose ID tokens the live surface accepts: a person's forwarded token names the client they signed in with, so this lists the platform's own client (`agent-platform` on the platform) and every other client people reach muster through (a developer portal's). The server trusts the union of this list and `muster.liveServer.requiredAudiences`: every forwarded token carries the required audiences by construction. The union must name at least one audience with `live.enabled`; the chart refuses to render otherwise. |
| live.jwksURL | string | `""` | The issuer's key set, an `https://` URL; empty reads it from the issuer's OpenID discovery document (an `https://` issuer on a public address). The key set is read over TLS only: a plain-`http://` URL is refused by the chart and at start-up, never fallen back from. An in-cluster identity provider is named by the Service name its certificate carries — `https://dex.dex.svc.cluster.local:5556/dex/keys` in a lab — with `allowPrivateIPJWKS` for the private address and `caSecret` for the CA the certificate chains to. |
| live.allowPrivateIPJWKS | bool | `false` | Let the issuer's discovery document or the key set be read from a private address (an in-cluster identity provider's Service): the allowance of the SSRF guard, not of the transport — the URL stays `https://`. |
| live.caSecret | string | `""` | Secret in the release namespace with a PEM bundle under `ca.crt` the issuer's certificate chains to (a lab CA); empty is the system trust. |
| live.muster.url | string | `""` | muster's own MCP endpoint as reached from the pod: where a live read loops back to with the person's token. Empty derives `http://muster.<namespace>.svc.cluster.local:8090/mcp` in the release namespace. |
| live.muster.kubernetesFamily | string | `"kubernetes"` | The muster family the installations' kubernetes tools are aggregated under — the tools are `x_<family>_get`, `_list` and `_logs` — and the family's argument that selects the installation. The platform's agent-platform-mcps chart registers every installation's mcp-kubernetes under the family `kubernetes` with `management_cluster`; a lab with one singleton server names it (`mcp-kubernetes`) and leaves the argument empty. |
| live.muster.kubernetesInstanceArg | string | `"management_cluster"` | The family's instance argument; empty for a singleton server. |
| oauth.enabled | bool | `false` | Require a GitHub user token as the bearer of every MCP request, verified with `GET /user`; the server then acts as that person. Behind muster the token is the person's own: muster runs the consent for the GitHub App `giantswarm-platform-manager` once (the MCPServer's `authorizationServer` pin under `muster.mcpServer.auth`), stores and refreshes the user token and puts it on every call. Off: anonymous — no caller, nothing acts as a person, the tools say so; only for a server nothing but a trusted proxy can reach. |
| oauth.baseURL | string | `""` | URL muster reaches this server at, without the MCP path: the resource of its OAuth protected-resource metadata. Empty derives the in-cluster Service URL, `http://<fullname>.<namespace>.svc.cluster.local:<service.port>`. |
| muster.mcpServer.enabled | bool | `false` | Register this server with muster by rendering an `mcpservers.muster.giantswarm.io` CR in the release namespace. Tools then appear as `x_<name>_<tool>`. |
| muster.mcpServer.name | string | `"giantswarm-platform-manager"` | MCPServer CR name (drives the tool prefix). |
| muster.mcpServer.autoStart | bool | `true` | Start the server connection when muster initializes. |
| muster.mcpServer.timeout | int | `120` | Seconds muster waits for one call before it cancels it (the CR's `spec.timeout`; muster's own default is 30, the CRD allows 1–300). 120 because a write in `mode: commit` renders the change and opens the pull request within the one call. |
| muster.mcpServer.description | string | `"Giant Swarm's installation manager — platform capabilities on opted-in installations, every write as the person"` | Human-readable description shown by muster. |
| muster.mcpServer.labels | object | `{}` | Extra labels on the MCPServer CR. |
| muster.mcpServer.auth | object | `{"authorizationServer":{"authorizationEndpoint":"https://github.com/login/oauth/authorize","clientCredentialsSecretRef":{"name":"giantswarm-platform-manager-oauth-client","namespace":""},"expectedIssuer":"https://github.com/login/oauth","grantScope":"subject","issuer":"https://github.com/apps/giantswarm-platform-manager","scopes":"","tokenEndpoint":"https://github.com/login/oauth/access_token"}}` | How muster authenticates to this server; rendered only with `oauth.enabled`: `auth.type: oauth` with the GitHub App `giantswarm-platform-manager` pinned as the authorization server — the pattern of the `github` and `pro` servers on the platform. GitHub publishes no discovery document, so the endpoints are named; the App's client credentials come from a Secret; no `scopes` for GitHub (the App's permissions are the App's), a `scopes` value for an authorization server that needs one. muster runs the consent once per person and puts their user token on every call. |
| muster.mcpServer.auth.authorizationServer.issuer | string | `"https://github.com/apps/giantswarm-platform-manager"` | The issuer identity the person's grant is filed under: the App's own, so the login App's GitHub grant stays separate. |
| muster.mcpServer.auth.authorizationServer.expectedIssuer | string | `"https://github.com/login/oauth"` | The issuer the authorization server puts in the RFC 9207 `iss` parameter of its authorization response — GitHub's is `https://github.com/login/oauth` for every App, while `issuer` stays the App's own identity the grant is filed under. Empty omits the field. |
| muster.mcpServer.auth.authorizationServer.authorizationEndpoint | string | `"https://github.com/login/oauth/authorize"` | GitHub's authorize endpoint. |
| muster.mcpServer.auth.authorizationServer.tokenEndpoint | string | `"https://github.com/login/oauth/access_token"` | GitHub's token endpoint. |
| muster.mcpServer.auth.authorizationServer.scopes | string | `""` | The OAuth `scope` parameter of the authorization request (space-separated), only for an authorization server that needs one — a Dex standing in for GitHub in a lab takes `openid profile email offline_access`. Leave empty for GitHub: a GitHub App's permissions are the App's, and the token endpoint takes no scope. Empty omits the field. |
| muster.mcpServer.auth.authorizationServer.clientCredentialsSecretRef.name | string | `"giantswarm-platform-manager-oauth-client"` | Secret with the App's OAuth client under `client-id` and `client-secret`. |
| muster.mcpServer.auth.authorizationServer.clientCredentialsSecretRef.namespace | string | `""` | Namespace of that Secret; empty is the release namespace. |
| muster.mcpServer.auth.authorizationServer.grantScope | string | `"subject"` | `subject`: the grant belongs to the person, not to one login session — every session of theirs (the portal, an agent, a Slack click) carries the same token. |
| muster.liveServer.enabled | bool | `false` | Register the live surface with muster as a second MCPServer of the same Deployment: `auth.type: oauth` with `forwardToken: true` and no authorization server, so muster puts the person's own ID token on every call and runs no consent. Its tools `verify_installation` and `watch_action` read an installation through muster's kubernetes tools as the person. Rendered with `muster.mcpServer.enabled` and `oauth.enabled`; needs `live.enabled` — the chart refuses to render the registration of a path the Deployment does not serve — in a release before this one, so that muster's one probe of the registration finds the path answering (its state is then *Auth Required* until the first person's session connects, *Connected* after). |
| muster.liveServer.name | string | `"giantswarm-platform-manager-live"` | MCPServer CR name (drives the tool prefix). |
| muster.liveServer.requiredAudiences | list | `[]` | Audiences the forwarded token must carry, rendered as the CR's `spec.auth.requiredAudiences` when set: muster requests them from the identity provider at login as cross-client audiences, so every token it forwards carries them whichever client the person signed in with (`dex-k8s-authenticator` on Giant Swarm installations, the audience the platform's other forwarded-token servers require). Trusted by the server next to `live.audiences`. People sign in again after a change. |
| muster.liveServer.timeout | int | `180` | Seconds muster waits for one call: a verify reads every object of the definition's probes through muster and the installation's mcp-kubernetes within the one call. |
| muster.liveServer.description | string | `"Giant Swarm's installation manager, the live surface — verify_installation and watch_action read an installation as you"` | Human-readable description shown by muster. |
| serviceAccount.create | bool | `true` | Create a ServiceAccount. |
| serviceAccount.annotations | object | `{}` | Annotations on the ServiceAccount. |
| serviceAccount.name | string | `""` | ServiceAccount name (generated when empty). |
| podAnnotations | object | `{}` | Annotations on the pod. |
| podLabels | object | `{}` | Labels on the pod. |
| podSecurityContext | object | `{"fsGroup":1000,"runAsGroup":1000,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod security context. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsGroup":1000,"runAsNonRoot":true,"runAsUser":1000,"seccompProfile":{"type":"RuntimeDefault"}}` | Container security context. |
| service.type | string | `"ClusterIP"` | Service type. |
| service.port | int | `8080` | Service port (the container listens on 8080). |
| resources | object | `{"limits":{"cpu":"500m","memory":"256Mi"},"requests":{"cpu":"50m","memory":"64Mi"}}` | Container resources. |
| extraArgs | list | `[]` | Extra container arguments. |
| extraEnv | list | `[]` | Extra environment variables. |
| nodeSelector | object | `{}` | Node selector. |
| tolerations | list | `[]` | Tolerations. |
| affinity | object | `{}` | Affinity. |
