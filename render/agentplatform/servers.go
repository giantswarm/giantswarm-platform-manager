package agentplatform

import (
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// serverDefinition is one MCP server of the installation itself, as data: the
// fleet base its extras reference, the Secrets its chart reads and their key
// contract, the Dex client the dex-app chart renders for it, and its entry in
// muster's server list.
type serverDefinition struct {
	// name is the component and namespace: mcp-kubernetes, mcp-prometheus, mcp-capi.
	name string
	// group is the muster tool group.
	group string
	// dexClient is the dex-app built-in static client the shared template
	// renders id and redirect URI for; the definition adds the secret reference.
	dexClient string
	// dexSecretRef says whether the dex-app chart reads clientSecretRef on that
	// built-in client: it does for every server since dex-app 3.2.0, the
	// version the definition targets (below it mcpPrometheus and mcpCapi were
	// no built-ins and their keys were dropped). Where it is false, the client
	// keeps its inline secret and no reference is rendered.
	dexSecretRef bool
	// oauthKeys is the key contract of the server's oauth-credentials Secret:
	// the names its chart reads the values by.
	oauthKeys oauthSecretKeys
	// valuesKey is the top-level key the server's chart reads its values
	// under: mcp-kubernetes has its own, mcp-prometheus the fleet base's app,
	// mcp-capi reads them at the root (empty).
	valuesKey string
	// ssoPrivateIPs says whether the chart has a separate flag for the SSO
	// forwarded-token JWKS fetch reaching private addresses (oauth.sso.allowPrivateIPs).
	ssoPrivateIPs bool
}

// oauthSecretKeys names the keys of a server's oauth-credentials Secret as its
// chart reads them. A chart that reads the Secret key by key (secretKeyRef)
// names lower-case keys; one that loads it whole into the environment (envFrom)
// names the environment variables themselves.
type oauthSecretKeys struct {
	// dexClientSecret carries the Dex client secret, shared with the Dex-side client.
	dexClientSecret string
	// encryptionKey carries the OAuth token-encryption key.
	encryptionKey string
	// valkeyPassword carries the Valkey password for a chart that takes it from
	// this Secret rather than from the valkey-auth Secret; empty for a chart
	// that reads the valkey-auth Secret's key directly.
	valkeyPassword string
}

// keyedOAuthKeys is the contract of the charts that read the Secret key by key
// (mcp-kubernetes, mcp-capi); they take the Valkey password from the
// valkey-auth Secret.
var keyedOAuthKeys = oauthSecretKeys{dexClientSecret: "dex-client-secret", encryptionKey: "oauth-encryption-key"} // #nosec G101 -- Secret key names, not values

// envOAuthKeys is the contract of a chart that loads the Secret with envFrom
// (mcp-prometheus): the keys are the variables its process reads, the Valkey
// password included.
var envOAuthKeys = oauthSecretKeys{dexClientSecret: "DEX_CLIENT_SECRET", encryptionKey: "MCP_OAUTH_ENCRYPTION_KEY", valkeyPassword: "VALKEY_PASSWORD"} // #nosec G101 -- Secret key names, not values

// valkeyAuthKey is the key of a server's valkey-auth Secret: the fleet base's
// Valkey reads the default user's password from it (aclUsers.default.passwordKey).
const valkeyAuthKey = "default"

// servers are the platform's own MCP servers, the set the shared template
// registers with muster on every installation.
var servers = []serverDefinition{
	{name: "mcp-kubernetes", group: "kubernetes", dexClient: "mcpKubernetes", dexSecretRef: true, oauthKeys: keyedOAuthKeys, valuesKey: "mcpKubernetes", ssoPrivateIPs: true},
	{name: "mcp-prometheus", group: "prometheus", dexClient: "mcpPrometheus", dexSecretRef: true, oauthKeys: envOAuthKeys, valuesKey: "app"},
	{name: "mcp-capi", group: "capi", dexClient: "mcpCapi", dexSecretRef: true, oauthKeys: keyedOAuthKeys},
}

// userValuesFile is the per-server values file the kustomization turns into a
// ConfigMap the HelmRelease reads (valuesFrom).
const userValuesFile = "user-values.yaml"

// privateURLValues are the chart values a server needs when Dex is reached on
// private addresses: the OAuth issuer URL may resolve to a private IP and,
// where the chart has the flags, so may the SSO forwarded-token JWKS fetch
// and, on a private installation, the clients' metadata documents and
// redirect URIs.
func (s serverDefinition) privateURLValues(private bool) render.Map {
	oauth := render.Map{e("allowPrivateURLs", true)}
	if s.ssoPrivateIPs {
		oauth = append(oauth, e("sso", render.Map{e("allowPrivateIPs", true)}))
		if private {
			oauth = append(oauth, e("cimd", render.Map{e("allowPrivateIPs", true)}),
				e("redirectURISecurity", render.Map{e("allowPrivateIPRedirectURIs", true)}))
		}
	}
	values := render.Map{e("oauth", oauth)}
	if s.valuesKey == "" {
		return values
	}
	return render.Map{e(s.valuesKey, values)}
}

const (
	// dexNamespace is where the installation's Dex runs and reads client Secrets.
	dexNamespace = "giantswarm"
	// dexSecretKey is the key every Dex client Secret carries.
	dexSecretKey = render.DexSecretKey
	// platformNamespace is muster's namespace.
	platformNamespace = "agent-platform"
	// kagentNamespace is where kagent and its oauth2-proxy run.
	kagentNamespace = "kagent"
	// authenticatorClient is the Dex client every installation trusts.
	authenticatorClient = "dex-k8s-authenticator"
	// basesRepository is the fleet base the extras reference.
	basesRepository = "https://github.com/giantswarm/management-cluster-bases//extras/"
	// fileHeader opens every rendered YAML file.
	fileHeader = "# Rendered by giantswarm-platform-manager, agent-platform definition. Do not edit by hand:\n# the next reconcile writes it again from the installation's inputs.\n"
)

// BuiltInDexClientID is the Dex client id of a built-in client of the dex-app
// chart, by the chart's key, where the installation knows it: muster's is the
// record's client id, the authenticator's is fixed. The other built-in clients'
// ids are the fleet's shared template's; empty here.
func (in *Input) BuiltInDexClientID(key string) string {
	switch key {
	case "muster":
		return in.Installation.MusterClientID
	case "dexK8SAuthenticator":
		return authenticatorClient
	}
	return ""
}

// dexClientSecretName is the Secret in Dex's namespace that carries a
// component's client secret.
func dexClientSecretName(component string) string {
	return render.DexClientSecretName(component)
}

// dexClientSecretFile is that Secret's file name; it matches the fleet's
// .sops.yaml rules (.*(secret|credential).*) and the commit step's secret-file test.
func dexClientSecretFile(component string) string {
	return dexClientSecretName(component) + "-secret.yaml"
}

// dexClientSecret renders the Dex-side Secret of a client whose secret is the
// generated value valueName, shared with the workload's own Secret.
func dexClientSecret(component, valueName string) render.File {
	return render.Secret(dexClientSecretName(component), dexNamespace, nil,
		render.GeneratedKey(dexSecretKey, valueName, render.Base64, 32))
}

// mcpServerEntry is the server's entry in muster's list, the template's shape.
func (s serverDefinition) mcpServerEntry(installation string) MCPServer {
	return MCPServer{
		Cluster: installation,
		Group:   s.group,
		URL:     "http://" + s.name + "." + s.name + ".svc:8080/mcp",
		Timeout: 30,
		Auth:    MCPAuth{Mode: "forward"},
	}
}

// extras renders the server's extras directory: the kustomization over the
// fleet base and the Secrets its chart reads — the OAuth credentials under the
// server's key contract (the Dex client secret shared with the Dex client, the
// encryption key, and the Valkey password where the chart takes it from this
// Secret) and the valkey-auth Secret the fleet base's Valkey reads — plus,
// where the dex-app chart reads a reference for the client, the Dex-side copy
// of the client secret. The Valkey password is one generated value, so the
// server and its Valkey agree wherever each reads it. With privateURLs the
// directory also carries the server's user values and the kustomization turns
// them into a ConfigMap the HelmRelease reads.
func (s serverDefinition) extras(result *render.Result, repo render.Repository, dir string, in *Input) {
	privateURLs := in.Installation.Private
	valueName := s.name + "-dex-client-secret"
	valkeyValue := s.name + "-valkey-password"
	resources := []string{basesRepository + s.name + "?ref=main", "oauth-credentials.enc.yaml", "valkey-credentials.enc.yaml"}
	if s.dexSecretRef {
		resources = append(resources, dexClientSecretFile(s.name))
		result.Add(repo, dir+"/"+dexClientSecretFile(s.name), dexClientSecret(s.name, valueName))
	}
	k := kustomizationDoc{APIVersion: kustomizationAPIVersion, Kind: kustomizationKind, Resources: resources}
	if privateURLs {
		result.Add(repo, dir+"/"+userValuesFile, yamlFile(s.privateURLValues(in.Installation.Private)))
		k.withUserValues(s.name)
	}
	result.Add(repo, dir+"/kustomization.yaml", render.File{Content: append([]byte(fileHeader), render.MustYAML(k)...)})
	oauth := []render.SecretKey{
		render.GeneratedKey(s.oauthKeys.dexClientSecret, valueName, render.Base64, 32),
		render.GeneratedKey(s.oauthKeys.encryptionKey, s.name+"-oauth-encryption-key", render.Base64, 32),
	}
	if s.oauthKeys.valkeyPassword != "" {
		oauth = append(oauth, render.GeneratedKey(s.oauthKeys.valkeyPassword, valkeyValue, render.Alphanumeric, 32))
	}
	result.Add(repo, dir+"/oauth-credentials.enc.yaml", render.Secret(s.name+"-oauth-credentials", s.name, nil, oauth...))
	result.Add(repo, dir+"/valkey-credentials.enc.yaml", render.Secret(s.name+"-valkey-auth", s.name, nil,
		render.GeneratedKey(valkeyAuthKey, valkeyValue, render.Alphanumeric, 32),
	))
}

// The kustomize Kustomization's API version and kind.
const kustomizationAPIVersion, kustomizationKind = "kustomize.config.k8s.io/v1beta1", "Kustomization"

// kustomizationDoc is a kustomize Kustomization: the resources and, where a
// directory carries user values, the ConfigMap generated from them and the
// patch that hands it to the HelmRelease.
type kustomizationDoc struct {
	APIVersion         string               `yaml:"apiVersion"`
	Kind               string               `yaml:"kind"`
	Resources          []string             `yaml:"resources"`
	GeneratorOptions   *generatorOptions    `yaml:"generatorOptions,omitempty"`
	ConfigMapGenerator []configMapGenerator `yaml:"configMapGenerator,omitempty"`
	Patches            []kustomizePatch     `yaml:"patches,omitempty"`
}

type generatorOptions struct {
	DisableNameSuffixHash bool `yaml:"disableNameSuffixHash"`
}

type configMapGenerator struct {
	Name      string   `yaml:"name"`
	Namespace string   `yaml:"namespace"`
	Files     []string `yaml:"files"`
}

type kustomizePatch struct {
	Patch  string      `yaml:"patch"`
	Target patchTarget `yaml:"target"`
}

type patchTarget struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name"`
}

