package agentplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	semverlib "github.com/Masterminds/semver/v3"
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

// The meta chart's components the definition names: muster and its Valkey,
// which every installation runs, and the components beside them.
const (
	componentMuster         = "muster"
	componentValkey         = "valkey"
	componentKagent         = "kagent"
	componentAgentManager   = "agent-manager"
	componentKlausGateway   = "klaus-gateway"
	componentClusterManager = "cluster-manager"
	// componentModelManager is no choice of the policy's: the meta chart's 4
	// line runs it by default and the definition leaves it on.
	componentModelManager = "model-manager"
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
	// musterRevisionSecret carries muster's credentials revision in the Flux
	// namespace for its consumers' HelmReleases (valuesFrom); the
	// revision is also a key of the two credentials Secrets, so a rewrite of
	// either draws it anew (see servers.go: the same shape as a server's).
	musterRevisionSecret = "muster-credentials-revision" // #nosec G101 -- a Secret name, not a value
	// kagentOAuth2ProxySecret carries the kagent UI's oauth2-proxy client and
	// cookie secrets, read into its environment at container start.
	kagentOAuth2ProxySecret = "kagent-oauth2-proxy-credentials" // #nosec G101 -- a Secret name, not a value
	// kagentRevisionSecret carries kagent's credentials revision in the Flux
	// namespace for the kagent HelmRelease (valuesFrom), which rolls the
	// oauth2-proxy with it; the revision is also a key of
	// kagentOAuth2ProxySecret, the shape of muster's.
	kagentRevisionSecret = "kagent-credentials-revision" // #nosec G101 -- a Secret name, not a value
)

// Input is the resolved inputs of one installation: the record
// (definitions/agent-platform/schema.json's installation.*, read from the
// registry and the repositories), the fleet policy (policy.yaml) applied to
// it, and the choices a person makes. The renderer reads nothing else.
type Input struct {
	Installation Installation
	// ModelServing is the person's choice: the meta chart's serving slice.
	ModelServing bool
	// SingletonsCapacity is the person's choice: the Karpenter capacity the
	// stateful singletons run on (scheduling.singletons), any or on-demand.
	SingletonsCapacity string
	// AIChat is the person's other choice: the portal's AI chat, in the
	// platform's portal section (portal.go).
	AIChat AIChat
	// SkillRepositories are the repositories the portal's agent creation
	// discovers skills in (skills.repositories), in the platform's portal
	// section: the person's third choice, read back from the portal's files
	// on record.
	SkillRepositories []string
	// Hive is the person's choice of the portal's Hive section (hive.*), in
	// the platform's portal section (hive.go).
	Hive Hive
	// ClusterManagerCommit is the person's choice: the cluster-manager's
	// commit mode through its GitHub App (clusterManager.github.enabled), read
	// back from the configmap patch on record.
	ClusterManagerCommit bool
	// ModelManagerCommit and AgentManagerCommit are the person's choices of
	// the model-manager's and the agent-manager's commit mode through their
	// GitHub Apps (modelManager.github.enabled, agentManager.github.enabled),
	// read back from the configmap patch on record.
	ModelManagerCommit, AgentManagerCommit bool
	// AgentManagerSkills is the person's choice of agent-manager's skill
	// catalog (agentManager.skills): the repositories list_skills discovers
	// and the Secrets private ones are read and booted with, read back from
	// the configmap patch on record.
	AgentManagerSkills AgentManagerSkills
	// Versions are the person's version holds (versions.*), read back from
	// the record so a reconcile keeps them.
	Versions Versions
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
	// HubConnectors are the connectors this installation's Dex registers for
	// the hubs that broker into it (installation.federation.connectors), the
	// policy's names over each hub's record, in the dex-app values' shape
	// (render.ExchangeConnectors); none for a hub whose connector the
	// fleet's Dex base registers.
	HubConnectors []render.Map
	// Teleport is the Teleport cluster a tunnel joins.
	Teleport Teleport
	// SourceInterval is the poll interval of every OCIRepository the
	// definition renders: the policy's flux.sourceInterval.
	SourceInterval string
	// ReleaseCandidates says the installation runs the platform's release
	// candidates: its collection follows the policy's releaseCandidates.stage
	// and its organisation is among releaseCandidates.customers.
	ReleaseCandidates bool
	// selectsLine says the enable selected the 4 chart line over the
	// record's 3: the render writes the selection into the record
	// (recordSelection) and Selected answers it.
	selectsLine bool
}

