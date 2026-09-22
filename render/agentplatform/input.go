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
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Errors the render refuses with. Every refusal is a render.Refusal: one
// sentence naming the input (what the schema says it is, its key), what is
// wrong with it and what supplies it, unwrapping to one of these.
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
	// Gateway is the chat gateway's shape for this installation, where the
	// policy runs it: the fleet's, with the installation's own default agent.
	Gateway GatewayPolicy
	// Connectors are the names of the connector a target's Dex registers
	// for this hub, rendered from the policy's templates over the record;
	// which one a target carries is connector's.
	Connectors Connectors
	// Teleport is the Teleport cluster a tunnel joins.
	Teleport Teleport
}

// Connectors are policy.yaml's federation.connector: the names of the
// connector a target's Dex registers for a hub, as Go templates over the
// hub's record. A target's Dex registers one connector per hub that brokers
// into it, and only one of an organisation's hubs can carry the
// organisation's plain name.
type Connectors struct {
	// First is the connector of the target's first hub of the organisation
	// — the target's only hub, mostly.
	First string `yaml:"first"`
	// Further is the connector of every further hub of the same
	// organisation, named after the hub.
	Further string `yaml:"further"`
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
	// Hub says this is the registry's hub: its broker releases the person's
	// GitHub grant to the Dev Portal (hub.go).
	Hub bool `json:"hub"`
	// PodCertificateRequest says the cluster serves certificates.k8s.io/v1beta1
	// PodCertificateRequest, which Agent Substrate needs on the 4 line: the
	// cluster App on record enables the feature gates, or its chart does by
	// default (substrate.go).
	PodCertificateRequest bool `json:"podCertificateRequest"`
	// DexAppVersion is the dex-app the installation runs, where the record
	// says: its own pin or the fleet's base. Renders nothing; the plan holds
	// the commit until it takes the referenced Dex client secrets.
	DexAppVersion string      `json:"dexAppVersion,omitempty"`
	Portals       []PortalRef `json:"portals"`
	Federation    Federation  `json:"federation"`
}