// withUserValues generates the ConfigMap <helmRelease>-user-values from the
// directory's user values (a stable name, no hash suffix) and appends it to
// the HelmRelease's valuesFrom.
func (k *kustomizationDoc) withUserValues(helmRelease string) {
	name := helmRelease + "-user-values"
	type op struct {
		Op    string         `yaml:"op"`
		Path  string         `yaml:"path"`
		Value map[string]any `yaml:"value"`
	}
	patch := render.MustYAML([]op{{Op: "add", Path: "/spec/valuesFrom/-",
		Value: map[string]any{"kind": "ConfigMap", "name": name, "valuesKey": "values"}}})
	k.GeneratorOptions = &generatorOptions{DisableNameSuffixHash: true}
	k.ConfigMapGenerator = []configMapGenerator{{Name: name, Namespace: fluxNamespace, Files: []string{"values=" + userValuesFile}}}
	k.Patches = []kustomizePatch{{Patch: strings.TrimRight(string(patch), "\n"), Target: patchTarget{Kind: "HelmRelease", Name: helmRelease}}}
}

// kustomization renders a kustomize Kustomization listing resources.
func kustomization(resources ...string) []byte {
	return append([]byte(fileHeader), render.MustYAML(kustomizationDoc{
		APIVersion: kustomizationAPIVersion, Kind: kustomizationKind, Resources: resources,
	})...)
}
