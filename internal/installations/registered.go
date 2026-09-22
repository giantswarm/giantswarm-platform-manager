package installations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// What an installation registers with muster beyond the platform's own is
// read here from its management-clusters tree, once per inspection, and lands
// on the Report as installation facts (installation.mcpServers,
// installation.mcpClients): the MCPServer objects under
// extras/agent-platform/mcpservers/ and the MCP clients with a stable callback
// under extras/agent-platform/mcpclients/, each a ConfigMap naming the
// consumer's redirect URIs. A directory counts where the tree's kustomization
// lists it, and a file where the directory's kustomization lists it — what
// Flux applies, and nothing else.

// The directories of extras/agent-platform/ that carry registrations.
const (
	mcpServersDir = "mcpservers"
	mcpClientsDir = "mcpclients"
)

// The kinds a registration is.
const (
	mcpServerKind = "MCPServer"
	configMapKind = "ConfigMap"
)

// redirectURIsKey is the ConfigMap key a client registration names its
// redirect URIs under, one per line.
const redirectURIsKey = "redirectURIs"

// AgentPlatformExtrasPath is the platform's tree in the installation's
// management-clusters repository.
func AgentPlatformExtrasPath(name string) string {
	return "management-clusters/" + name + "/extras/agent-platform"
}

// RegisteredServer is an MCP server registered on the installation beyond
// the platform's own three, as the definitions' installation.mcpServers[*]
// names it: the object's name, where muster reaches it, how muster
// authenticates to it (none, forward, exchange, oauth) and, for exchange, the
// Dex the exchange runs at.
type RegisteredServer struct {
	Name             string `json:"name"`
	URL              string `json:"url"`
	Auth             string `json:"auth"`
	DexTokenEndpoint string `json:"dexTokenEndpoint,omitempty"`
}

// RegisteredClient is an MCP client registered on the installation with a
// stable callback, as installation.mcpClients[*] names it: the consumer and
// its redirect URIs.
type RegisteredClient struct {
	Name         string   `json:"name"`
	RedirectURIs []string `json:"redirectURIs"`
}

// Registered is what an installation's record registers with muster beyond
// the platform's own. The slices are never nil: a record without a directory
// registers nothing.
type Registered struct {
	Servers []RegisteredServer `json:"servers"`
	Clients []RegisteredClient `json:"clients"`
}

// The ways muster authenticates to a registered server, in the
// agent-platform-mcps chart's words.
const (
	authNone     = "none"
	authForward  = "forward"
	authExchange = "exchange"
	authOAuth    = "oauth"
)

// readRegistered reads inst's registrations as the person: the tree's
// kustomization, and for each registration directory it lists, the
// directory's kustomization and every file that lists. An installation
// without the tree registers nothing; a directory listed without its
// kustomization, a file that does not parse or an object that lacks what a
// registration needs is an error naming the file — the fact's error, not a
// condition of reading the installation.
func readRegistered(ctx context.Context, read Reader, inst Installation) (Registered, error) {
	reg := Registered{Servers: []RegisteredServer{}, Clients: []RegisteredClient{}}
	repo, tree := inst.Repositories.ManagementClusters, AgentPlatformExtrasPath(inst.Name)
	dirs, err := kustomizationDirectories(ctx, read, repo, tree+"/kustomization.yaml")
	if errors.Is(err, gh.ErrNotFound) {
		return reg, nil
	}
	if err != nil {
		return reg, err
	}
	for _, dir := range dirs {
		var parse func(path string, doc *yaml.Node) error
		switch dir {
		case mcpServersDir:
			parse = func(path string, doc *yaml.Node) error {
				s, ok, err := registeredServer(doc)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				if ok {
					reg.Servers = append(reg.Servers, s)
				}
				return nil
			}
		case mcpClientsDir:
			parse = func(path string, doc *yaml.Node) error {
				c, ok, err := registeredClient(doc)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				if ok {
					reg.Clients = append(reg.Clients, c)
				}
				return nil
			}
		default:
			continue
		}
		files, err := kustomizationFiles(ctx, read, repo, tree+"/"+dir+"/kustomization.yaml")
		if err != nil {
			return reg, err
		}
		for _, f := range files {
			path := tree + "/" + dir + "/" + f
			data, err := read(ctx, repo, path)
			if err != nil {
				return reg, fmt.Errorf("the registrations on record: %w", err)
			}
			if err := documents(data, func(doc *yaml.Node) error { return parse(path, doc) }); err != nil {
				return reg, fmt.Errorf("the registrations on record: %w", err)
			}
		}
	}
	return reg, nil
}

// kustomizationDirectories are the local directories a kustomization's
// resources list, by name: ./mcpservers, mcpservers/ and mcpservers all name
// mcpservers. A remote resource or a file is none.
func kustomizationDirectories(ctx context.Context, read Reader, repo, path string) ([]string, error) {
	entries, err := kustomizationResources(ctx, read, repo, path)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if isRemote(e) || isYAMLFile(e) {
			continue
		}
		dirs = append(dirs, strings.Trim(strings.TrimPrefix(e, "./"), "/"))
	}
	return dirs, nil
}

