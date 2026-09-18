// Package agentplatform is the agent-platform capability definition over the
// render library: an installation's inputs (definitions/agent-platform/schema.json)
// in, the files of its configs and management-clusters repositories out, with
// the probes of the running installation and the customer's actions as data.
//
// It renders a public or private installation's own platform: the
// agent-platform configmap patch with the components the policy offers, the
// dex-app configmap patch with every Dex client as a plaintext entry
// referencing a Secret, the extras/agent-platform tree, the extras of the
// installation's own MCP servers and the platform's section of the developer
// portal. It refuses, naming the input, what it does not render yet: the hub
// outputs of federation.targets.
package agentplatform

import (
	"fmt"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Render turns an installation's inputs into its fileset. raw is the decoded
// input document (map[string]any at the top, as a YAML or JSON decoder returns
// it); secrets carries the values the person supplies, by field name — the
// model key for kagent.modelKeySecret: managed, the client credentials of an
// oauth server in toolAccess.additionalServers. Everything else the platform
// needs is a placeholder the commit step generates.
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
	// The portal's client ids look like credentials to a secret scanner; they are public identifiers.
	configmap.Content = render.LineComment(configmap.Content, "oidc-extra-audience", "gitleaks:allow")
	r.Add(configs, apps+"agent-platform/configmap-values.yaml.patch", configmap)
	r.Add(configs, apps+"dex-app/configmap-values.yaml.patch", yamlFile(in.dexPatch()))
	in.platformExtras(r, clusters, extras+"agent-platform", secrets)
	r.Include(clusters, extras+"kustomization.yaml", "./agent-platform/")
	for _, s := range servers {
		s.extras(r, clusters, extras+s.name)
		r.Include(clusters, extras+"kustomization.yaml", "./"+s.name+"/")
	}
	if in.Portal.Enabled {
		backstage := "management-clusters/" + in.portalHost() + "/extras/backstage/"
		in.portalFiles(r, clusters, backstage+portalDir, secrets)
		r.IncludeComponent(clusters, backstage+"kustomization.yaml", "./"+portalDir+"/")
	}
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

// audiences are the Dex client ids whose tokens the platform accepts as
// bearer tokens: the authenticator, the kagent UI's client when it runs, the
// portal instances and whatever the person adds.
func (in *Input) audiences() []string {
	a := []string{authenticatorClient}
	if in.Kagent.Enabled {
		a = append(a, "kagent")
	}
	a = append(a, in.Portal.ClientIDs...)
	return append(a, in.Identity.ExtraTrustedAudiences...)
}

// configmapPatch is installations/<name>/apps/agent-platform/configmap-values.yaml.patch,
// merged by konfigure over the shared template: only what deviates per installation.
func (in *Input) configmapPatch() render.Map {
	var m render.Map
	m = append(m, e("global", render.Map{e("domain", in.Installation.BaseDomain)}))

	components := render.Map{e("kagent", render.Map{e("enabled", in.Kagent.Enabled)}),
		e("agent-manager", render.Map{e("enabled", in.ToolAccess.AgentManager.Enabled)})}
	if in.AgentSandbox != nil {
		components = append(components, e("agent-sandbox", render.Map{e("enabled", in.AgentSandbox.Enabled)}))
	}
	m = append(m, e("components", in.componentToggles(components)))

	if in.Kagent.Enabled {
		postgres := render.Map{e("enabled", true)}
		if in.Kagent.StorageClass != "" {
			postgres = append(postgres, e("storage", render.Map{e("storageClass", in.Kagent.StorageClass)}))
		}
		if in.Kagent.PostgresBackupAzureSubscriptionID != "" {
			postgres = append(postgres, e("backup", render.Map{e("crossplane", render.Map{e("azure",
				render.Map{e("subscriptionId", in.Kagent.PostgresBackupAzureSubscriptionID)})})}))
		}
		m = append(m, e("postgres", postgres), e("llmRouting", render.Map{e("enabled", true)}), e("kagent", in.kagentValues()))
	}

	m = append(m, e("muster", in.musterValues()))
	if len(in.ToolAccess.AdditionalServers) > 0 {
		list := make([]MCPServer, 0, len(servers)+len(in.ToolAccess.AdditionalServers))
		for _, s := range servers {
			list = append(list, s.mcpServerEntry(in.Installation.Name))
		}
		list = append(list, in.ToolAccess.AdditionalServers...)
		m = append(m, e("agent-platform-mcps", render.Map{e("mcpServers", list)}))
	}
	if in.ToolAccess.AgentManager.Enabled && in.Installation.ChartLine == "3" {
		m = append(m, e("agent-manager", render.Map{e("oauth", in.managerOAuth("agent-manager"))}))
	}
	m = in.componentValues(m)
	m = append(m, e("valkey", render.Map{e("valkey", render.Map{e("auth", render.Map{
		e("usersExistingSecret", in.Secrets.MusterValkey),
		e("aclUsers", render.Map{e("default", render.Map{e("passwordKey", "valkey-password")})}),
	})})}))
	return m
}

func (in *Input) kagentValues() render.Map {
	anthropic := render.Map{}
	if in.Kagent.DefaultModel != "" {
		anthropic = append(anthropic, e("model", in.Kagent.DefaultModel))
	}
	anthropic = append(anthropic, e("apiKeySecretRef", "kagent-anthropic-key"),
		e("config", render.Map{e("baseUrl", "http://agentgateway.agent-platform.svc:8081")}))
	k := render.Map{e("providers", render.Map{e("anthropic", anthropic)})}
	if len(in.Kagent.AdditionalModelConfigs) > 0 {
		k = append(k, e("modelConfigs", in.Kagent.AdditionalModelConfigs))
	}
	if in.Kagent.ControllerResources != nil {
		k = append(k, e("controller", render.Map{e("resources", in.Kagent.ControllerResources)}))
	}
	for _, agent := range in.Kagent.BundledAgents {
		k = append(k, e(agent, render.Map{e("enabled", true)}))
	}
	k = append(k, e("oauth2-proxy", render.Map{
		e("config", render.Map{e("existingSecret", "kagent-oauth2-proxy-credentials")}),
		e("extraArgs", render.Map{e("oidc-extra-audience", strings.Join(in.audiences(), ","))}),
	}))
	if len(in.Kagent.UIIngressPeers) > 0 {
		k = append(k, e("oauth2ProxyIngress", render.Map{e("additionalPeers", in.Kagent.UIIngressPeers)}))
	}
	return k
}

func (in *Input) musterValues() render.Map {
	server := render.Map{
		e("existingSecret", in.Secrets.MusterOAuth),
		e("storage", render.Map{e("valkey", render.Map{e("existingSecret", in.Secrets.MusterValkey)})}),
	}
	if in.Identity.LoginConnectorID != "" {
		server = append(server, e("dex", render.Map{e("connectorId", in.Identity.LoginConnectorID)}))
	}
	if in.Installation.Private {
		server = append(server, e("allowPrivateIPClientMetadata", true), e("allowPrivateIPRedirectURIs", true))
	}
	server = append(server, e("trustedAudiences", in.audiences()))
	if len(in.Identity.TrustedIssuers) > 0 {
		server = append(server, e("trustedIssuers", in.Identity.TrustedIssuers))
	}
	if len(in.Identity.PublicRegistrationRedirectURIs) > 0 {
		server = append(server, e("trustedPublicRegistrationRedirectURIs", in.Identity.PublicRegistrationRedirectURIs))
	}
	oauth := render.Map{}
	if len(in.Identity.PostLoginRedirectAllowlist) > 0 {
		oauth = append(oauth, e("mcpClient", render.Map{e("postLoginRedirectAllowlist", in.Identity.PostLoginRedirectAllowlist)}))
	}
	oauth = append(oauth, e("server", server))
	m := render.Map{}
	if in.Muster.Resources != nil {
		m = append(m, e("resources", in.Muster.Resources))
	}
	return append(m, e("muster", render.Map{e("oauth", oauth)}))
}

// dexClientRef is the referenced-Secret form of a Dex client secret.
func dexClientRef(component string) render.Map {
	return render.Map{e("name", dexClientSecretName(component)), e("key", dexSecretKey)}
}

// hubClient is the id of the token-exchange client a hub uses in this installation's Dex.
func hubClient(hub string) string { return hub + "-token-exchange" }

// dexPatch is installations/<name>/apps/dex-app/configmap-values.yaml.patch:
// the platform's clients in plaintext, every secret a reference to a Secret
// in Dex's namespace. It never touches the encrypted secret patch.
func (in *Input) dexPatch() render.Map {
	static := render.Map{e("muster", render.Map{e("clientSecretRef", dexClientRef("muster"))})}
	for _, s := range servers {
		if s.dexSecretRef {
			static = append(static, e(s.dexClient, render.Map{e("clientSecretRef", dexClientRef(s.name))}))
		}
	}
	peers := append([]string{}, in.Portal.ClientIDs...)
	for _, hub := range in.Federation.Hubs {
		peers = append(peers, hubClient(hub))
	}
	if len(peers) > 0 {
		static = append(static, e("dexK8SAuthenticator", render.Map{e("trustedPeers", peers)}))
	}
	var extra []render.Map
	if in.Kagent.Enabled {
		extra = append(extra, render.Map{e("id", "kagent"), e("name", "kagent-ui"),
			e("secretRef", dexClientRef("kagent")),
			e("redirectURIs", []string{in.kagentRedirectURI()})})
	}
	for _, hub := range in.Federation.Hubs {
		extra = append(extra, render.Map{e("id", hubClient(hub)), e("name", hub+" token exchange"),
			e("secretRef", dexClientRef(hubClient(hub)))})
	}
	for _, c := range in.Identity.AdditionalDexClients {
		entry := render.Map{e("id", c.ID), e("name", c.Name)}
		if c.Public {
			entry = append(entry, e("public", true))
		} else {
			entry = append(entry, e("secretRef", dexClientRef(c.ID)))
		}
		if len(c.RedirectURIs) > 0 {
			entry = append(entry, e("redirectURIs", c.RedirectURIs))
		}
		if len(c.TrustedPeers) > 0 {
			entry = append(entry, e("trustedPeers", c.TrustedPeers))
		}
		extra = append(extra, entry)
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
	k := kustomizationWithPatches{APIVersion: "kustomize.config.k8s.io/v1beta1", Kind: "Kustomization",
		Resources: []string{basesRepository + "agent-platform?ref=main", "./secrets"}}
	if in.Chart.Semver != "" {
		k.Patches = []patch{{
			Patch:  "- op: replace\n  path: /spec/ref/semver\n  value: " + fmt.Sprintf("%q", in.Chart.Semver) + "\n",
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
	add(in.Secrets.MusterOAuth+".yaml", render.Secret(in.Secrets.MusterOAuth, platformNamespace, team,
		render.GeneratedKey("dex-client-secret", "muster-dex-client-secret", render.Base64, 32),
		render.GeneratedKey("registration-token", "muster-registration-token", render.Base64, 32),
		render.GeneratedKey("oauth-encryption-key", "muster-oauth-encryption-key", render.Base64, 32)))
	add(in.Secrets.MusterValkey+".yaml", render.Secret(in.Secrets.MusterValkey, platformNamespace, team,
		render.GeneratedKey("valkey-password", "muster-valkey-password", render.Alphanumeric, 32)))
	add(dexClientSecretFile("muster"), dexClientSecret("muster", "muster-dex-client-secret"))
	if in.Kagent.Enabled {
		add("kagent-oauth2-proxy-credentials.yaml", render.Secret("kagent-oauth2-proxy-credentials", kagentNamespace, team,
			render.ValueKey("client-id", "kagent"),
			render.GeneratedKey("client-secret", "kagent-dex-client-secret", render.Base64, 32),
			render.GeneratedKey("cookie-secret", "kagent-cookie-secret", render.Alphanumeric, 32)))
		add(dexClientSecretFile("kagent"), dexClientSecret("kagent", "kagent-dex-client-secret"))
		if in.Kagent.ModelKeySecret == modelKeyManaged {
			add("kagent-anthropic-key-secret.yaml", render.Secret("kagent-anthropic-key", kagentNamespace, team,
				render.ValueKey("ANTHROPIC_API_KEY", secrets[fieldModelKey])))
		}
	}
	for _, hub := range in.Federation.Hubs {
		add(dexClientSecretFile(hubClient(hub)), dexClientSecret(hubClient(hub), hubClient(hub)+"-client-secret"))
	}
	for _, c := range in.Identity.AdditionalDexClients {
		if !c.Public {
			add(dexClientSecretFile(c.ID), dexClientSecret(c.ID, c.ID+"-dex-client-secret"))
		}
	}
	for _, s := range in.ToolAccess.AdditionalServers {
		if s.Auth.Mode == "oauth" {
			field := "toolAccess.additionalServers." + s.Name
			add(s.Name+"-oauth-client.yaml", render.Secret(s.Name+"-oauth-client", platformNamespace, team,
				render.ValueKey("client-id", secrets[field+".client-id"]),
				render.ValueKey("client-secret", secrets[field+".client-secret"])))
		}
	}
	if token := secrets[fieldSkillsToken]; token != "" {
		add(skillsTokenFile, render.Secret(skillsTokenSecret, kagentNamespace, team, render.ValueKey("token", token)))
	}
	in.componentSecrets(add, secrets)
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Key)
	}
	r.Add(repo, dir+"/secrets/kustomization.yaml", render.File{Content: kustomization(names...)})
}
