// Package mcpservers renders a management cluster's own MCP servers
// (mcp-kubernetes, mcp-prometheus, mcp-capi) over the fleet's bases: each
// server's extras directory with the Secrets its chart reads, the Dex-side
// copy of its client secret, and the credentials revision a rotation rolls
// the server and its Valkey on. The agent-platform definition renders them on
// an installation with the platform, the cluster-mcp-servers definition on
// every other management cluster that runs them; both render the same files.
package mcpservers

import (
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Server is one MCP server of the installation itself, as data: the fleet
// base its extras reference, the Secrets its chart reads and their key
// contract, the Dex client the dex-app chart renders for it, and its muster
// tool group.
type Server struct {
	// Name is the component and namespace: mcp-kubernetes, mcp-prometheus, mcp-capi.
	Name string
	// Group is the muster tool group.
	Group string
	// DexClient is the dex-app built-in static client the shared template
	// renders id and redirect URI for; the definition adds the secret reference.
	DexClient string
	// DexSecretRef says whether the dex-app chart reads clientSecretRef on that
	// built-in client: it does for every server since dex-app 3.2.0, the
	// version the definitions target (below it mcpPrometheus and mcpCapi were
	// no built-ins and their keys were dropped). Where it is false, the client
	// keeps its inline secret and no reference is rendered.
	DexSecretRef bool
	// oauthKeys is the key contract of the server's oauth-credentials Secret:
	// the names its chart reads the values by.
	oauthKeys oauthSecretKeys
	// valuesKey is the top-level key the server's chart reads its values
	// under: mcp-kubernetes has its own, mcp-prometheus the fleet base's app,
	// mcp-capi reads them at the root (empty).
	valuesKey string
	// ssoPrivateIPs says whether the chart has a separate flag for the SSO
	// forwarded-token JWKS fetch reaching private addresses (oauth.sso.allowPrivateIPs)
	// and the private-address flags of the clients' metadata documents and
	// redirect URIs.
	ssoPrivateIPs bool
	// dexCA is where the chart reads the Secret with the CA of a Dex served
	// with a certificate from the installation's private CA, under oauth.
	dexCA dexCAValues
}

// dexCAValues names the chart values of the Dex CA Secret: a chart that
// reads name and key under oauth.dex.caSecret, or one that reads the name
// alone under oauth.dexCASecret (the key is ca.crt).
type dexCAValues int

const (
	dexCASecretWithKey dexCAValues = iota
	dexCASecretName
)

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
	// revision carries the server's credentials revision (the revision
	// Secret): a key no chart reads, held here so a rewrite of this Secret
	// draws the revision anew.
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

// Servers are the management cluster's own MCP servers, the set the shared
// template registers with muster on every installation.
var Servers = []Server{
	{Name: "mcp-kubernetes", Group: "kubernetes", DexClient: "mcpKubernetes", DexSecretRef: true, oauthKeys: keyedOAuthKeys, valuesKey: "mcpKubernetes", ssoPrivateIPs: true},
	{Name: "mcp-prometheus", Group: "prometheus", DexClient: "mcpPrometheus", DexSecretRef: true, oauthKeys: envOAuthKeys, valuesKey: "app", dexCA: dexCASecretName},
	{Name: "mcp-capi", Group: "capi", DexClient: "mcpCapi", DexSecretRef: true, oauthKeys: keyedOAuthKeys},
}

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
	// RevisionKey is the key the revision is read by: in the valkey-auth
	// Secret beside the password, and in the revision Secret the HelmReleases read.
	RevisionKey = "revision"
	// RevisionLength is the size of the revision, an Alphanumeric value like
	// the Valkey password.
	RevisionLength = 32
	// RevisionFile is the revision Secret's file in the server's extras
	// directory; it matches the fleet's .sops.yaml rules (.*(secret|credential).*)
	// and the commit step's secret-file test like the other two.
	RevisionFile = "credentials-revision.enc.yaml" // #nosec G101 -- a file name, not a value
	// ValkeyChecksumValue is the Valkey chart's mark for a users Secret it
	// cannot read, rendered as the pod template's checksum annotation
	// (giantswarm/valkey-app: valkey.auth.usersExistingSecretChecksum).
	ValkeyChecksumValue = "valkey.auth.usersExistingSecretChecksum"
)