// kustomizationFiles are the local YAML files a kustomization's resources
// list, relative to its directory. A remote resource or a directory is none.
func kustomizationFiles(ctx context.Context, read Reader, repo, path string) ([]string, error) {
	entries, err := kustomizationResources(ctx, read, repo, path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if isRemote(e) || !isYAMLFile(e) {
			continue
		}
		files = append(files, strings.TrimPrefix(e, "./"))
	}
	return files, nil
}

// kustomizationResources are a kustomization's resources entries, in order.
func kustomizationResources(ctx context.Context, read Reader, repo, path string) ([]string, error) {
	data, err := read(ctx, repo, path)
	if err != nil {
		return nil, fmt.Errorf("the registrations on record: %w", err)
	}
	var k struct {
		Resources []string `yaml:"resources"`
	}
	if err := yaml.Unmarshal([]byte(data), &k); err != nil {
		return nil, fmt.Errorf("the registrations on record: %s: %w", path, err)
	}
	return k.Resources, nil
}

func isRemote(entry string) bool { return strings.Contains(entry, "://") }
func isYAMLFile(entry string) bool {
	return strings.HasSuffix(entry, ".yaml") || strings.HasSuffix(entry, ".yml")
}

// documents calls fn with every YAML document of data.
func documents(data string, fn func(doc *yaml.Node) error) error {
	dec := yaml.NewDecoder(bytes.NewReader([]byte(data)))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(&doc); err != nil {
			return err
		}
	}
}

// registeredServer reads one document as an MCPServer registration: false
// for a document of another kind, an error for an MCPServer without a name or
// a URL. The auth mode follows muster's spec: a token exchange enabled is
// exchange, an authorization server named is oauth, forwardToken is forward,
// anything else none.
func registeredServer(doc *yaml.Node) (RegisteredServer, bool, error) {
	var o struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Spec struct {
			URL  string `yaml:"url"`
			Auth struct {
				ForwardToken  bool `yaml:"forwardToken"`
				TokenExchange struct {
					Enabled          bool   `yaml:"enabled"`
					DexTokenEndpoint string `yaml:"dexTokenEndpoint"`
				} `yaml:"tokenExchange"`
				AuthorizationServer struct {
					Issuer string `yaml:"issuer"`
				} `yaml:"authorizationServer"`
			} `yaml:"auth"`
		} `yaml:"spec"`
	}
	if err := doc.Decode(&o); err != nil {
		return RegisteredServer{}, false, err
	}
	if o.Kind != mcpServerKind {
		return RegisteredServer{}, false, nil
	}
	if o.Metadata.Name == "" || o.Spec.URL == "" {
		return RegisteredServer{}, false, fmt.Errorf("MCPServer %q: metadata.name and spec.url are required", o.Metadata.Name)
	}
	s := RegisteredServer{Name: o.Metadata.Name, URL: o.Spec.URL, Auth: authNone}
	switch {
	case o.Spec.Auth.TokenExchange.Enabled:
		s.Auth, s.DexTokenEndpoint = authExchange, o.Spec.Auth.TokenExchange.DexTokenEndpoint
	case o.Spec.Auth.AuthorizationServer.Issuer != "":
		s.Auth = authOAuth
	case o.Spec.Auth.ForwardToken:
		s.Auth = authForward
	}
	return s, true, nil
}

// registeredClient reads one document as an MCP client registration: false
// for a document of another kind, an error for a ConfigMap without a name or
// without redirect URIs (data.redirectURIs, one per line, HTTPS).
func registeredClient(doc *yaml.Node) (RegisteredClient, bool, error) {
	var o struct {
		Kind     string `yaml:"kind"`
		Metadata struct {
			Name string `yaml:"name"`
		} `yaml:"metadata"`
		Data map[string]string `yaml:"data"`
	}
	if err := doc.Decode(&o); err != nil {
		return RegisteredClient{}, false, err
	}
	if o.Kind != configMapKind {
		return RegisteredClient{}, false, nil
	}
	c := RegisteredClient{Name: o.Metadata.Name}
	for _, line := range strings.Split(o.Data[redirectURIsKey], "\n") {
		if uri := strings.TrimSpace(line); uri != "" {
			c.RedirectURIs = append(c.RedirectURIs, uri)
		}
	}
	if c.Name == "" || len(c.RedirectURIs) == 0 {
		return RegisteredClient{}, false, fmt.Errorf("ConfigMap %q: metadata.name and data.%s (one HTTPS redirect URI per line) are required", c.Name, redirectURIsKey)
	}
	for _, uri := range c.RedirectURIs {
		if !strings.HasPrefix(uri, "https://") {
			return RegisteredClient{}, false, fmt.Errorf("ConfigMap %q: redirect URI %q is not HTTPS", c.Name, uri)
		}
	}
	return c, true, nil
}
