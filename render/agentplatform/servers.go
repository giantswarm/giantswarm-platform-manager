package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

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
	// built-in client (it does for muster and mcpKubernetes); where it does
	// not, the client keeps its inline secret and no reference is rendered.
	dexSecretRef bool
}

// servers are the platform's own MCP servers, the set the shared template
// registers with muster on every installation.
var servers = []serverDefinition{
	{name: "mcp-kubernetes", group: "kubernetes", dexClient: "mcpKubernetes", dexSecretRef: true},
	{name: "mcp-prometheus", group: "prometheus", dexClient: "mcpPrometheus"},
	{name: "mcp-capi", group: "capi", dexClient: "mcpCapi"},
}

const (
	// dexNamespace is where the installation's Dex runs and reads client Secrets.
	dexNamespace = "giantswarm"
	// dexSecretKey is the key every Dex client Secret carries.
	dexSecretKey = "secret"
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
	return "dex-client-" + component
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
// fleet base and the Secrets its chart reads — the OAuth credentials
// (dex-client-secret shared with the Dex client, oauth-encryption-key) and the
// Valkey password — plus, where the dex-app chart reads a reference for the
// client, the Dex-side copy of the client secret.
func (s serverDefinition) extras(result *render.Result, repo render.Repository, dir string) {
	valueName := s.name + "-dex-client-secret"
	resources := []string{basesRepository + s.name + "?ref=main", "oauth-credentials.enc.yaml", "valkey-credentials.enc.yaml"}
	if s.dexSecretRef {
		resources = append(resources, dexClientSecretFile(s.name))
		result.Add(repo, dir+"/"+dexClientSecretFile(s.name), dexClientSecret(s.name, valueName))
	}
	result.Add(repo, dir+"/kustomization.yaml", render.File{Content: kustomization(resources...)})
	result.Add(repo, dir+"/oauth-credentials.enc.yaml", render.Secret(s.name+"-oauth-credentials", s.name, nil,
		render.GeneratedKey("dex-client-secret", valueName, render.Base64, 32),
		render.GeneratedKey("oauth-encryption-key", s.name+"-oauth-encryption-key", render.Base64, 32),
	))
	result.Add(repo, dir+"/valkey-credentials.enc.yaml", render.Secret(s.name+"-valkey-auth", s.name, nil,
		render.GeneratedKey("default", s.name+"-valkey-password", render.Alphanumeric, 32),
	))
}

// kustomization renders a kustomize Kustomization listing resources.
func kustomization(resources ...string) []byte {
	type k struct {
		APIVersion string   `yaml:"apiVersion"`
		Kind       string   `yaml:"kind"`
		Resources  []string `yaml:"resources"`
	}
	return append([]byte(fileHeader), render.MustYAML(k{
		APIVersion: "kustomize.config.k8s.io/v1beta1", Kind: "Kustomization", Resources: resources,
	})...)
}