const (
	// fluxNamespace is where the HelmReleases and their values sources live.
	fluxNamespace = "flux-giantswarm"
	// basesRepository is the fleet base the extras reference.
	basesRepository = "https://github.com/giantswarm/management-cluster-bases//extras/"
	// userValuesFile is the per-server values file the kustomization turns into a
	// ConfigMap the HelmRelease reads (valuesFrom).
	userValuesFile = "user-values.yaml"
	// DexCAFile is the installation's own Secret with the CA of its Dex, where
	// Dex is served with a certificate from the installation's private CA: the
	// kustomization lists it, the definitions never render it.
	DexCAFile = "dex-ca-secret.enc.yaml" // #nosec G101 -- a file name, not a value
	// dexCAKey is the key of the Dex CA Secret.
	dexCAKey = "ca.crt"
)

// RevisionSecretName is the Secret in the Flux namespace that carries the
// server's credentials revision for its HelmReleases.
func (s Server) RevisionSecretName() string { return s.Name + "-credentials-revision" }

// dexCASecretName is the installation's Secret with its Dex's CA, in the server's namespace.
func (s Server) dexCASecretName() string { return s.Name + "-dex-ca" }

// ChecksumValues are the chart values the server's HelmRelease takes the
// revision into: the OAuth credentials Secret's mark and, for a chart that
// reads the Valkey password from the valkey-auth Secret rather than from the
// OAuth Secret, that Secret's mark; each renders as a pod-template checksum
// annotation.
func (s Server) ChecksumValues() []string {
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

// DexClientRef is the server's Dex client in the dex-app configmap patch: its
// secret read from the Secret in Dex's namespace the extras carry.
func (s Server) DexClientRef() render.Map {
	return render.Map{e("clientSecretRef", render.Map{e("name", render.DexClientSecretName(s.Name)), e("key", render.DexSecretKey)})}
}

// Options are how one installation runs a server.
type Options struct {
	// Installation names the generated values: the commit step draws one
	// value per name across a pull request, and a wave commits several
	// installations of one organisation into one.
	Installation string
	// PrivateURLs says Dex is reached on private addresses: the OAuth issuer
	// URL and, where the chart has the flag, the SSO forwarded-token JWKS
	// fetch may resolve to private IPs, in the server's user values.
	PrivateURLs bool
	// PrivateIPs says the clients' metadata documents and redirect URIs are on
	// private addresses too, where the chart has the flags (a private installation).
	PrivateIPs bool
	// DexCA says Dex is served with a certificate from the installation's
	// private CA: the kustomization lists the installation's Dex CA Secret
	// (DexCAFile) and the user values point the chart at it.
	DexCA bool
	// Header opens every rendered YAML file, naming the definition.
	Header string
}

// userValues are the server's user values, nil where the server needs none:
// Dex on private addresses and, with the installation's private CA, the Secret
// with that CA.
func (s Server) userValues(o Options) render.Map {
	if !o.PrivateURLs && !o.DexCA {
		return nil
	}
	var oauth render.Map
	if o.PrivateURLs {
		oauth = append(oauth, e("allowPrivateURLs", true))
		if s.ssoPrivateIPs {
			oauth = append(oauth, e("sso", render.Map{e("allowPrivateIPs", true)}))
			if o.PrivateIPs {
				oauth = append(oauth, e("cimd", render.Map{e("allowPrivateIPs", true)}),
					e("redirectURISecurity", render.Map{e("allowPrivateIPRedirectURIs", true)}))
			}
		}
	}
	if o.DexCA {
		switch s.dexCA {
		case dexCASecretWithKey:
			oauth = append(oauth, e("dex", render.Map{e("caSecret", render.Map{e("name", s.dexCASecretName()), e("key", dexCAKey)})}))
		case dexCASecretName:
			oauth = append(oauth, e("dexCASecret", render.Map{e("name", s.dexCASecretName())}))
		}
	}
	values := render.Map{e("oauth", oauth)}
	if s.valuesKey == "" {
		return values
	}
	return render.Map{e(s.valuesKey, values)}
}

// Extras renders the server's extras directory: the kustomization over the
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
// With user values (Options) the directory also carries them and the
// kustomization turns them into a ConfigMap the HelmRelease reads.
func (s Server) Extras(result *render.Result, repo render.Repository, dir string, o Options) {
	generatedName := func(base string) string { return o.Installation + "-" + s.Name + "-" + base }
	valueName := generatedName("dex-client-secret")
	valkeyValue := generatedName("valkey-password")
	revision := generatedName("credentials-revision")
	resources := []string{basesRepository + s.Name + "?ref=main", "oauth-credentials.enc.yaml", "valkey-credentials.enc.yaml", RevisionFile}
	if s.DexSecretRef {
		resources = append(resources, render.DexClientSecretFile(s.Name))
		result.Add(repo, dir+"/"+render.DexClientSecretFile(s.Name), render.DexClientSecret(s.Name, valueName))
	}
	if o.DexCA {
		resources = append(resources, DexCAFile)
	}
	k := kustomizationDoc{APIVersion: KustomizationAPIVersion, Kind: KustomizationKind, Resources: resources}
	if values := s.userValues(o); values != nil {
		result.Add(repo, dir+"/"+userValuesFile, render.File{Content: append([]byte(o.Header), render.MustYAML(values)...)})
		k.withUserValues(s.Name)
	}
	k.withRevision(s)
	result.Add(repo, dir+"/kustomization.yaml", render.File{Content: append([]byte(o.Header), render.MustYAML(k)...)})
	oauth := []render.SecretKey{
		render.GeneratedKey(s.oauthKeys.dexClientSecret, valueName, render.Base64, 32),
		render.GeneratedKey(s.oauthKeys.encryptionKey, generatedName("oauth-encryption-key"), render.Base64, 32),
	}
	if s.oauthKeys.valkeyPassword != "" {
		oauth = append(oauth, render.GeneratedKey(s.oauthKeys.valkeyPassword, valkeyValue, render.Alphanumeric, 32))
	}
	oauth = append(oauth, render.GeneratedKey(s.oauthKeys.revision, revision, render.Alphanumeric, RevisionLength))
	valkey := []render.SecretKey{
		render.GeneratedKey(valkeyAuthKey, valkeyValue, render.Alphanumeric, 32),
		render.GeneratedKey(RevisionKey, revision, render.Alphanumeric, RevisionLength),
	}
	result.Add(repo, dir+"/oauth-credentials.enc.yaml", render.Secret(s.Name+"-oauth-credentials", s.Name, nil, oauth...))
	result.Add(repo, dir+"/valkey-credentials.enc.yaml", render.Secret(s.Name+"-valkey-auth", s.Name, nil, valkey...))
	// Every value of the two Secrets rolls the server and its Valkey with the revision.
	result.Revision(revision, oauth...)
	result.Revision(revision, valkey...)
	result.Add(repo, dir+"/"+RevisionFile, render.Secret(s.RevisionSecretName(), fluxNamespace, nil,
		render.GeneratedKey(RevisionKey, revision, render.Alphanumeric, RevisionLength),
	))
}

// The kustomize Kustomization's API version and kind.
const KustomizationAPIVersion, KustomizationKind = "kustomize.config.k8s.io/v1beta1", "Kustomization"

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
func (k *kustomizationDoc) withRevision(s Server) {
	source := func(targetPath string) render.Map {
		return render.Map{e("kind", "Secret"), e("name", s.RevisionSecretName()), e("valuesKey", RevisionKey), e("targetPath", targetPath)}
	}
	var server []op
	for _, value := range s.ChecksumValues() {
		server = append(server, op{Op: opAdd, Path: valuesFromEntry, Value: source(value)})
	}
	k.patch(s.Name, server)
	k.patch(s.Name+"-valkey", []op{{Op: opAdd, Path: valuesFromPath, Value: []render.Map{source(ValkeyChecksumValue)}}})
}

func e(key string, value any) render.Entry { return render.Entry{Key: key, Value: value} }
