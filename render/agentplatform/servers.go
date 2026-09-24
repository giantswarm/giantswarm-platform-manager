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
	// revision carries the server's credentials revision (revisionSecret): a
	// key no chart reads, held here so a rewrite of this Secret draws the
	// revision anew.
	revision string
}

// keyedOAuthKeys is the contract of the charts that read the Secret key by key
// (mcp-kubernetes, mcp-capi); they take the Valkey password from the
// valkey-auth Secret.
var keyedOAuthKeys = oauthSecretKeys{dexClientSecret: "dex-client-secret", encryptionKey: "oauth-encryption-key", revision: "credentials-revision"} // #nosec G101 -- Secret key names, not values

// envOAuthKeys is the contract of a chart that loads the Secret with envFrom
// (mcp-prometheus): the keys are the variables its process reads, the Valkey
// password included.
var envOAuthKeys = oauthSecretKeys{dexClientSecret: "DEX_CLIENT_SECRET", encryptionKey: "MCP_OAUTH_ENCRYPTION_KEY", valkeyPassword: "VALKEY_PASSWORD", revision: "CREDENTIALS_REVISION"} // #nosec G101 -- Secret key names, not values

// valkeyAuthKey is the key of a server's valkey-auth Secret: the fleet base's
// Valkey reads the default user's password from it (aclUsers.default.passwordKey).
const valkeyAuthKey = "default"

// The server's credentials revision: a generated value drawn anew with every
// rotation of the server's credentials and kept otherwise, which the pod
// templates roll on. The server and its Valkey read their Secrets at start
// and never again, so a rotation alone left them on the old values. The
// revision is held by the server's credentials Secret and its Valkey's (a
// rewrite of either draws it anew, and every file holding it is rewritten
// with it) and by a third Secret beside the HelmReleases in the Flux
// namespace, which both read into their charts' checksum values (valuesFrom
// with targetPath): the charts render the value as a pod-template annotation,
// so the pods restart with the rotation and stay put without one.
const (
	// revisionKey is the key the revision is read by: in the valkey-auth
	// Secret beside the password, and in the revision Secret the HelmReleases read.
	revisionKey = "revision"
	// revisionLength is the size of the revision, an Alphanumeric value like
	// the Valkey password.
	revisionLength = 32
	// revisionFile is the revision Secret's file in the server's extras
	// directory; it matches the fleet's .sops.yaml rules (.*(secret|credential).*)
	// and the commit step's secret-file test like the other two.
	revisionFile = "credentials-revision.enc.yaml" // #nosec G101 -- a file name, not a value
	// valkeyChecksumValue is the Valkey chart's mark for a users Secret it
	// cannot read, rendered as the pod template's checksum annotation
	// (giantswarm/valkey-app: valkey.auth.usersExistingSecretChecksum).
	valkeyChecksumValue = "valkey.auth.usersExistingSecretChecksum"
)

// revisionSecretName is the Secret in the Flux namespace that carries the
// server's credentials revision for its HelmReleases.
func (s serverDefinition) revisionSecretName() string { return s.name + "-credentials-revision" }

