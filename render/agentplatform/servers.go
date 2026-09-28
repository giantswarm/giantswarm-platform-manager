package agentplatform

import (
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/mcpservers"
)

// servers are the platform's own MCP servers, the set the shared template
// registers with muster on every installation (render/mcpservers renders
// their extras).
var servers = mcpservers.Servers

// The credentials revision's key, size and the Valkey chart's mark, shared
// with muster's revision.
const (
	revisionKey         = mcpservers.RevisionKey
	revisionLength      = mcpservers.RevisionLength
	valkeyChecksumValue = mcpservers.ValkeyChecksumValue
)

// serverOptions are how this installation runs its servers: Dex on private
// addresses and the clients' too on a private installation.
func (in *Input) serverOptions() mcpservers.Options {
	return mcpservers.Options{Installation: in.Installation.Name, PrivateURLs: in.Installation.Private, PrivateIPs: in.Installation.Private, Header: fileHeader}
}

const (
	// dexNamespace is where the installation's Dex runs and reads client Secrets.
	dexNamespace = render.DexNamespace
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

// dexClientSecretFile is that Secret's file name.
func dexClientSecretFile(component string) string { return render.DexClientSecretFile(component) }

// dexClientSecret renders the Dex-side Secret of a client whose secret is the
// generated value valueName, shared with the workload's own Secret.
func dexClientSecret(component, valueName string) render.File {
	return render.DexClientSecret(component, valueName)
}

// mcpServerEntry is the server's entry in muster's list, the template's shape.
func mcpServerEntry(s mcpservers.Server, installation string) MCPServer {
	return MCPServer{
		Cluster: installation,
		Group:   s.Group,
		URL:     "http://" + s.Name + "." + s.Name + ".svc:8080/mcp",
		Timeout: 30,
		Auth:    MCPAuth{Mode: "forward"},
	}
}

// The kustomize Kustomization's API version and kind.
const kustomizationAPIVersion, kustomizationKind = mcpservers.KustomizationAPIVersion, mcpservers.KustomizationKind

// kustomizationDoc is a kustomize Kustomization listing resources.
type kustomizationDoc struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Resources  []string `yaml:"resources"`
}

// kustomization renders a kustomize Kustomization listing resources.
func kustomization(resources ...string) []byte {
	return append([]byte(fileHeader), render.MustYAML(kustomizationDoc{
		APIVersion: kustomizationAPIVersion, Kind: kustomizationKind, Resources: resources,
	})...)
}