// AIChat is the portal's AI chat as the person chooses it: whether the
// platform's portal section carries it, the model it answers with, and the
// provider serving Claude — Anthropic's API, whose key is supplied at commit
// (fieldAnthropicKey), or Vertex AI with the Google project and location,
// whose service-account JSON is supplied at commit (fieldGoogleCredentials);
// no credential is part of the document.
type AIChat struct {
	Enabled  bool         `json:"enabled"`
	Model    string       `json:"model"`
	Provider string       `json:"provider"`
	Google   GoogleVertex `json:"google"`
}

// GoogleVertex is where a Vertex chat runs: the GCP project and the Vertex
// region.
type GoogleVertex struct {
	Project  string `json:"project"`
	Location string `json:"location"`
}

// Connectors are the names of the connector a target's Dex registers for
// this hub, policy.yaml's federation.connector templates rendered over the
// hub's record (render.ConnectorNames). A target's Dex registers one
// connector per hub that brokers into it, and only one of an organisation's
// hubs can carry the organisation's plain name.
type Connectors struct {
	// First is the connector of the target's first hub of the organisation
	// — the target's only hub, mostly.
	First string
	// Further is the connector of every further hub of the same
	// organisation, named after the hub.
	Further string
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
	// ClusterIssuer is the cert-manager ClusterIssuer of the installation's
	// Gateway API hosts, where the record says: the serving slice's models
	// Gateway takes its certificate from it.
	ClusterIssuer string `json:"clusterIssuer,omitempty"`
	// Hub says this is the registry's hub: its broker releases the person's
	// GitHub grant to the Dev Portal (hub.go).
	Hub bool `json:"hub"`
	// AgentPlatform says the capability is on record for the installation:
	// its marker file exists, whoever put it there. False is a fresh enable,
	// which selects the chart line the policy needs where the record
	// selects none it can run on (selectLine).
	AgentPlatform bool `json:"agentPlatform"`
	// PodCertificateRequest says the cluster serves certificates.k8s.io/v1beta1
	// PodCertificateRequest, which Agent Substrate needs on the 4 line: the
	// cluster App on record enables the feature gates, or its chart does by
	// default (substrate.go).
	PodCertificateRequest bool `json:"podCertificateRequest"`
	// PortalClientSecret says the portal client's Secret (dex-client-backstage
	// in Dex's namespace) is on record: the customer-portal definition's file
	// in the installation's portal directory, or the portal client already in
	// its Dex patch. Only then does the render add the client, its audience
	// and its trusted peer: this definition renders no file for the Secret.
	PortalClientSecret bool `json:"portalClientSecret"`
	// DexAppVersion is the dex-app the installation runs, where the record
	// says: its own pin or the fleet's base. Renders nothing; the plan holds
	// the commit until it takes the referenced Dex client secrets.
	DexAppVersion string `json:"dexAppVersion,omitempty"`
	// CollectionsStage is the stage the installation's app collection
	// follows, where the record says; with the policy's releaseCandidates it
	// decides whether the installation runs release candidates.
	CollectionsStage string      `json:"collectionsStage,omitempty"`
	Portals          []PortalRef `json:"portals"`
	Federation       Federation  `json:"federation"`
	// MCPServers are the servers registered on the installation beyond the
	// platform's own three: the MCPServer objects under
	// extras/agent-platform/mcpservers/ on record (registered.go).
	MCPServers []RegisteredServer `json:"mcpServers"`
	// MCPClients are the clients registered on the installation with a
	// stable callback: the ConfigMaps under extras/agent-platform/mcpclients/
	// on record (registered.go).
	MCPClients []RegisteredClient `json:"mcpClients"`
	// Workspaces is what the installation's agent-platform values on record
	// say about the workspace-manager: the definition renders none of those
	// keys and registers the manager's sign-in redirect URI from them.
	Workspaces Workspaces `json:"workspaces"`
}