// checksumValues are the chart values the server's HelmRelease takes the
// revision into: the OAuth credentials Secret's mark and, for a chart that
// reads the Valkey password from the valkey-auth Secret rather than from the
// OAuth Secret, that Secret's mark; each renders as a pod-template checksum
// annotation.
func (s serverDefinition) checksumValues() []string {
	prefix := "oauth."
	if s.valuesKey != "" {
		prefix = s.valuesKey + ".oauth."
	}
	values := []string{prefix + "existingSecretChecksum"}
	if s.oauthKeys.valkeyPassword == "" {
		values = append(values, prefix+"storage.valkey.existingSecretChecksum")
	}
	return values
}

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
// server and its Valkey agree wherever each reads it. Both Secrets carry the
// server's credentials revision, and a third Secret in the Flux namespace
// carries it for the HelmReleases, which the kustomization patches to read it
// into their charts' checksum values, so a rotation rolls the server and its
// Valkey; every value of the two Secrets names the revision as its own
// (render.Result.Revisions), so a rotation asked for by name draws it too.
// With privateURLs the directory also carries the server's user values
// and the kustomization turns them into a ConfigMap the HelmRelease reads.
func (s serverDefinition) extras(result *render.Result, repo render.Repository, dir string, in *Input) {
	privateURLs := in.Installation.Private
	valueName := in.generatedName(s.name + "-dex-client-secret")
	valkeyValue := in.generatedName(s.name + "-valkey-password")
	revision := in.generatedName(s.name + "-credentials-revision")
	resources := []string{basesRepository + s.name + "?ref=main", "oauth-credentials.enc.yaml", "valkey-credentials.enc.yaml", revisionFile}
	if s.dexSecretRef {
		resources = append(resources, dexClientSecretFile(s.name))
		result.Add(repo, dir+"/"+dexClientSecretFile(s.name), dexClientSecret(s.name, valueName))
	}
	k := kustomizationDoc{APIVersion: kustomizationAPIVersion, Kind: kustomizationKind, Resources: resources}
	if privateURLs {
		result.Add(repo, dir+"/"+userValuesFile, yamlFile(s.privateURLValues(in.Installation.Private)))
		k.withUserValues(s.name)
	}
	k.withRevision(s)
	result.Add(repo, dir+"/kustomization.yaml", render.File{Content: append([]byte(fileHeader), render.MustYAML(k)...)})
	oauth := []render.SecretKey{
		render.GeneratedKey(s.oauthKeys.dexClientSecret, valueName, render.Base64, 32),
		in.generated(s.oauthKeys.encryptionKey, s.name+"-oauth-encryption-key", render.Base64, 32),
	}
	if s.oauthKeys.valkeyPassword != "" {
		oauth = append(oauth, render.GeneratedKey(s.oauthKeys.valkeyPassword, valkeyValue, render.Alphanumeric, 32))
	}
	oauth = append(oauth, render.GeneratedKey(s.oauthKeys.revision, revision, render.Alphanumeric, revisionLength))
	valkey := []render.SecretKey{
		render.GeneratedKey(valkeyAuthKey, valkeyValue, render.Alphanumeric, 32),
		render.GeneratedKey(revisionKey, revision, render.Alphanumeric, revisionLength),
	}
	result.Add(repo, dir+"/oauth-credentials.enc.yaml", render.Secret(s.name+"-oauth-credentials", s.name, nil, oauth...))
	result.Add(repo, dir+"/valkey-credentials.enc.yaml", render.Secret(s.name+"-valkey-auth", s.name, nil, valkey...))
	// Every value of the two Secrets rolls the server and its Valkey with the revision.
	result.Revision(revision, oauth...)
	result.Revision(revision, valkey...)
	result.Add(repo, dir+"/"+revisionFile, render.Secret(s.revisionSecretName(), fluxNamespace, nil,
		render.GeneratedKey(revisionKey, revision, render.Alphanumeric, revisionLength),
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

// op is one operation of a JSON patch on a HelmRelease.
type op struct {
	Op    string `yaml:"op"`
	Path  string `yaml:"path"`
	Value any    `yaml:"value"`
}

// The JSON patch operation that adds a value, and the paths it adds a
// values source at: appended to the HelmRelease's valuesFrom, or as its list.
const (
	opAdd           = "add"
	valuesFromPath  = "/spec/valuesFrom"
	valuesFromEntry = valuesFromPath + "/-"
)

// patch appends a JSON patch on the HelmRelease of the name.
func (k *kustomizationDoc) patch(helmRelease string, ops []op) {
	patch := render.MustYAML(ops)
	k.Patches = append(k.Patches, kustomizePatch{Patch: strings.TrimRight(string(patch), "\n"), Target: patchTarget{Kind: "HelmRelease", Name: helmRelease}})
}

// withUserValues generates the ConfigMap <helmRelease>-user-values from the
// directory's user values (a stable name, no hash suffix) and appends it to
// the HelmRelease's valuesFrom.
func (k *kustomizationDoc) withUserValues(helmRelease string) {
	name := helmRelease + "-user-values"
	k.GeneratorOptions = &generatorOptions{DisableNameSuffixHash: true}
	k.ConfigMapGenerator = []configMapGenerator{{Name: name, Namespace: fluxNamespace, Files: []string{"values=" + userValuesFile}}}
	k.patch(helmRelease, []op{{Op: opAdd, Path: valuesFromEntry,
		Value: map[string]any{"kind": "ConfigMap", "name": name, "valuesKey": "values"}}})
}

// withRevision hands the server's credentials revision to its HelmReleases:
// valuesFrom entries reading the revision Secret's key into the charts'
// checksum values, appended to the server's list after the values sources
// the fleet base and the user values name, and created for its Valkey's,
// whose fleet base names none. A rotation then rolls the server and its
// Valkey and nothing else; a reconcile without one changes no pod template.
func (k *kustomizationDoc) withRevision(s serverDefinition) {
	source := func(targetPath string) render.Map {
		return render.Map{e("kind", "Secret"), e("name", s.revisionSecretName()), e("valuesKey", revisionKey), e("targetPath", targetPath)}
	}
	var server []op
	for _, value := range s.checksumValues() {
		server = append(server, op{Op: opAdd, Path: valuesFromEntry, Value: source(value)})
	}
	k.patch(s.name, server)
	k.patch(s.name+"-valkey", []op{{Op: opAdd, Path: valuesFromPath, Value: []render.Map{source(valkeyChecksumValue)}}})
}

// kustomization renders a kustomize Kustomization listing resources.
func kustomization(resources ...string) []byte {
	return append([]byte(fileHeader), render.MustYAML(kustomizationDoc{
		APIVersion: kustomizationAPIVersion, Kind: kustomizationKind, Resources: resources,
	})...)
}
