// Package clustermcpservers is the cluster-mcp-servers definition's render:
// a management cluster's own MCP servers on an installation without the agent
// platform — each server's extras directory over the fleet base, with its
// credentials, its Dex client's Secret and the credentials revision a
// rotation restarts the server and its Valkey on (render/mcpservers, the files
// the agent-platform definition renders for the same servers), and each
// server's Dex client as a referenced Secret in the dex-app configmap patch,
// a file whose other clients and keys stay their owners'.
package clustermcpservers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Masterminds/semver/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/mcpservers"
)

// Capability is the definition's name, its directory under definitions/.
const Capability = "cluster-mcp-servers"

// Errors the render refuses with, each a render.Refusal naming the input.
var (
	// ErrInput is an input the schema rejects: an unknown key, a missing
	// required one, a value of the wrong shape.
	ErrInput = errors.New(Capability + ": input")
	// ErrPolicy is an installation the definition does not render for: the
	// agent platform renders its servers, or its dex-app cannot take a
	// rotated client secret.
	ErrPolicy = errors.New(Capability + ": policy")
	// ErrUnknownSecret is a supplied secret value: the definition asks for none.
	ErrUnknownSecret = errors.New(Capability + ": unknown secret value")
)

// DexAppRotation is the first dex-app that restarts Dex when a referenced
// client Secret changes. Before it, Dex keeps the secret it started with, so a
// rotation of a server's Dex client secret fails the server's sign-ins until a
// hand-run restart.
const DexAppRotation = "3.2.3"

const (
	// fluxNamespace is where the servers' HelmReleases live.
	fluxNamespace = "flux-giantswarm"
	// fileHeader opens every rendered YAML file.
	fileHeader = "# Rendered by giantswarm-platform-manager, cluster-mcp-servers definition. Do not edit by hand:\n# the next reconcile writes it again from the installation's inputs.\n"
)

// The live dimensions of features.yaml the probes report under, by feature.
const (
	featureServers  = "servers"
	featureIdentity = "identity"
)

// Input is the typed form of definitions/cluster-mcp-servers/schema.json.
type Input struct {
	Installation Installation `json:"installation"`
	Servers      Servers      `json:"servers"`
}

// Installation is the facts on record; see the schema for each field.
type Installation struct {
	Name          string `json:"name"`
	BaseDomain    string `json:"baseDomain"`
	Customer      string `json:"customer"`
	DexAppVersion string `json:"dexAppVersion"`
	AgentPlatform bool   `json:"agentPlatform"`
}

// Servers are how the installation runs each of its MCP servers, by the
// server's Dex client key.
type Servers struct {
	MCPKubernetes Server `json:"mcpKubernetes"`
	MCPPrometheus Server `json:"mcpPrometheus"`
	MCPCapi       Server `json:"mcpCapi"`
}

// Server is how the installation runs one MCP server; see the schema.
type Server struct {
	// Enabled is nil where neither the record nor the person says: a fresh
	// enable runs every server.
	Enabled     *bool `json:"enabled"`
	PrivateURLs bool  `json:"privateURLs"`
	PrivateIPs  bool  `json:"privateIPs"`
	DexCA       bool  `json:"dexCA"`
}

// runs says whether the server runs on the installation.
func (s Server) runs() bool { return s.Enabled == nil || *s.Enabled }

// of is the input of the server by its Dex client key.
func (s Servers) of(server mcpservers.Server) Server {
	switch server.DexClient {
	case "mcpKubernetes":
		return s.MCPKubernetes
	case "mcpPrometheus":
		return s.MCPPrometheus
	case "mcpCapi":
		return s.MCPCapi
	}
	panic("clustermcpservers: no input for " + server.Name)
}

// Parse reads the decoded input document against the schema.
func Parse(raw any) (*Input, error) {
	schemaBytes, err := definitions.FS.ReadFile(Capability + "/schema.json")
	if err != nil {
		return nil, err
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: schema: %w", Capability, err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaDoc); err != nil {
		return nil, fmt.Errorf("%s: schema: %w", Capability, err)
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("%s: schema: %w", Capability, err)
	}
	// One JSON round trip normalises what a YAML decoder produces into what
	// the validator and encoding/json expect.
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	var in Input
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	return &in, nil
}

func refuse(kind error, sentence string) error {
	return &render.Refusal{Kind: kind, Sentence: sentence}
}

func describe(field string) string { return render.Describe(Capability, field) }