// Workspaces is the workspace-manager as the installation's own
// agent-platform values turn it on (installation.workspaces): whether
// workspaces are on (workspaces.enabled), the manager's public base URL where
// the values set one (workspace-manager.oauth.baseURL) and the Dex client it
// signs the person's browser in with where they name one
// (workspace-manager.oauth.dex.clientID). The keys are the installation's own,
// kept; from them the definition registers <baseURL>/signin as a redirect URI
// of the platform client (workspaceSigninURI).
type Workspaces struct {
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"baseUrl,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// PortalRef is a developer portal that signs people in on the installation:
// its host, the host's organisation, its hostname, where its host's Dex patch
// carries it the id of the Dex client it signs in through — the audience of
// the ID tokens it forwards — whether it is hand-kept: its app-config on
// record carries a literal extension list of its own, so the portal owns its
// lists and the Component sets none (portal.go) — whether its Grafana plugin
// is wired: its app-config carries the plugin's proxy endpoint, so the
// extension list the Component includes carries the dashboards card —
// whether its AI chat is hand-kept: its app-config carries the aiChat block
// by hand, so its environment supplies the chat's key and the Component
// renders the chat's blocks without a credentials Secret — and the chart
// line it follows: the ref its directory kustomization patches onto the
// fleet base's backstage OCIRepository, empty when it patches none — and,
// on a portal the customer-portal definition renders, the installations it
// shows that run the platform on record, which the platform's portal
// section lists with this installation (portalInstallations).
type PortalRef struct {
	Installation  string               `json:"installation"`
	Customer      string               `json:"customer"`
	Domain        string               `json:"domain"`
	ClientID      string               `json:"clientId,omitempty"`
	ChartLine     string               `json:"chartLine,omitempty"`
	HandKept      bool                 `json:"handKept,omitempty"`
	GrafanaWired  bool                 `json:"grafanaWired,omitempty"`
	HandKeptChat  bool                 `json:"handKeptChat,omitempty"`
	Installations []PortalInstallation `json:"installations,omitempty"`
}

// PortalInstallation is an installation a portal shows that runs the agent
// platform on record: its name and its base domain, which its muster's URL
// derives from.
type PortalInstallation struct {
	Name       string `json:"name"`
	BaseDomain string `json:"baseDomain"`
}

// Federation is the installation's place in the fleet's token exchange.
type Federation struct {
	Hubs    []string `json:"hubs"`
	Targets []Target `json:"targets"`
	// Connectors are the hubs among Hubs with the facts the connector this
	// installation's Dex registers for each is rendered from: the hub's
	// organisation and base domain, and whether it is the first of its
	// organisation's hubs into this installation (render.HubConnector).
	Connectors []render.HubConnector `json:"connectors"`
	// RegistryHub is the registry's hub by name (the installation with
	// installation.hub), among Hubs or not: its token-exchange client in this
	// installation's Dex carries the fleet's plain id, every other hub's the
	// hub's name too (tokenExchangeClient).
	RegistryHub    string `json:"registryHub"`
	BrokerClientID string `json:"brokerClientId"`
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
	// Servers are the groups of the MCP servers the target runs: the servers
	// whose extras directory its management-clusters repository carries.
	Servers []string `json:"servers"`
	// AgentPlatform says the target runs the agent platform, which renders
	// the Dex side of this hub's client there: the pair's two files name each
	// other (exchangeSecretName). ClusterMCPServers says the target runs its
	// MCP servers under cluster-mcp-servers without the platform, whose
	// render holds the Dex side in the same file (Target.rendersDexSide).
	// With neither the target keeps the client by hand.
	AgentPlatform     bool `json:"agentPlatform"`
	ClusterMCPServers bool `json:"clusterMcpServers"`
	// BrowseOnly says the policy keeps the target to browsing
	// (federation.browseOnly): the hub's portal still lists its clusters
	// through the broker, and the hub federates none of its MCP servers,
	// registers no identity provider at its Dex and tunnels nothing beyond
	// its Dex and its API server. The policy's, never the record's.
	BrowseOnly bool `json:"-"`
}

// groups are the target's federated MCP server groups: the servers it runs,
// in the order the shared template registers them.
func (t Target) groups() []string {
	groups := make([]string, 0, len(servers))
	for _, s := range servers {
		if slices.Contains(t.Servers, s.Group) {
			groups = append(groups, s.Group)
		}
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
	KlausGateway      GatewayPolicy `yaml:"klausGateway"`
	ReleaseCandidates struct {
		Stage     string   `yaml:"stage"`
		Customers []string `yaml:"customers"`
	} `yaml:"releaseCandidates"`
	Federation struct {
		Connector  render.ConnectorNames `yaml:"connector"`
		Teleport   Teleport              `yaml:"teleport"`
		BrowseOnly []string              `yaml:"browseOnly"`
	} `yaml:"federation"`
	Flux struct {
		SourceInterval string `yaml:"sourceInterval"`
	} `yaml:"flux"`
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
	if _, err := time.ParseDuration(pol.Flux.SourceInterval); err != nil {
		return nil, fmt.Errorf("%w: flux.sourceInterval: %w", ErrPolicy, err)
	}
	return &pol, nil
}

// releaseCandidates says the policy runs the platform's release candidates on
// an installation: its collection follows the policy's releaseCandidates.stage
// and its organisation is among releaseCandidates.customers.
func (p *policy) releaseCandidates(inst Installation) bool {
	rc := p.ReleaseCandidates
	return rc.Stage != "" && inst.CollectionsStage == rc.Stage && slices.Contains(rc.Customers, inst.Customer)
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

// browseOnly keeps the targets the policy names under federation.browseOnly
// to browsing: no MCP server group and no proxied agent platform, so the hub
// renders for them only what the portal's browsing needs (Target.BrowseOnly).
func (p *policy) browseOnly(targets []Target) {
	for i := range targets {
		if slices.Contains(p.Federation.BrowseOnly, targets[i].Installation) {
			targets[i].BrowseOnly, targets[i].Servers, targets[i].PlatformProxied = true, nil, false
		}
	}
}

// connectors renders the hub's connector names from the record: what a
// target's Dex registers for this hub as its first, and as a further, hub of
// the organisation. A template that cannot name one is ErrPolicy.
func (p *policy) connectors(inst Installation) (Connectors, error) {
	first, err := p.Federation.Connector.Name(true, inst.Customer, inst.Name)
	if err != nil {
		return Connectors{}, fmt.Errorf("%w: %w", ErrPolicy, err)
	}
	further, err := p.Federation.Connector.Name(false, inst.Customer, inst.Name)
	if err != nil {
		return Connectors{}, fmt.Errorf("%w: %w", ErrPolicy, err)
	}
	return Connectors{First: first, Further: further}, nil
}

// hubConnectors renders the connectors this installation's Dex registers
// for the hubs that broker into it, the policy's names over each hub's
// record. A template that cannot name one is ErrPolicy.
func (p *policy) hubConnectors(inst Installation) ([]render.Map, error) {
	list, err := render.ExchangeConnectors(p.Federation.Connector, inst.Federation.Connectors)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPolicy, err)
	}
	return list, nil
}

// document is the input document as the schema shapes it.
type document struct {
	Installation Installation `json:"installation"`
	ModelServing struct {
		Enabled bool `json:"enabled"`
	} `json:"modelServing"`
	Scheduling struct {
		SingletonsCapacity string `json:"singletonsCapacity"`
	} `json:"scheduling"`
	AIChat AIChat `json:"aiChat"`
	Skills struct {
		Repositories []string `json:"repositories"`
	} `json:"skills"`
	Hive           Hive         `json:"hive"`
	ClusterManager commitChoice `json:"clusterManager"`
	ModelManager   commitChoice `json:"modelManager"`
	AgentManager   struct {
		commitChoice
		Skills AgentManagerSkills `json:"skills"`
	} `json:"agentManager"`
	Versions Versions `json:"versions"`
}

// Versions are the version holds on record — the meta chart's version or
// range in place of the chart line's, and a component's in place of the meta
// chart's, by component; empty is no hold — and each hold's reason, the
// comment a person wrote above it, by the same keys; empty, the hold
// carries the generic comment naming its input.
type Versions struct {
	Holds
	Reasons Holds `json:"reasons"`
}

// Holds is one value per hold: the meta chart's and the components', by
// component.
type Holds struct {
	Chart      string            `json:"chart"`
	Components map[string]string `json:"components"`
}

// AgentManagerSkills is agent-manager's skill catalog: the repositories
// list_skills discovers skills in, the Secret of the skills GitHub App they
// are read with, and the Secret agents with a git skill boot with — provisioned
// and named, or minted from the App by agent-manager.
type AgentManagerSkills struct {
	Repositories      []string `json:"repositories"`
	AppSecretName     string   `json:"appSecretName"`
	GitAuthSecretName string   `json:"gitAuthSecretName"`
	MintGitAuthSecret bool     `json:"mintGitAuthSecret"`
}

// set is whether the person chose anything for the catalog.
func (s AgentManagerSkills) set() bool {
	return len(s.Repositories) > 0 || s.AppSecretName != "" || s.GitAuthSecretName != "" || s.MintGitAuthSecret
}

// commitChoice is a manager's commit mode through its GitHub App: the one
// choice clusterManager, modelManager and agentManager each carry.
type commitChoice struct {
	GitHub struct {
		Enabled bool `json:"enabled"`
	} `json:"github"`
}

// Parse validates raw against the schema and resolves the inputs: the record
// as given, the policy applied to its organisation, the choices as made. raw
// is the decoded document (from YAML or JSON): map[string]any at the top. A
// key the schema does not know, a missing required key or a wrong shape is
// ErrInput naming the location; a record the definition cannot render as it
// stands (a hub without its broker client, a private target on a hub without a
// published service-account issuer, the serving slice or a component of the 4
// chart line on a record that selects the 3 line, kagent on the 4 line where
// the cluster does not serve PodCertificateRequest, the chat or skill
// repositories on an installation whose organisation hosts no portal for
// them, the chat without a model, or on Vertex without its Google project or
// location, the Hive where the installation cannot serve it, the cluster-manager's commit mode where no cluster-manager runs)
// is ErrInput too.
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
	in := &Input{Installation: d.Installation, ModelServing: d.ModelServing.Enabled, SingletonsCapacity: d.Scheduling.SingletonsCapacity, AIChat: d.AIChat, SkillRepositories: d.Skills.Repositories, Hive: d.Hive,
		ClusterManagerCommit: d.ClusterManager.GitHub.Enabled, ModelManagerCommit: d.ModelManager.GitHub.Enabled, AgentManagerCommit: d.AgentManager.GitHub.Enabled, AgentManagerSkills: d.AgentManager.Skills, Versions: d.Versions, Gateway: pol.gateway(d.Installation), Teleport: pol.Federation.Teleport, SourceInterval: pol.Flux.SourceInterval,
		ReleaseCandidates: pol.releaseCandidates(d.Installation)}
	if in.Components, err = pol.components(in.Installation); err != nil {
		return nil, err
	}
	if in.Connectors, err = pol.connectors(in.Installation); err != nil {
		return nil, err
	}
	if in.HubConnectors, err = pol.hubConnectors(in.Installation); err != nil {
		return nil, err
	}
	pol.browseOnly(in.Installation.Federation.Targets)
	in.selectLine()
	if err := in.checkRecord(); err != nil {
		return nil, err
	}
	return in, nil
}

