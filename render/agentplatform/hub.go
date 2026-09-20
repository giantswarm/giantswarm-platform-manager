package agentplatform

import (
	"bytes"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The hub side of federation: what an installation renders for the targets its
// muster exchanges tokens into. The broker's targets and the agentgateway's
// identity providers in the configmap patch, the targets' MCP servers with
// exchange auth in muster's list, a credentials Secret per target, and for a
// private target the tunnel on this hub: the tunnelport release next to muster
// and a RemoteApp per tunnelled app. The hub's entries of teleport-fleet's
// tunnelport values, which render the Teleport objects the tunnel joins with,
// are teleport.go's.

const (
	// brokerScopes is what the broker requests at a target's Dex: the cross-client
	// audience mints tokens the target's API server accepts.
	brokerScopes = "openid profile email groups audience:server:client_id:dex-k8s-authenticator"
	// providerScopes is what the agentgateway's identity provider requests at a target's Dex.
	providerScopes = "openid profile email groups"
	// tunnelPort is the port of every RemoteApp Service on the hub: ghostunnel
	// terminates TLS there with the app's SVID.
	tunnelPort = "8443"
	// spiffeBundle is the trust-bundle Secret the tunnelport release writes
	// and muster mounts to trust the tunnelled Services.
	spiffeBundle = "tunnelport-spiffe-bundle"
	// brokerClients is the Secret muster seeds the broker client record from
	// at startup, so a Valkey wipe self-heals.
	brokerClients = "muster-broker-clients"
	// tunnelportChart is the tunnelport operator chart; the floor is the first
	// release whose chart version label survives a digest-pinned OCI consumer.
	tunnelportChart  = "oci://gsoci.azurecr.io/charts/giantswarm/tunnelport"
	tunnelportSemver = ">=1.0.4"
	// githubGrantTarget is the broker's grant target on the registry's hub
	// (installation.hub): it releases the person's own GitHub grant — the one
	// the GitHub MCP servers pin with grantScope subject — to the hub's broker
	// client, an access token and its expiry, never the refresh token. The
	// hub's Dev Portal backs its GitHub auth API with it, so the portal needs
	// no GitHub App. A customer aggregator brokers for its siblings but holds
	// no GitHub grant, so it renders none.
	githubGrantTarget = "github"
	githubGrantIssuer = "https://github.com/login/oauth"
)

// tunnelledApp is one app reached through the tunnel on a private target. port is
// the tunnel's loopback port on the hub (the ghostunnel target), distinct from
// tunnelPort; the upstream port is the Teleport app's, advertised by the target.
// probe is the upstream's health path where GET / would not answer 2xx without
// a token; empty for no HTTP probe.
type tunnelledApp struct {
	name  string
	port  int
	probe string
}

// tunnelledApps are the target's Dex (the exchange endpoint), each federated
// group's MCP server, on a target that runs the agent platform its kagent (the
// UI and API v1 behind oauth2-proxy, whose health route is /ping) and its
// agentgateway (the kagent API v2 controller's gRPC listener, no HTTP probe) —
// the hub's Dev Portal reaches both through the tunnel — and the API server the
// broker's tokens are for.
func (t Target) tunnelledApps() []tunnelledApp {
	apps := []tunnelledApp{{name: "dex", port: 5556}}
	for _, g := range t.groups() {
		apps = append(apps, tunnelledApp{name: "mcp-" + g, port: 8080})
	}
	if t.AgentPlatform {
		apps = append(apps, tunnelledApp{name: "kagent", port: 4180, probe: "/ping"}, tunnelledApp{name: "agentgateway", port: 8080})
	}
	return append(apps, tunnelledApp{name: "kubernetes", port: 6443})
}

// appName is a tunnelled app's name on the hub and on Teleport: <app>-<target>.
func (t Target) appName(app string) string { return app + "-" + t.Installation }

// tunnelHost is the in-cluster DNS name of a tunnelled app's Service on the hub.
func (t Target) tunnelHost(app string) string {
	return t.appName(app) + "." + platformNamespace + ".svc.cluster.local:" + tunnelPort
}

// issuer is the target's Dex issuer, public whether or not the target is.
func (t Target) issuer() string { return "https://dex." + t.BaseDomain }

// dexTokenEndpoint is where the hub exchanges tokens: the target's Dex, through
// the tunnel for a private target.
func (t Target) dexTokenEndpoint() string {
	if t.Private {
		return "https://" + t.tunnelHost("dex") + "/token"
	}
	return t.issuer() + "/token"
}

// serverURL is a federated group's MCP server on the target.
func (t Target) serverURL(group string) string {
	if t.Private {
		return "https://" + t.tunnelHost("mcp-"+group) + "/mcp"
	}
	return "https://mcp-" + group + "." + t.BaseDomain + "/mcp"
}

// credentialsSecret is the hub-side Secret carrying the hub's client in the target's Dex.
func (t Target) credentialsSecret() string { return t.Installation + "-token-exchange-credentials" }

// exchangeSecretName is the generated value shared by the hub's credentials
// Secret for a target and the target's Dex-side copy: the pair names it, so the
// two installations' filesets agree on which value they share.
func exchangeSecretName(hub, target string) string {
	return hubClient(hub) + "-" + target + "-client-secret"
}

// brokerValues is muster.muster.oauth.server.tokenExchangeBroker: the hub's
// broker client, on the registry's hub the GitHub grant target, the targets
// it may mint for, and each target's exchange.
func (in *Input) brokerValues() render.Map {
	m := render.Map{}
	if in.hasPrivateTarget() {
		// A tunnelled target's token endpoint resolves to a ClusterIP; the broker's
		// SSRF guard would refuse it.
		m = append(m, e("allowPrivateIP", true))
	}
	names := make([]string, 0, len(in.Installation.Federation.Targets)+1)
	targets := render.Map{}
	if in.Installation.Hub {
		names = append(names, githubGrantTarget)
		targets = append(targets, e(githubGrantTarget, render.Map{e("grantIssuer", githubGrantIssuer)}))
	}
	for _, t := range in.Installation.Federation.Targets {
		names = append(names, t.Installation)
		entry := render.Map{e("dexTokenEndpoint", t.dexTokenEndpoint())}
		if t.Private {
			entry = append(entry, e("expectedIssuer", t.issuer()))
		}
		entry = append(entry, e("connectorId", in.Connector), e("scopes", brokerScopes),
			e("clientCredentialsSecretRef", render.Map{e("name", t.credentialsSecret())}))
		targets = append(targets, e(t.Installation, entry))
	}
	return append(m,
		e("brokerClients", render.Map{e(in.Installation.Federation.BrokerClientID,
			render.Map{e("clientCredentialsSecretRef", render.Map{e("name", brokerClients)})})}),
		e("clientAudiences", render.Map{e(in.Installation.Federation.BrokerClientID, names)}),
		e("targets", targets))
}

// extraCaFile is muster.muster.extraCaFile: the SPIFFE bundle muster trusts the
// tunnelled Services' SVIDs with.
func extraCaFile() render.Map {
	return render.Map{e("path", "/etc/muster/ca/spiffe-bundle.pem"),
		e("secret", render.Map{e("name", spiffeBundle), e("key", "svid_bundle.pem")})}
}

// identityProviders is agent-platform-mcps.identityProviders: the exchange at
// each target's Dex that the targets' MCP servers authenticate through.
func (in *Input) identityProviders() render.Map {
	providers := render.Map{}
	for _, t := range in.Installation.Federation.Targets {
		entry := render.Map{e("tokenEndpoint", t.dexTokenEndpoint())}
		if t.Private {
			entry = append(entry, e("expectedIssuer", t.issuer()))
		}
		entry = append(entry, e("connectorId", in.Connector), e("scopes", providerScopes),
			e("credentialsSecret", render.Map{e("name", t.credentialsSecret()),
				e("clientIdKey", "client-id"), e("clientSecretKey", "client-secret")}))
		providers = append(providers, e(t.Installation, entry))
	}
	return providers
}

// targetServers are the targets' MCP servers in muster's list, each authenticated
// by the exchange at its target's provider.
func (in *Input) targetServers() []MCPServer {
	var list []MCPServer
	for _, t := range in.Installation.Federation.Targets {
		for _, g := range t.groups() {
			list = append(list, MCPServer{Cluster: t.Installation, Group: g, URL: t.serverURL(g), Timeout: 30,
				Auth: MCPAuth{Mode: "exchange", Provider: t.Installation}})
		}
	}
	return list
}

// hubSecrets are the extras Secrets of a hub: the broker client's credentials
// and, per target, the hub's client in the target's Dex.
func (in *Input) hubSecrets(add func(file string, f render.File)) {
	hub := in.Installation.Name
	add(brokerClients+".yaml", render.Secret(brokerClients, platformNamespace,
		map[string]string{"muster.giantswarm.io/type": "broker-client-credentials"},
		render.ValueKey("client-id", in.Installation.Federation.BrokerClientID),
		in.generated("client-secret", "muster-broker-client-secret", render.Base64, 32)))
	for _, t := range in.Installation.Federation.Targets {
		add(t.credentialsSecret()+".yaml", render.Secret(t.credentialsSecret(), platformNamespace,
			map[string]string{"muster.giantswarm.io/management-cluster": t.Installation, "muster.giantswarm.io/type": "token-exchange-credentials"},
			render.ValueKey("client-id", hubClient(hub)),
			render.GeneratedKey("client-secret", exchangeSecretName(hub, t.Installation), render.Base64, 32)))
	}
}

// trustBundleTokenName is the provision token of this hub's trust-bundle bot,
// named in the tunnelport release and among the hub's entries of the
// tunnelport values.
func trustBundleTokenName(hub string) string { return "tunnelport-trust-bundle-token-" + hub }

// tunnelExtras is extras/agent-platform/tunnelport/: the tunnelport operator
// release in muster's namespace (the trust-bundle Secret it writes is mounted
// there) and one RemoteApp per tunnelled app of every private target.
func (in *Input) tunnelExtras(r *render.Result, repo render.Repository, dir string) {
	r.Add(repo, dir+"/kustomization.yaml", render.File{Content: kustomization("oci-repository.yaml", "helm-release.yaml", "remoteapps.yaml")})
	r.Add(repo, dir+"/oci-repository.yaml", yamlFile(render.Map{
		e("apiVersion", "source.toolkit.fluxcd.io/v1"), e("kind", "OCIRepository"),
		e("metadata", render.Map{e("name", "tunnelport"), e("namespace", fluxNamespace)}),
		e("spec", render.Map{e("interval", "10m"), e("url", tunnelportChart), e("ref", render.Map{e("semver", tunnelportSemver)}), e("provider", "generic")}),
	}))
	remediation := render.Map{e("remediation", render.Map{e("retries", 10), e("remediateLastFailure", false)})}
	r.Add(repo, dir+"/helm-release.yaml", yamlFile(render.Map{
		e("apiVersion", "helm.toolkit.fluxcd.io/v2"), e("kind", "HelmRelease"),
		e("metadata", render.Map{e("name", "tunnelport"), e("namespace", fluxNamespace)}),
		e("spec", render.Map{
			e("releaseName", "tunnelport"),
			e("chartRef", render.Map{e("kind", "OCIRepository"), e("name", "tunnelport"), e("namespace", fluxNamespace)}),
			e("interval", "10m"), e("targetNamespace", platformNamespace), e("timeout", "10m"),
			e("install", remediation), e("upgrade", remediation),
			e("values", render.Map{
				// The platform's pods pull through node-level registry access; the chart's default pull Secret does not exist here.
				e("imagePullSecret", ""),
				// Where the trust-bundle Secret and ServiceAccount are created; the trust-bundle token admits exactly this ServiceAccount.
				e("installNamespace", platformNamespace),
				e("teleport", render.Map{e("clusterName", in.Teleport.ClusterName), e("proxyAddr", in.Teleport.ProxyAddr)}),
				// Mimir loads a rule only with its tenant label.
				e("monitoring", render.Map{e("prometheusRule", render.Map{e("labels", render.Map{e("observability.giantswarm.io/tenant", "giantswarm")})})}),
				e("trustBundle", render.Map{e("enabled", true), e("secretName", spiffeBundle), e("tokenName", trustBundleTokenName(in.Installation.Name))}),
			}),
		}),
	}))
	var docs [][]byte
	for _, t := range in.Installation.Federation.Targets {
		if !t.Private {
			continue
		}
		for _, app := range t.tunnelledApps() {
			spec := render.Map{e("appName", t.appName(app.name)), e("port", app.port), e("tokenName", t.appName(app.name)+"-bot-token")}
			if app.probe != "" {
				spec = append(spec, e("probe", render.Map{e("path", app.probe)}))
			}
			docs = append(docs, render.MustYAML(render.Map{
				e("apiVersion", "access.giantswarm.io/v1alpha1"), e("kind", "RemoteApp"),
				e("metadata", render.Map{e("name", t.appName(app.name)), e("namespace", platformNamespace)}),
				e("spec", spec),
			}))
		}
	}
	header := fileHeader + "# One RemoteApp per tunnelled app of every private target: a tbot + ghostunnel Deployment and a\n" +
		"# Service named <app>-<target> on :" + tunnelPort + ", TLS terminated with the app's SVID. spec.port is the\n" +
		"# tunnel's loopback port; the upstream port is the Teleport app's, advertised by the target. A target\n" +
		"# that runs the agent platform is also tunnelled to its kagent and its agentgateway, for the Dev Portal.\n"
	r.Add(repo, dir+"/remoteapps.yaml", render.File{Content: append([]byte(header), bytes.Join(docs, []byte("---\n"))...)})
}
