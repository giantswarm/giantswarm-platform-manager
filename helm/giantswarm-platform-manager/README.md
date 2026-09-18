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
| approvals.gatewayURL | string | `""` | klaus-gateway's base URL as reached from the pod (for example `http://klaus-gateway.agent-platform.svc:8080`): the team-review endpoint the asks a write raises go to. Empty leaves the asks undelivered; `get_info` reports `approvals.configured: false`. |
| approvals.channel | string | `""` | The channel the asks land in, as the gateway names it. |
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