// check refuses an installation the definition does not render for, and any
// supplied value.
func (in *Input) check(secrets map[string]string) error {
	if in.Installation.AgentPlatform {
		return refuse(ErrPolicy, "the agent platform is enabled on "+in.Installation.Name+" and its definition renders the installation's MCP servers; reconcile agent-platform instead")
	}
	if v := in.Installation.DexAppVersion; v != "" {
		version, err := semver.NewVersion(v)
		if err != nil {
			return refuse(ErrInput, describe("installation.dexAppVersion")+" "+v+" is no version")
		}
		if version.LessThan(semver.MustParse(DexAppRotation)) {
			return refuse(ErrPolicy, "dex-app "+v+" on "+in.Installation.Name+" keeps a rotated client secret until Dex restarts by hand; the servers' Dex clients need dex-app "+DexAppRotation+" or later, which restarts Dex when their Secrets change")
		}
	}
	for field := range secrets {
		return refuse(ErrUnknownSecret, describe(field)+" is no value this definition asks for; it generates every credential")
	}
	if !in.runsAny() {
		return refuse(ErrInput, "no MCP server runs on "+in.Installation.Name+" (servers.*.enabled): the definition has nothing to render")
	}
	return nil
}

// runsAny says whether any server runs.
func (in *Input) runsAny() bool {
	for _, s := range mcpservers.Servers {
		if in.Servers.of(s).runs() {
			return true
		}
	}
	return false
}

// Render renders the fileset of the installation's MCP servers. Every
// credential is a placeholder the commit step generates; the definition asks
// for no supplied value, and the mode takes no part.
func Render(raw any, secrets map[string]string, _ render.Mode) (*render.Result, error) {
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
	extras := "management-clusters/" + name + "/extras/"

	r := &render.Result{}
	static := render.Map{}
	for _, s := range mcpservers.Servers {
		server := in.Servers.of(s)
		if !server.runs() {
			continue
		}
		s.Extras(r, clusters, extras+s.Name, mcpservers.Options{Installation: name,
			PrivateURLs: server.PrivateURLs, PrivateIPs: server.PrivateIPs, DexCA: server.DexCA, Header: fileHeader})
		r.Include(clusters, extras+"kustomization.yaml", "./"+s.Name+"/")
		if s.DexSecretRef {
			static = append(static, render.Entry{Key: s.DexClient, Value: s.DexClientRef()})
		}
	}
	dex := render.Map{{Key: "oidc", Value: render.Map{{Key: "staticClients", Value: static}}}}
	r.Add(configs, "installations/"+name+"/apps/dex-app/configmap-values.yaml.patch", render.File{Content: append([]byte(fileHeader), render.MustYAML(dex)...)})
	r.Probes = in.probes()
	return r, nil
}

// probes are the live reads of the installation: every running server's and
// its Valkey's HelmRelease Ready, the server's Deployment Available (a
// HelmRelease stays Ready when its pods turn unready on rotated-away
// credentials), and Dex started after every server's client Secret changed.
func (in *Input) probes() []render.Probe {
	var p []render.Probe
	for _, s := range mcpservers.Servers {
		if !in.Servers.of(s).runs() {
			continue
		}
		for _, hr := range []string{s.Name, s.Name + "-valkey"} {
			p = append(p, render.Probe{ID: "live-helmreleases-ready", Feature: featureServers, Kind: render.HelmReleaseReady, Namespace: fluxNamespace, Resource: "HelmRelease", Name: hr})
		}
		p = append(p, render.Probe{ID: "live-server-workloads", Feature: featureServers, Kind: render.Condition, Namespace: s.Name, Resource: "Deployment", Name: s.Name,
			Expect: render.Expectation{Condition: "Available", ConditionStatus: "True"}})
		if s.DexSecretRef {
			p = append(p, render.DexSecretLoadedProbe("live-dex-client-secrets-loaded", featureIdentity, render.DexNamespace, render.DexClientSecretName(s.Name), s.DexClient))
		}
	}
	return p
}

// The render.Input contract: the definition asks for no supplied value, no
// required person input can be missing, the shared template names the
// servers' Dex client ids, and no action is the customer's.

func (in *Input) SuppliedMarkers() map[string]string       { return nil }
func (in *Input) SuppliedSecretFields() []string           { return nil }
func (in *Input) MissingInputs() []string                  { return nil }
func (in *Input) BuiltInDexClientID(string) string         { return "" }
func (in *Input) CustomerActions() []render.CustomerAction { return nil }
func (in *Input) Selected() map[string]any                 { return nil }
