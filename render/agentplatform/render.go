// Package agentplatform is the agent-platform capability definition over the
// render library: an installation's record and its one choice
// (definitions/agent-platform/schema.json) in, the files of its configs and
// management-clusters repositories out, with the probes of the running
// installation and the customer's actions as data.
//
// It renders a public or private installation's own platform: the
// agent-platform configmap patch with the components the fleet policy gives
// its organisation, the dex-app configmap patch with every Dex client as a
// plaintext entry referencing a Secret, the extras/agent-platform tree, the
// extras of the installation's own MCP servers and the platform's section of
// the organisation's developer portal; and the hub side of federation: the
// broker and identity provider per target, the targets' MCP servers, the
// credentials Secrets and, for a private target, the tunnel on the hub
// (hub.go) with the hub's entries of teleport-fleet's tunnelport values
// (teleport.go).
package agentplatform

import (
	"fmt"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Render turns an installation's record and choice into its fileset. raw is
// the decoded input document (map[string]any at the top, as a YAML or JSON
// decoder returns it); secrets carries the values the person supplies, by
// field name — the Slack app's credentials where the gateway runs, nothing
// else. Everything else the platform needs is a placeholder the commit step
// generates, except the model key, which nobody supplies: kagent references
// the Secret by name and the person creates it (CustomerActions).
func Render(raw any, secrets map[string]string) (*render.Result, error) {
	in, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := in.check(secrets); err != nil {
		return nil, err
	}

	name := in.Installation.Name
	configs := render.Repository("giantswarm/" + in.Installation.Customer + "-configs")
	clusters := render.Repository("giantswarm/" + in.Installation.Customer + "-management-clusters")
	apps := "installations/" + name + "/apps/"
	extras := "management-clusters/" + name + "/extras/"

	r := &render.Result{}
	configmap := yamlFile(in.configmapPatch())
	// The portal's client id looks like a credential to a secret scanner; it is a public identifier.
	configmap.Content = render.LineComment(configmap.Content, "oidc-extra-audience", "gitleaks:allow")
	r.Add(configs, apps+"agent-platform/configmap-values.yaml.patch", configmap)
	r.Add(configs, apps+"dex-app/configmap-values.yaml.patch", yamlFile(in.dexPatch()))
	in.platformExtras(r, clusters, extras+"agent-platform", secrets)
	r.Include(clusters, extras+"kustomization.yaml", "./agent-platform/")
	for _, s := range servers {
		s.extras(r, clusters, extras+s.name, in)
		r.Include(clusters, extras+"kustomization.yaml", "./"+s.name+"/")
	}
	if host := in.portalHost(); host != "" {
		backstage := "management-clusters/" + host + "/extras/backstage/"
		in.portalFiles(r, clusters, backstage+portalDir)
		r.IncludeComponent(clusters, backstage+"kustomization.yaml", render.PortalPlatformComponent())
	}
	in.tunnelValues(r)
	r.Probes = in.probes()
	r.Actions = in.actions()
	return r, nil
}

func yamlFile(v any) render.File {
	return render.File{Content: append([]byte(fileHeader), render.MustYAML(v)...)}
}

func e(key string, value any) render.Entry { return render.Entry{Key: key, Value: value} }

// host is a platform hostname on the installation's base domain.
func (in *Input) host(component string) string {
	return component + "." + in.Installation.BaseDomain
}

// kagentRedirectURI is where Dex sends the kagent UI's login back to: its
// oauth2-proxy's callback.
func (in *Input) kagentRedirectURI() string {
	return "https://" + in.host("kagent") + "/oauth2/callback"
}

// hasPortal says whether a developer portal signs people in on this installation.
func (in *Input) hasPortal() bool { return len(in.Installation.Portals) > 0 }

// audiences are the Dex client ids whose tokens the platform accepts as
// bearer tokens: the authenticator, the kagent UI's client when it runs, and
// the portals' client when a portal signs people in here.
func (in *Input) audiences() []string {
	a := []string{authenticatorClient}
	if in.kagent() {
		a = append(a, "kagent")
	}
	if in.hasPortal() {
		a = append(a, render.PortalDexClientID)
	}
	return a
}

// The meta chart's serving slice (4.44.0 and later): the llm-d control plane's
// components and the values the slice sets.
const (
	servingRuntimeClass = "nvidia"
)

var servingComponents = []string{"kserve-llmisvc-crd", "kserve-llmisvc-resources", "kserve-runtime-configs", "modelServing"}

// configmapPatch is installations/<name>/apps/agent-platform/configmap-values.yaml.patch,
// merged by konfigure over the shared template: only what deviates per installation.
func (in *Input) configmapPatch() render.Map {
	var m render.Map
	m = append(m, e("global", render.Map{e("domain", in.Installation.BaseDomain)}))

	components := render.Map{e("kagent", render.Map{e("enabled", in.kagent())}),
		e("agent-manager", render.Map{e("enabled", in.agentManager())})}
	components = in.componentToggles(components)
	if in.ModelServing {
		for _, c := range servingComponents {
			components = append(components, e(c, render.Map{e("enabled", true)}))
		}
	}
	m = append(m, e("components", components))

	if in.kagent() {
		m = append(m, e("postgres", render.Map{e("enabled", true)}), e("llmRouting", render.Map{e("enabled", true)}), e("kagent", in.kagentValues()))
	}
	if in.ModelServing {
		m = append(m, e("modelServing", render.Map{
			e("serving", render.Map{e("runtimeClassName", servingRuntimeClass)}),
			e("modelsGateway", render.Map{e("enabled", true)}),
		}))
	}

	m = append(m, e("muster", in.musterValues()))
	targets := in.Installation.Federation.Targets
	var mcps render.Map
	if len(targets) > 0 {
		mcps = append(mcps, e("identityProviders", in.identityProviders()))
		// A patch replaces the list as a whole, so the template's own entries come first.
		list := make([]MCPServer, 0, len(servers)+len(targets)*len(servers))
		for _, s := range servers {
			list = append(list, s.mcpServerEntry(in.Installation.Name))
		}
		list = append(list, in.targetServers()...)
		mcps = append(mcps, e("mcpServers", list))
	}
	if in.edgeJWTProvider() {
		m = append(m, e("gateway", render.Map{e("jwksEgress", render.Map{e("enabled", true)})}),
			e("extraObjects", []render.Map{in.dexJWKSReferenceGrant()}))
		mcps = append(mcps, e("agentgateway", render.Map{e("jwt", render.Map{e("extraProviders", []render.Map{in.portalJWTProvider()})})}))
	}
	if len(mcps) > 0 {
		m = append(m, e("agent-platform-mcps", mcps))
	}
	if in.agentManager() && in.Installation.ChartLine == lineThree {
		m = append(m, e("agent-manager", render.Map{e("oauth", in.managerOAuth("agent-manager"))}))
	}
	m = in.componentValues(m)
	m = append(m, e("valkey", render.Map{e("valkey", render.Map{e("auth", render.Map{
		e("usersExistingSecret", musterValkeySecret),
		e("aclUsers", render.Map{e("default", render.Map{e("passwordKey", "valkey-password")})}),
	})})}))
	return m
}

// edgeJWTProvider says whether the edge accepts the portals' Dex ID token: a
// portal's chat forwards the signed-in person's token to the edge on /mcp,
// which on the 4 chart line validates it against a JWT provider of its own
// (the 3 line's edge forwards the bearer untouched).
func (in *Input) edgeJWTProvider() bool {
	return in.Installation.ChartLine == lineFour && in.hasPortal()
}

// dexService is the in-cluster Dex Service the edge fetches the JWKS from.
const dexService, dexServicePort = "dex", 5556

// dexJWKSReferenceGrant lets the edge's AgentgatewayPolicy in the platform's
// namespace reference the Dex Service in Dex's namespace for the JWKS fetch.
func (in *Input) dexJWKSReferenceGrant() render.Map {
	return render.Map{
		e("apiVersion", "gateway.networking.k8s.io/v1beta1"), e("kind", "ReferenceGrant"),
		e("metadata", render.Map{e("name", "agentgateway-jwks-dex"), e("namespace", dexNamespace)}),
		e("spec", render.Map{
			e("from", []render.Map{{e("group", "agentgateway.dev"), e("kind", "AgentgatewayPolicy"), e("namespace", platformNamespace)}}),
			e("to", []render.Map{{e("group", ""), e("kind", "Service"), e("name", dexService)}}),
		}),
	}
}

// portalJWTProvider is the edge's JWT provider for the portals' ID tokens:
// the installation's Dex as issuer, the portals' client as audience, the
// JWKS fetched in-cluster from the Dex Service.
func (in *Input) portalJWTProvider() render.Map {
	return render.Map{
		e("issuer", "https://"+in.host("dex")),
		e("audiences", []string{render.PortalDexClientID}),
		e("jwks", render.Map{e("remote", render.Map{
			e("backendRef", render.Map{e("name", dexService), e("namespace", dexNamespace), e("port", dexServicePort)}),
			e("jwksPath", "/keys"), e("cacheDuration", "5m"),
		})}),
	}
}

// kagentValues is the kagent section: the model provider wired through the
// edge to the key Secret the installation creates (modelKeySecret; no file is
// rendered for it), and the UI's oauth2-proxy with its credentials Secret and
// the audiences it accepts.
func (in *Input) kagentValues() render.Map {
	return render.Map{
		e("providers", render.Map{e("anthropic", render.Map{
			e("apiKeySecretRef", modelKeySecret),
			e("config", render.Map{e("baseUrl", "http://agentgateway.agent-platform.svc:8081")}),
		})}),
		e("oauth2-proxy", render.Map{
			e("config", render.Map{e("existingSecret", "kagent-oauth2-proxy-credentials")}),
			e("extraArgs", render.Map{e("oidc-extra-audience", strings.Join(in.audiences(), ","))}),
		}),
	}
}

// postLoginRedirectAllowlist is where muster may send the browser after a
// connector sign-in, where the gateway's on-behalf-of connectors run: the
// agentgateway completion landing and every portal that signs people in here.
func (in *Input) postLoginRedirectAllowlist() []string {
	list := []string{"https://" + in.host("agentgateway") + "/connectors/complete"}
	for _, p := range in.Installation.Portals {
		list = append(list, "https://"+p.Domain+"/")
	}
	return list
}

func (in *Input) musterValues() render.Map {
	server := render.Map{
		e("existingSecret", musterOAuthSecret),
		e("storage", render.Map{e("valkey", render.Map{e("existingSecret", musterValkeySecret)})}),
	}
	if in.Installation.Private {
		server = append(server, e("allowPrivateIPClientMetadata", true), e("allowPrivateIPRedirectURIs", true))
	}
	server = append(server, e("trustedAudiences", in.audiences()))
	if len(in.Installation.Federation.Targets) > 0 {
		server = append(server, e("tokenExchangeBroker", in.brokerValues()))
	}
	oauth := render.Map{}
	if in.klausGateway() && in.Gateway.OBO.Connectors {
		oauth = append(oauth, e("mcpClient", render.Map{e("postLoginRedirectAllowlist", in.postLoginRedirectAllowlist())}))
	}
	oauth = append(oauth, e("server", server))
	muster := render.Map{}
	if in.hasPrivateTarget() {
		muster = append(muster, e("extraCaFile", extraCaFile()))
	}
	return render.Map{e("muster", append(muster, e("oauth", oauth)))}
}

// dexClientRef is the referenced-Secret form of a Dex client secret.
func dexClientRef(component string) render.Map {
	return render.Map{e("name", dexClientSecretName(component)), e("key", dexSecretKey)}
}

// generated is a SecretKey whose value the commit step generates for this
// installation alone: the name carries the installation, because the commit
// step draws one value per name across every file of a pull request and a
// wave commits several installations of one organisation into one — no client
// secret is ever shared between installations.
func (in *Input) generated(key, base string, kind render.GeneratedKind, length int) render.SecretKey {
	return render.GeneratedKey(key, in.generatedName(base), kind, length)
}

// generatedName names a generated value of this installation.
func (in *Input) generatedName(base string) string { return in.Installation.Name + "-" + base }

// hubClient is the id of the token-exchange client a hub uses in this installation's Dex.
func hubClient(hub string) string { return hub + "-token-exchange" }

// portalDexClient is the one Dex client every portal signs in through: the
// customer-portal definition's client, with a redirect URI per portal.
func (in *Input) portalDexClient() render.Map {
	uris := make([]string, 0, len(in.Installation.Portals))
	for _, p := range in.Installation.Portals {
		uris = append(uris, render.PortalRedirectURI(p.Domain, in.Installation.Name))
	}
	return render.Map{e("id", render.PortalDexClientID), e("name", render.PortalDexClientName),
		e("redirectURIs", uris), e("secretRef", dexClientRef(render.PortalDexClientID))}
}

// dexPatch is installations/<name>/apps/dex-app/configmap-values.yaml.patch:
// the platform's clients in plaintext, every secret a reference to a Secret
// in Dex's namespace. It never touches the encrypted secret patch. The patch
// is one file with one owner: on an installation with the platform enabled
// this definition owns it, so the portals' client is carried here, the entry
// the customer-portal definition renders on an installation without the
// platform, with every portal's redirect URI.
func (in *Input) dexPatch() render.Map {
	static := render.Map{e("muster", render.Map{e("clientSecretRef", dexClientRef("muster"))})}
	for _, s := range servers {
		if s.dexSecretRef {
			static = append(static, e(s.dexClient, render.Map{e("clientSecretRef", dexClientRef(s.name))}))
		}
	}
	var peers []string
	if in.hasPortal() {
		peers = append(peers, render.PortalDexClientID)
	}
	for _, hub := range in.Installation.Federation.Hubs {
		peers = append(peers, hubClient(hub))
	}
	if len(peers) > 0 {
		static = append(static, e("dexK8SAuthenticator", render.Map{e("trustedPeers", peers)}))
	}
	var extra []render.Map
	if in.kagent() {
		extra = append(extra, render.Map{e("id", "kagent"), e("name", "kagent-ui"),
			e("secretRef", dexClientRef("kagent")),
			e("redirectURIs", []string{in.kagentRedirectURI()})})
	}
	if in.hasPortal() {
		extra = append(extra, in.portalDexClient())
	}
	for _, hub := range in.Installation.Federation.Hubs {
		extra = append(extra, render.Map{e("id", hubClient(hub)), e("name", hub+" token exchange"),
			e("secretRef", dexClientRef(hubClient(hub)))})
	}
	oidc := render.Map{e("staticClients", static)}
	if len(extra) > 0 {
		oidc = append(oidc, e("extraStaticClients", extra))
	}
	return render.Map{e("oidc", oidc)}
}

// platformExtras is management-clusters/<name>/extras/agent-platform/: the
// kustomization over the fleet base and the Secrets the platform reads.
func (in *Input) platformExtras(r *render.Result, repo render.Repository, dir string, secrets map[string]string) {
	type patch struct {
		Patch  string     `yaml:"patch"`
		Target render.Map `yaml:"target"`
	}
	type kustomizationWithPatches struct {
		APIVersion string   `yaml:"apiVersion"`
		Kind       string   `yaml:"kind"`
		Resources  []string `yaml:"resources"`
		Patches    []patch  `yaml:"patches,omitempty"`
	}
	k := kustomizationWithPatches{APIVersion: kustomizationAPIVersion, Kind: kustomizationKind,
		Resources: []string{basesRepository + "agent-platform?ref=main", "./secrets"}}
	if in.hasPrivateTarget() {
		k.Resources = append(k.Resources, "./tunnelport")
	}
	if semver := in.chartSemver(); semver != "" {
		k.Patches = []patch{{
			Patch:  "- op: replace\n  path: /spec/ref/semver\n  value: " + fmt.Sprintf("%q", semver),
			Target: render.Map{e("kind", "OCIRepository"), e("name", "agent-platform")},
		}}
	}
	r.Add(repo, dir+"/kustomization.yaml", yamlFile(k))

	team := teamLabels
	files := render.Map{}
	add := func(file string, f render.File) {
		files = append(files, e(file, nil))
		r.Add(repo, dir+"/secrets/"+file, f)
	}
	add(musterOAuthSecret+".yaml", render.Secret(musterOAuthSecret, platformNamespace, team,
		in.generated("dex-client-secret", "muster-dex-client-secret", render.Base64, 32),
		in.generated("registration-token", "muster-registration-token", render.Base64, 32),
		in.generated("oauth-encryption-key", "muster-oauth-encryption-key", render.Base64, 32)))
	add(musterValkeySecret+".yaml", render.Secret(musterValkeySecret, platformNamespace, team,
		in.generated("valkey-password", "muster-valkey-password", render.Alphanumeric, 32)))
	add(dexClientSecretFile("muster"), dexClientSecret("muster", in.generatedName("muster-dex-client-secret")))
	if in.kagent() {
		add("kagent-oauth2-proxy-credentials.yaml", render.Secret("kagent-oauth2-proxy-credentials", kagentNamespace, team,
			render.ValueKey("client-id", "kagent"),
			in.generated("client-secret", "kagent-dex-client-secret", render.Base64, 32),
			in.generated("cookie-secret", "kagent-cookie-secret", render.Alphanumeric, 32)))
		add(dexClientSecretFile("kagent"), dexClientSecret("kagent", in.generatedName("kagent-dex-client-secret")))
	}
	if in.hasPortal() {
		add(dexClientSecretFile(render.PortalDexClientID), dexClientSecret(render.PortalDexClientID, in.generatedName(render.PortalDexClientID+"-dex-client-secret")))
	}
	for _, hub := range in.Installation.Federation.Hubs {
		add(dexClientSecretFile(hubClient(hub)), dexClientSecret(hubClient(hub), exchangeSecretName(hub, in.Installation.Name)))
	}
	if len(in.Installation.Federation.Targets) > 0 {
		in.hubSecrets(add)
	}
	in.componentSecrets(add, secrets)
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Key)
	}
	r.Add(repo, dir+"/secrets/kustomization.yaml", render.File{Content: kustomization(names...)})
	if in.hasPrivateTarget() {
		in.tunnelExtras(r, repo, dir+"/tunnelport")
	}
}
