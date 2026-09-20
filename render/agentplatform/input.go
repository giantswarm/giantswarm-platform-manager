package agentplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"text/template"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// Errors the render refuses with. Every message names the key, field or
// component concerned.
var (
	// ErrInput is an input the schema rejects (an unknown key, a missing
	// required one, a value of the wrong shape) or a record the definition
	// cannot render as it stands.
	ErrInput = errors.New("agent-platform: input")
	// ErrEmptySecret is a supplied secret value the render needs and did not
	// get, or got empty.
	ErrEmptySecret = errors.New("agent-platform: empty secret value")
	// ErrUnknownSecret is a supplied secret value no input asks for.
	ErrUnknownSecret = errors.New("agent-platform: unknown secret value")
	// ErrPolicy is a policy.yaml the renderer cannot apply.
	ErrPolicy = errors.New("agent-platform: fleet policy")
)

// The components of the definition beside muster and its servers.
const (
	componentKagent         = "kagent"
	componentAgentManager   = "agent-manager"
	componentKlausGateway   = "klaus-gateway"
	componentClusterManager = "cluster-manager"
)

// organisationComponents are the components policy.yaml lists per
// organisation. The chat gateway is not among them: it follows the
// installation's Slack app (klausGateway.installations).
var organisationComponents = []string{componentKagent, componentAgentManager, componentClusterManager}

// lineFourComponents are the components the meta chart carries on its 4 line
// only: the 3 line's values schema has no key for them and refuses a patch
// that names them. A record on the 3 line whose policy grants one is refused
// at plan time instead (checkRecord).
var lineFourComponents = []string{componentClusterManager}

// The meta chart lines a record selects (installation.chartLine): the 4 line
// carries every component and the serving slice; the 3 line runs the base's
// range and the managers' own OAuth sections.
const (
	lineThree = "3"
	lineFour  = "4"
)

// The referenced Secrets every installation names alike.
const (
	musterOAuthSecret  = "muster-oauth-credentials"  // #nosec G101 -- a Secret name, not a value
	musterValkeySecret = "muster-valkey-credentials" // #nosec G101 -- a Secret name, not a value
)

// Input is the resolved inputs of one installation: the record
// (definitions/agent-platform/schema.json's installation.*, read from the
// registry and the repositories), the fleet policy (policy.yaml) applied to
// it, and the one choice a person makes. The renderer reads nothing else.
type Input struct {
	Installation Installation
	// ModelServing is the person's choice: the meta chart's serving slice.
	ModelServing bool
	// Components are the components the policy gives the installation, by
	// name: its organisation's list and, where the policy names a Slack app
	// for the installation, the chat gateway.
	Components map[string]bool
	// Gateway is the chat gateway's shape, where the policy runs it.
	Gateway GatewayPolicy
	// Connector is the connector every target's Dex registers for this hub.
	Connector string
	// Teleport is the Teleport cluster a tunnel joins.
	Teleport Teleport
}

// Installation is the record; see the schema for each field.
type Installation struct {
	Name           string `json:"name"`
	BaseDomain     string `json:"baseDomain"`
	Customer       string `json:"customer"`
	Provider       string `json:"provider"`
	Private        bool   `json:"private"`
	ChartLine      string `json:"chartLine"`
	MusterClientID string `json:"musterClientId"`
	// PodCertificateRequest says the cluster serves certificates.k8s.io/v1beta1
	// PodCertificateRequest, which Agent Substrate needs on the 4 line: the
	// cluster App on record enables the feature gates, or its chart does by
	// default (substrate.go).
	PodCertificateRequest bool        `json:"podCertificateRequest"`
	Portals               []PortalRef `json:"portals"`
	Federation            Federation  `json:"federation"`
}

// PortalRef is a developer portal that signs people in on the installation:
// its host, the host's organisation and its hostname.
type PortalRef struct {
	Installation string `json:"installation"`
	Customer     string `json:"customer"`
	Domain       string `json:"domain"`
}