// PortalRef is a developer portal that signs people in on the installation:
// its host, the host's organisation, its hostname, where its host's Dex patch
// carries it the id of the Dex client it signs in through — the audience of
// the ID tokens it forwards — whether it is hand-kept: its app-config on
// record carries a literal extension list of its own, so the portal owns its
// lists and the Component sets none (portal.go) — and the chart line it
// follows: the ref its directory kustomization patches onto the fleet base's
// backstage OCIRepository, empty when it patches none.
type PortalRef struct {
	Installation string `json:"installation"`
	Customer     string `json:"customer"`
	Domain       string `json:"domain"`
	ClientID     string `json:"clientId,omitempty"`
	ChartLine    string `json:"chartLine,omitempty"`
	HandKept     bool   `json:"handKept,omitempty"`
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
	// PlatformProxied says the hub's portal proxies the target's agent
	// platform: a private one is then also tunnelled to its kagent and its
	// agentgateway.
	PlatformProxied bool `json:"platformProxied"`
	// Hubs are the hubs of this hub's organisation that broker into the
	// target, this hub among them, in the registry's order: the first
	// carries the organisation's plain connector on the target's Dex, every
	// further hub its own (connector). Empty: this hub alone brokers into it.
	Hubs []string `json:"hubs"`
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
// Slack app, where the gateway runs, each with what is its own, and the shape
// one for all of them. Slack's mode is no policy: it follows the record
// (slackMode).
type GatewayPolicy struct {
	// Installations are the installations a Slack app exists for, by name —
	// the one entry of the policy keyed by installation, a fact of each rather
	// than a tuning — with the installation's own values. The gateway renders
	// there and nowhere else.
	Installations map[string]InstallationGateway `yaml:"installations"`
	Slack         struct {
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

// InstallationGateway is what the gateway's shape carries per installation:
// the agent agent-to-agent calls by default, one of the installation's own
// agents; empty, the policy's a2a.defaultAgent.
type InstallationGateway struct {
	DefaultAgent string `yaml:"defaultAgent"`
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
		Connector Connectors `yaml:"connector"`
		Teleport  Teleport   `yaml:"teleport"`
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
	if _, slackApp := p.KlausGateway.Installations[inst.Name]; slackApp {
		out[componentKlausGateway] = true
	}
	return out, nil
}

// gateway is the chat gateway's shape for an installation: the policy's, with
// the installation's own default agent where its entry names one.
func (p *policy) gateway(inst Installation) GatewayPolicy {
	g := p.KlausGateway
	if own := g.Installations[inst.Name]; own.DefaultAgent != "" {
		g.A2A.DefaultAgent = own.DefaultAgent
	}
	return g
}

// connectors renders the hub's connector names from the record.
func (p *policy) connectors(inst Installation) (Connectors, error) {
	first, err := renderPolicyTemplate("federation.connector.first", p.Federation.Connector.First, inst)
	if err != nil {
		return Connectors{}, err
	}
	further, err := renderPolicyTemplate("federation.connector.further", p.Federation.Connector.Further, inst)
	if err != nil {
		return Connectors{}, err
	}
	return Connectors{First: first, Further: further}, nil
}

// renderPolicyTemplate renders one of the policy's Go templates over the
// record; a template that does not parse, names a field the record lacks or
// renders empty is ErrPolicy naming the key.
func renderPolicyTemplate(key, text string, inst Installation) (string, error) {
	tpl, err := template.New(key).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrPolicy, key, err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, inst); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrPolicy, key, err)
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("%w: %s: renders empty", ErrPolicy, key)
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
	in := &Input{Installation: d.Installation, ModelServing: d.ModelServing.Enabled, Gateway: pol.gateway(d.Installation), Teleport: pol.Federation.Teleport}
	if in.Components, err = pol.components(in.Installation); err != nil {
		return nil, err
	}
	if in.Connectors, err = pol.connectors(in.Installation); err != nil {
		return nil, err
	}
	if err := in.checkRecord(); err != nil {
		return nil, err
	}
	return in, nil
}

// refuse is the definition's refusal of an input, as a person reads it.
func refuse(sentence string) error { return &render.Refusal{Kind: ErrInput, Sentence: sentence} }

// describe names an input for a refusal: what the schema says it is and its
// key.
func describe(field string) string { return render.Describe("agent-platform", field) }

// checkRecord refuses a record the definition cannot render as it stands:
// one sentence naming the fact, what is wrong with it and what supplies it.
func (in *Input) checkRecord() error {
	fed := in.Installation.Federation
	if len(fed.Targets) > 0 && fed.BrokerClientID == "" {
		return refuse(describe("installation.federation.brokerClientId") + " is not on record; a hub's broker client is registered once and read back from its patch")
	}
	if in.hasPrivateTarget() && in.serviceAccountIssuer() == "" {
		return refuse(fmt.Sprintf("%s include a private one, whose tunnel joins Teleport by this hub's published service-account issuer, and a %s installation publishes none the definition knows", describe("installation.federation.targets"), in.Installation.Provider))
	}
	for i, t := range fed.Targets {
		if len(t.Hubs) > 0 && !slices.Contains(t.Hubs, in.Installation.Name) {
			return refuse(fmt.Sprintf("installation.federation.targets[%d].hubs names the hubs of this organisation that broker into %s as %s, and %s is not among them; the record supplies them", i, t.Installation, strings.Join(t.Hubs, ", "), in.Installation.Name))
		}
	}
	if in.ModelServing && in.Installation.ChartLine != lineFour {
		return refuse(fmt.Sprintf("%s is the 4 chart line's serving slice, and this installation runs the %s line; the record's chart line decides", describe("modelServing.enabled"), in.Installation.ChartLine))
	}
	for _, c := range lineFourComponents {
		if in.Components[c] && in.Installation.ChartLine != lineFour {
			return refuse(fmt.Sprintf("%s selects the %s line, and %s needs the platform's 4 chart line; agentPlatform.kagentApiV2: true in installations/%s/config.yaml.patch selects 4", describe("installation.chartLine"), in.Installation.ChartLine, c, in.Installation.Name))
		}
	}
	if p := in.hostedPortal(); p != nil && in.kagent() {
		if p.ChartLine == "" {
			return refuse(fmt.Sprintf("the hosted portal's chart line (installation.portals[%s].chartLine) is not on record: management-clusters/%s/extras/backstage/backstage/kustomization.yaml patches no ref onto the fleet base's backstage OCIRepository, and the portal section names the agents' Flux identity for a portal before backstage %s only", p.Installation, p.Installation, portalPluginRemoval))
		}
		if _, err := portalChartFloor(p.ChartLine); err != nil {
			return refuse(fmt.Sprintf("the hosted portal's chart line (installation.portals[%s].chartLine) is not one the definition reads: %v", p.Installation, err))
		}
	}
	if in.kagent() && in.Installation.ChartLine == lineFour && !in.Installation.PodCertificateRequest {
		return refuse(fmt.Sprintf("%s does not say this cluster serves %s/%s %s, which kagent's Agent Substrate on the 4 chart line needs; enable the feature gates %s under cluster.internal.advancedConfiguration.{%s}.featureGates in the cluster App's values (management-clusters/%s/cluster-app-manifests.yaml), or run a cluster App chart that enables them by default (%s and later)",
			describe("installation.podCertificateRequest"), apiGroup(PodCertificateRequestResource), PodCertificateRequestVersion, apiResource(PodCertificateRequestResource), strings.Join(PodCertificateRequestGates, ", "), strings.Join(PodCertificateRequestComponents, ","), in.Installation.Name, podCertificateRequestDefaults()))
	}
	return nil
}

// apiResource and apiGroup split a probe's resource.group.
func apiResource(resource string) string { r, _, _ := strings.Cut(resource, "."); return r }
func apiGroup(resource string) string    { _, g, _ := strings.Cut(resource, "."); return g }

// connector is the connector t's Dex registers for this hub. A target's Dex
// registers one connector per hub that brokers into it, and only one of an
// organisation's hubs carries the organisation's plain name: the target's
// first hub of the organisation (t.Hubs, in the registry's order), which a
// target this hub alone brokers into names none of. Every further hub of the
// same organisation carries its own name.
func (in *Input) connector(t Target) string {
	if len(t.Hubs) == 0 || t.Hubs[0] == in.Installation.Name {
		return in.Connectors.First
	}
	return in.Connectors.Further
}

// hasPrivateTarget says whether any federated target is reached through the tunnel.
func (in *Input) hasPrivateTarget() bool {
	return slices.ContainsFunc(in.Installation.Federation.Targets, func(t Target) bool { return t.Private })
}

func (in *Input) kagent() bool         { return in.Components[componentKagent] }
func (in *Input) agentManager() bool   { return in.Components[componentAgentManager] }
func (in *Input) klausGateway() bool   { return in.Components[componentKlausGateway] }
func (in *Input) clusterManager() bool { return in.Components[componentClusterManager] }

// portalHost is the installation whose management-clusters tree hosts the
// portal the platform's portal section is written into (hostedPortal); empty
// when there is none.
func (in *Input) portalHost() string {
	if p := in.hostedPortal(); p != nil {
		return p.Installation
	}
	return ""
}

// hostedPortal is the portal the platform's portal section is written into:
// the portal hosted on this installation itself; else the organisation's
// portal on a sibling that is not hand-kept (a customer aggregator's); else
// none. A hand-kept sibling portal — the hub's Dev Portal — carries its own
// section for the installations it proxies and takes no other installation's
// Component; nor does another organisation's portal.
func (in *Input) hostedPortal() *PortalRef {
	for i := range in.Installation.Portals {
		if p := &in.Installation.Portals[i]; p.Installation == in.Installation.Name {
			return p
		}
	}
	for i := range in.Installation.Portals {
		if p := &in.Installation.Portals[i]; p.Customer == in.Installation.Customer && !p.HandKept {
			return p
		}
	}
	return nil
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