// selectLine picks the meta chart line of a fresh enable. The record selects
// the line (installation.chartLine: agentPlatform.kagentApiV2 in
// config.yaml.patch selects 4, its absence 3), and a fresh record never
// carries the key; an organisation the policy grants a component the 4 line
// alone carries (lineFourComponents) has no line to run on but 4. So where
// the capability is not on record (installation.agentPlatform false) and the
// record selects 3, the enable selects 4 and writes the selection into the
// record as one more file of the configs pull request (recordSelection). An
// installation with the capability on record keeps the line its record
// selects, and checkRecord refuses as before: adoption is an Apply, never a
// line change; an organisation without a line-4 component stays on the line
// its record selects.
func (in *Input) selectLine() {
	if in.Installation.AgentPlatform || in.Installation.ChartLine == lineFour {
		return
	}
	for _, c := range lineFourComponents {
		if in.Components[c] {
			in.Installation.ChartLine = lineFour
			in.selectsLine = true
			return
		}
	}
}

// Selected is the chart line a fresh enable selected over the record's, in
// the document's shape; nil where the record's line stands.
func (in *Input) Selected() map[string]any {
	if !in.selectsLine {
		return nil
	}
	return map[string]any{"installation": map[string]any{"chartLine": lineFour}}
}

// recordSelection is the fragment of the record a fresh enable writes: the
// key that selects the 4 line, with why. The plan edits it into
// installations/<name>/config.yaml.patch and keeps every other key.
func (in *Input) recordSelection() string {
	granted := make([]string, 0, len(lineFourComponents))
	for _, c := range lineFourComponents {
		if in.Components[c] {
			granted = append(granted, c)
		}
	}
	return "# The agent-platform meta chart line, selected by the enable through giantswarm-platform-manager:\n" +
		"# the fleet policy grants " + in.Installation.Customer + "'s installations the " + strings.Join(granted, " and the ") +
		", which the 4 line alone carries.\n" +
		"agentPlatform:\n  kagentApiV2: true\n"
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
	for i, c := range fed.Connectors {
		if !slices.Contains(fed.Hubs, c.Hub) {
			return refuse(fmt.Sprintf("installation.federation.connectors[%d] names %s, which is not among the hubs that broker into %s (installation.federation.hubs: %s); the record supplies both", i, c.Hub, in.Installation.Name, strings.Join(fed.Hubs, ", ")))
		}
	}
	if in.ModelServing && in.Installation.ChartLine != lineFour {
		return refuse(fmt.Sprintf("%s is the 4 chart line's serving slice, and this installation runs the %s line; the record's chart line decides", describe("modelServing.enabled"), in.Installation.ChartLine))
	}
	if in.ModelServing && in.Installation.ClusterIssuer == "" {
		return refuse(fmt.Sprintf("%s serves the models Gateway with a certificate from %s, and the record names none; gatewayApi.clusterIssuer in installations/%s/config.yaml.patch or the shared default supplies it", describe("modelServing.enabled"), describe("installation.clusterIssuer"), in.Installation.Name))
	}
	if in.singletonsOnDemand() && in.Installation.Provider != providerCAPA {
		return refuse(fmt.Sprintf("%s is on-demand, Karpenter's capacity type, and a %s installation runs no Karpenter the definition knows; the pods would stay Pending", describe("scheduling.singletonsCapacity"), in.Installation.Provider))
	}
	if in.ReleaseCandidates && in.Installation.ChartLine != lineFour {
		return refuse(fmt.Sprintf("%s is %s, whose installations of %s run release candidates (the fleet policy's releaseCandidates), and release candidates are the 4 chart line's (gitops.prereleases); %s selects the %s line, and agentPlatform.kagentApiV2: true in installations/%s/config.yaml.patch selects 4", describe("installation.collectionsStage"), in.Installation.CollectionsStage, in.Installation.Customer, describe("installation.chartLine"), in.Installation.ChartLine, in.Installation.Name))
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
	if in.AIChat.Enabled && in.hostedPortal() == nil {
		return refuse(describe("aiChat.enabled") + " asks for the chat in the developer portal's agent-platform section, and no portal carries one for this installation: the record lists no portal hosted on it, nor its organisation's on a sibling that is not hand-kept")
	}
	if len(in.SkillRepositories) > 0 && in.hostedPortal() == nil {
		return refuse(describe("skills.repositories") + " lists the skill repositories of the developer portal's agent-platform section, and no portal carries one for this installation: the record lists no portal hosted on it, nor its organisation's on a sibling that is not hand-kept")
	}
	if err := in.checkHive(); err != nil {
		return err
	}
	if in.ClusterManagerCommit && !in.clusterManager() {
		return refuse(fmt.Sprintf("%s asks for the cluster-manager's commit mode, and the fleet policy runs no cluster-manager on %s's installations", describe("clusterManager.github.enabled"), in.Installation.Customer))
	}
	if in.AgentManagerCommit && !in.agentManager() {
		return refuse(fmt.Sprintf("%s asks for the agent-manager's commit mode, and the fleet policy runs no agent-manager on %s's installations", describe("agentManager.github.enabled"), in.Installation.Customer))
	}
	if in.AgentManagerSkills.set() && !in.agentManager() {
		return refuse(fmt.Sprintf("%s asks for the agent-manager's skill catalog, and the fleet policy runs no agent-manager on %s's installations", describe("agentManager.skills"), in.Installation.Customer))
	}
	if skills := in.AgentManagerSkills; skills.MintGitAuthSecret {
		if skills.GitAuthSecretName != "" {
			return refuse(describe("agentManager.skills.mintGitAuthSecret") + " and " + describe("agentManager.skills.gitAuthSecretName") + " both name the agents' boot Secret; the minted one replaces the provisioned one")
		}
		if skills.AppSecretName == "" {
			return refuse(describe("agentManager.skills.mintGitAuthSecret") + " mints the skills GitHub App's installation token, and " + describe("agentManager.skills.appSecretName") + " names no App")
		}
	}
	if in.ModelManagerCommit && !in.modelManager() {
		return refuse(describe("modelManager.github.enabled") + " asks for the model-manager's commit mode, and the model-manager runs on the platform's 4 chart line only; agentPlatform.kagentApiV2: true in the installation's config.yaml.patch selects 4")
	}
	if in.AIChat.Enabled && in.AIChat.Model == "" {
		return refuse(describe("aiChat.model") + " is empty; the chat answers with one model, and the schema's default stands where none is typed")
	}
	if in.aiChatVertex() {
		for field, value := range map[string]string{"aiChat.google.project": in.AIChat.Google.Project, "aiChat.google.location": in.AIChat.Google.Location} {
			if value == "" {
				return refuse(describe(field) + " is empty, and a chat on Vertex AI (aiChat.provider: vertex) runs in one Google project and region; the person names them")
			}
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
// same organisation carries its own name. The tunnel tokens follow the same
// rule (Target.tokenName).
func (in *Input) connector(t Target) string {
	if t.first(in.Installation.Name) {
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
func (in *Input) modelManager() bool   { return in.Installation.ChartLine == lineFour }

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

// releaseTagFilter is the tag filter patched onto the agent-platform
// OCIRepository beside a range that admits release candidates: stable releases
// and release candidates only. The chart's branch builds are pushed to the same
// repository with a pre-release of their own (X.Y.Z-r<hash>t<time>h<sha>),
// which the range alone would select.
const releaseTagFilter = `^v?[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`

// chartSemver is the range patched onto the agent-platform OCIRepository: the 4
// line pins itself, its release candidates included where the installation
// runs them; the 3 line runs the base's range.
func (in *Input) chartSemver() string {
	if in.Installation.ChartLine != lineFour {
		return ""
	}
	if in.ReleaseCandidates {
		return ">=4.0.0-0 <5.0.0-0"
	}
	return ">=4.0.0 <5.0.0"
}

// releaseTag is releaseTagFilter compiled: the tags the filter admits.
var releaseTag = regexp.MustCompile(releaseTagFilter)

// releaseFilterAdmits says whether the release tag filter has anything to
// select beside the OCIRepository's semver: a range, or an exact version the
// filter admits (a stable release or a candidate). An exact hold on a
// development build (X.Y.Z-r<hash>t<time>h<sha>) is one the filter would
// exclude, so Flux would resolve no tag at all: the pin is written alone.
func releaseFilterAdmits(semver string) bool {
	if _, err := semverlib.StrictNewVersion(strings.TrimPrefix(semver, "v")); err != nil {
		return true
	}
	return releaseTag.MatchString(semver)
}

// holdComment is the comment a held version carries in the file: the reason
// on record or typed for it (versions.reasons.*), else the generic one naming
// the input that keeps it and how it moves.
func holdComment(input, reason string) string {
	if reason != "" {
		return reason
	}
	return "Held by the input versions." + input + ": a reconcile keeps it; --input versions." + input + "=<version> moves it, an empty value lifts it."
}

// commentLines is text as YAML comment lines, one per line of text.
func commentLines(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
	}
	return b.String()
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
// runs, the chat's Anthropic API key where the portal runs the chat, and
// nothing else — an installation without a Slack app or a chat commits with
// no supplied secret. Everything else the platform needs is generated by the
// commit step from the placeholders in the fileset; the model key is never
// supplied, its Secret is the installation's own (CustomerActions).
func (in *Input) suppliedSecretFields() []string {
	fields := append(in.componentSecretFields(), in.chatSecretFields()...)
	sort.Strings(fields)
	return fields
}