// Federation is the installation's place in the fleet's token exchange.
type Federation struct {
	Hubs           []string `json:"hubs"`
	Targets        []Target `json:"targets"`
	BrokerClientID string   `json:"brokerClientId"`
}

// Target is an installation a hub brokers for.
type Target struct {
	Installation string `json:"installation"`
	BaseDomain   string `json:"baseDomain"`
	Private      bool   `json:"private"`
	// AgentPlatform says the target runs the agent platform: a private one
	// is then also tunnelled to its kagent and its agentgateway.
	AgentPlatform bool `json:"agentPlatform"`
}

// groups are the federated MCP server groups of every target: the target's
// own three servers, the set the shared template registers everywhere.
func (t Target) groups() []string {
	groups := make([]string, 0, len(servers))
	for _, s := range servers {
		groups = append(groups, s.group)
	}
	return groups
}

// Teleport is the Teleport cluster the tunnel joins.
type Teleport struct {
	ClusterName string `yaml:"clusterName"`
	ProxyAddr   string `yaml:"proxyAddr"`
}

// GatewayPolicy is policy.yaml's klausGateway block: the installations with a
// Slack app, where the gateway runs, and its shape, one for all of them.
type GatewayPolicy struct {
	// Installations are the installations a Slack app exists for — the one
	// entry of the policy keyed by installation, a fact of each rather than a
	// tuning. The gateway renders there and nowhere else.
	Installations []string `yaml:"installations"`
	Slack         struct {
		Mode        string `yaml:"mode"`
		ChannelMode string `yaml:"channelMode"`
	} `yaml:"slack"`
	OBO struct {
		Connectors bool `yaml:"connectors"`
	} `yaml:"obo"`
	A2A struct {
		Enabled      bool   `yaml:"enabled"`
		DefaultAgent string `yaml:"defaultAgent"`
	} `yaml:"a2a"`
	Reviews struct {
		Enabled        bool     `yaml:"enabled"`
		Audience       string   `yaml:"audience"`
		AllowedCallers []string `yaml:"allowedCallers"`
	} `yaml:"reviews"`
}

// MCPServer is one server registered with muster, in the mcps chart's shape.
type MCPServer struct {
	Name       string  `json:"name" yaml:"name,omitempty"`
	Cluster    string  `json:"cluster" yaml:"cluster"`
	Group      string  `json:"group" yaml:"group"`
	URL        string  `json:"url" yaml:"url"`
	Timeout    int     `json:"timeout" yaml:"timeout,omitempty"`
	ToolPrefix string  `json:"toolPrefix" yaml:"toolPrefix,omitempty"`
	Auth       MCPAuth `json:"auth" yaml:"auth"`
}

// MCPAuth is how muster authenticates to a server.
type MCPAuth struct {
	Mode     string `json:"mode" yaml:"mode"`
	Provider string `json:"provider" yaml:"provider,omitempty"`
}

// policy is definitions/agent-platform/policy.yaml.
type policy struct {
	Components struct {
		Default   []string            `yaml:"default"`
		Customers map[string][]string `yaml:"customers"`
	} `yaml:"components"`
	KlausGateway GatewayPolicy `yaml:"klausGateway"`
	Federation   struct {
		Connector string   `yaml:"connector"`
		Teleport  Teleport `yaml:"teleport"`
	} `yaml:"federation"`
}

func loadPolicy() (*policy, error) {
	raw, err := definitions.FS.ReadFile("agent-platform/policy.yaml")
	if err != nil {
		return nil, err
	}
	var pol policy
	if err := yaml.Unmarshal(raw, &pol); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicy, err)
	}
	return &pol, nil
}

// components are the components the policy gives an installation: its
// organisation's list and, where klausGateway.installations names the
// installation, the chat gateway. An organisation's list naming the gateway is
// refused: the gateway follows a Slack app, which an installation has or not.
func (p *policy) components(inst Installation) (map[string]bool, error) {
	list, listed := p.Components.Customers[inst.Customer]
	if !listed {
		list = p.Components.Default
	}
	out := map[string]bool{}
	for _, c := range list {
		if c == componentKlausGateway {
			return nil, fmt.Errorf("%w: components: the chat gateway follows the installation's Slack app; an installation with one is named under klausGateway.installations", ErrPolicy)
		}
		if !slices.Contains(organisationComponents, c) {
			return nil, fmt.Errorf("%w: components: %q is not a component the definition renders", ErrPolicy, c)
		}
		out[c] = true
	}
	if slices.Contains(p.KlausGateway.Installations, inst.Name) {
		out[componentKlausGateway] = true
	}
	return out, nil
}

// connector renders the hub connector's name from the record.
func (p *policy) connector(inst Installation) (string, error) {
	tpl, err := template.New("connector").Option("missingkey=error").Parse(p.Federation.Connector)
	if err != nil {
		return "", fmt.Errorf("%w: federation.connector: %w", ErrPolicy, err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, inst); err != nil {
		return "", fmt.Errorf("%w: federation.connector: %w", ErrPolicy, err)
	}
	return b.String(), nil
}

// document is the input document as the schema shapes it.
type document struct {
	Installation Installation `json:"installation"`
	ModelServing struct {
		Enabled bool `json:"enabled"`
	} `json:"modelServing"`
}

// Parse validates raw against the schema and resolves the inputs: the record
// as given, the policy applied to its organisation, the choice as made. raw is
// the decoded document (from YAML or JSON): map[string]any at the top. A key
// the schema does not know, a missing required key or a wrong shape is
// ErrInput naming the location; a record the definition cannot render as it
// stands (a hub without its broker client, a private target on a hub without a
// published service-account issuer, the serving slice or a component of the 4
// chart line on a record that selects the 3 line, kagent on the 4 line where
// the cluster does not serve PodCertificateRequest) is ErrInput too.
func Parse(raw any) (*Input, error) {
	schemaBytes, err := definitions.FS.ReadFile("agent-platform/schema.json")
	if err != nil {
		return nil, err
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("agent-platform: schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaDoc); err != nil {
		return nil, fmt.Errorf("agent-platform: schema: %w", err)
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("agent-platform: schema: %w", err)
	}
	// One JSON round trip normalises the numbers and maps a YAML decoder
	// produces into what the validator and encoding/json expect.
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
	var d document
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	pol, err := loadPolicy()
	if err != nil {
		return nil, err
	}
	in := &Input{Installation: d.Installation, ModelServing: d.ModelServing.Enabled, Gateway: pol.KlausGateway, Teleport: pol.Federation.Teleport}
	if in.Components, err = pol.components(in.Installation); err != nil {
		return nil, err
	}
	if in.Connector, err = pol.connector(in.Installation); err != nil {
		return nil, err
	}
	if err := in.checkRecord(); err != nil {
		return nil, err
	}
	return in, nil
}

// checkRecord refuses a record the definition cannot render as it stands.
func (in *Input) checkRecord() error {
	fed := in.Installation.Federation
	if len(fed.Targets) > 0 && fed.BrokerClientID == "" {
		return fmt.Errorf("%w: installation.federation.brokerClientId: a hub's broker client is registered once and read back from its patch; none is on record", ErrInput)
	}
	if in.hasPrivateTarget() && in.serviceAccountIssuer() == "" {
		return fmt.Errorf("%w: installation.federation.targets: a private target's tunnel joins Teleport by this hub's published service-account issuer, and a %s installation publishes none the definition knows", ErrInput, in.Installation.Provider)
	}
	if in.ModelServing && in.Installation.ChartLine != lineFour {
		return fmt.Errorf("%w: modelServing.enabled: the serving slice is the 4 chart line's; this installation runs the %s line", ErrInput, in.Installation.ChartLine)
	}
	for _, c := range lineFourComponents {
		if in.Components[c] && in.Installation.ChartLine != lineFour {
			return fmt.Errorf("%w: installation.chartLine: %s needs the platform's 4 chart line and the record selects the %s line; agentPlatform.kagentApiV2: true in installations/%s/config.yaml.patch selects 4", ErrInput, c, in.Installation.ChartLine, in.Installation.Name)
		}
	}
	if in.kagent() && in.Installation.ChartLine == lineFour && !in.Installation.PodCertificateRequest {
		return fmt.Errorf("%w: installation.podCertificateRequest: kagent's Agent Substrate on the 4 chart line needs a cluster that serves %s/%s %s, and the record does not say this one does; enable the feature gates %s under cluster.internal.advancedConfiguration.{%s}.featureGates in the cluster App's values (management-clusters/%s/cluster-app-manifests.yaml), or run a cluster App chart that enables them by default (%s and later)",
			ErrInput, apiGroup(PodCertificateRequestResource), PodCertificateRequestVersion, apiResource(PodCertificateRequestResource), strings.Join(PodCertificateRequestGates, ", "), strings.Join(PodCertificateRequestComponents, ","), in.Installation.Name, podCertificateRequestDefaults())
	}
	return nil
}

// apiResource and apiGroup split a probe's resource.group.
func apiResource(resource string) string { r, _, _ := strings.Cut(resource, "."); return r }
func apiGroup(resource string) string    { _, g, _ := strings.Cut(resource, "."); return g }

// hasPrivateTarget says whether any federated target is reached through the tunnel.
func (in *Input) hasPrivateTarget() bool {
	return slices.ContainsFunc(in.Installation.Federation.Targets, func(t Target) bool { return t.Private })
}

func (in *Input) kagent() bool         { return in.Components[componentKagent] }
func (in *Input) agentManager() bool   { return in.Components[componentAgentManager] }
func (in *Input) klausGateway() bool   { return in.Components[componentKlausGateway] }
func (in *Input) clusterManager() bool { return in.Components[componentClusterManager] }

// portalHost is the installation whose management-clusters tree hosts the
// organisation's own portal — the one the platform's portal section is written
// into; empty when no portal of the organisation lists this installation (the
// hub's Dev Portal carries its own section for other organisations' installations).
func (in *Input) portalHost() string {
	for _, p := range in.Installation.Portals {
		if p.Customer == in.Installation.Customer {
			return p.Installation
		}
	}
	return ""
}

// chartSemver is the range patched onto the agent-platform OCIRepository: the 4
// line pins itself; the 3 line runs the base's range.
func (in *Input) chartSemver() string {
	if in.Installation.ChartLine == lineFour {
		return ">=4.0.0 <5.0.0"
	}
	return ""
}

// check applies the rules the schema cannot express to the supplied secret
// values: what the policy has the person supply is there and nothing else is.
func (in *Input) check(secrets map[string]string) error {
	needed := in.suppliedSecretFields()
	for _, field := range needed {
		if secrets[field] == "" {
			return fmt.Errorf("%w: %s", ErrEmptySecret, field)
		}
	}
	for field, value := range secrets {
		if !slices.Contains(needed, field) {
			return fmt.Errorf("%w: %s", ErrUnknownSecret, field)
		}
		if value == "" {
			return fmt.Errorf("%w: %s", ErrEmptySecret, field)
		}
	}
	return nil
}

// suppliedSecretFields lists the secret values the person supplies for this
// installation, by field name: the Slack app's credentials where the gateway
// runs, and nothing else — an installation without a Slack app commits with no
// supplied secret. Everything else the platform needs is generated by the
// commit step from the placeholders in the fileset; the model key is never
// supplied, its Secret is the installation's own (CustomerActions).
func (in *Input) suppliedSecretFields() []string {
	fields := in.componentSecretFields()
	sort.Strings(fields)
	return fields
}
