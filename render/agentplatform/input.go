package agentplatform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// Errors the render refuses with. Every message names the key, field or
// component concerned.
var (
	// ErrInput is an input the schema rejects: an unknown key, a missing
	// required one, a value of the wrong shape.
	ErrInput = errors.New("agent-platform: input")
	// ErrEmptySecret is a supplied secret value the render needs and did not
	// get, or got empty.
	ErrEmptySecret = errors.New("agent-platform: empty secret value")
	// ErrUnknownSecret is a supplied secret value no input asks for.
	ErrUnknownSecret = errors.New("agent-platform: unknown secret value")
	// ErrPolicy is a component or pin the fleet policy does not offer the
	// installation's customer.
	ErrPolicy = errors.New("agent-platform: fleet policy")
	// ErrNotRendered is an input this version of the definition accepts by
	// schema but has no rendering for; it refuses rather than emit a fileset
	// that is missing the input's files.
	ErrNotRendered = errors.New("agent-platform: not rendered by this definition")
)

// fieldModelKey is the supplied secret field carrying the model provider key
// when kagent.modelKeySecret is managed.
const fieldModelKey = "kagent.modelKey"

// modelKeyManaged is the kagent.modelKeySecret value under which the platform
// team supplies the model key; any other value leaves it to the customer.
const modelKeyManaged = "managed"

// Input is the typed form of definitions/agent-platform/schema.json. The
// schema is the contract; this struct is how the renderer reads it.
type Input struct {
	Installation   Installation    `json:"installation"`
	Secrets        SecretNames     `json:"secrets"`
	Kagent         Kagent          `json:"kagent"`
	Identity       Identity        `json:"identity"`
	Portal         Portal          `json:"portal"`
	ToolAccess     ToolAccess      `json:"toolAccess"`
	Federation     Federation      `json:"federation"`
	Chart          Chart           `json:"chart"`
	Muster         MusterKnobs     `json:"muster"`
	AgentSandbox   *Toggle         `json:"agentSandbox"`
	KlausGateway   *KlausGateway   `json:"klausGateway"`
	ClusterManager *ClusterManager `json:"clusterManager"`
}

// Installation is the facts on record; see the schema for each field.
type Installation struct {
	Name           string `json:"name"`
	BaseDomain     string `json:"baseDomain"`
	Customer       string `json:"customer"`
	Provider       string `json:"provider"`
	Private        bool   `json:"private"`
	ChartLine      string `json:"chartLine"`
	MusterClientID string `json:"musterClientId"`
}

// SecretNames names the referenced Secrets; empty fields take the schema's defaults.
type SecretNames struct {
	MusterOAuth  string `json:"musterOAuth"`
	MusterValkey string `json:"musterValkey"`
}

// Kagent is the agent runtime section.
type Kagent struct {
	Enabled                           bool          `json:"enabled"`
	ModelKeySecret                    string        `json:"modelKeySecret"`
	DefaultModel                      string        `json:"defaultModel"`
	AdditionalModelConfigs            []ModelConfig `json:"additionalModelConfigs"`
	StorageClass                      string        `json:"storageClass"`
	PostgresBackupAzureSubscriptionID string        `json:"postgresBackupAzureSubscriptionId"`
	ControllerResources               *Resources    `json:"controllerResources"`
	BundledAgents                     []string      `json:"bundledAgents"`
	UIIngressPeers                    []Peer        `json:"uiIngressPeers"`
}

// ModelConfig is one additional kagent ModelConfig.
type ModelConfig struct {
	Name            string `json:"name" yaml:"name"`
	DisplayName     string `json:"displayName" yaml:"displayName"`
	Provider        string `json:"provider" yaml:"provider"`
	Model           string `json:"model" yaml:"model"`
	APIKeySecret    string `json:"apiKeySecret" yaml:"apiKeySecret"`
	APIKeySecretKey string `json:"apiKeySecretKey" yaml:"apiKeySecretKey"`
	BaseURL         string `json:"baseUrl" yaml:"baseUrl"`
}

// Resources is a requests/limits pair.
type Resources struct {
	Requests *ResourceList `json:"requests" yaml:"requests,omitempty"`
	Limits   *ResourceList `json:"limits" yaml:"limits,omitempty"`
}

// ResourceList is cpu and memory.
type ResourceList struct {
	CPU    string `json:"cpu" yaml:"cpu,omitempty"`
	Memory string `json:"memory" yaml:"memory,omitempty"`
}

// Peer is a workload by pod label and namespace.
type Peer struct {
	App       string `json:"app" yaml:"app"`
	Namespace string `json:"namespace" yaml:"namespace"`
}

// Identity is the login and token trust section.
type Identity struct {
	LoginConnectorID               string          `json:"loginConnectorId"`
	ExtraTrustedAudiences          []string        `json:"extraTrustedAudiences"`
	TrustedIssuers                 []TrustedIssuer `json:"trustedIssuers"`
	PostLoginRedirectAllowlist     []string        `json:"postLoginRedirectAllowlist"`
	PublicRegistrationRedirectURIs []string        `json:"publicRegistrationRedirectURIs"`
	AdditionalDexClients           []DexClient     `json:"additionalDexClients"`
}

// TrustedIssuer is an issuer muster accepts besides the installation's Dex.
type TrustedIssuer struct {
	Issuer                  string            `json:"issuer" yaml:"issuer"`
	JWKSURL                 string            `json:"jwksUrl" yaml:"jwksUrl,omitempty"`
	AllowedAudiences        []string          `json:"allowedAudiences" yaml:"allowedAudiences"`
	AcceptedTypHeaders      []string          `json:"acceptedTypHeaders" yaml:"acceptedTypHeaders,omitempty"`
	AllowedClaims           map[string]string `json:"allowedClaims" yaml:"allowedClaims,omitempty"`
	SubjectClaim            string            `json:"subjectClaim" yaml:"subjectClaim,omitempty"`
	AllowPrivateIPJWKS      bool              `json:"allowPrivateIPJWKS" yaml:"allowPrivateIPJWKS,omitempty"`
	AllowPrivateIPJWKSHosts []string          `json:"allowPrivateIPJWKSHosts" yaml:"allowPrivateIPJWKSHosts,omitempty"`
}

// DexClient is a Dex client of the installation beyond the platform's own.
type DexClient struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Public       bool     `json:"public"`
	RedirectURIs []string `json:"redirectURIs"`
	TrustedPeers []string `json:"trustedPeers"`
}

// Portal is the developer portal's agent-platform section.
type Portal struct {
	Enabled            bool     `json:"enabled"`
	Installation       string   `json:"installation"`
	ClientIDs          []string `json:"clientIds"`
	SkillsRepositories []string `json:"skillsRepositories"`
	AIChat             *AIChat  `json:"aiChat"`
}

// AIChat is the portal's AI chat.
type AIChat struct {
	Enabled  bool    `json:"enabled"`
	Model    string  `json:"model"`
	Provider string  `json:"provider"`
	Google   *Google `json:"google"`
}

// Google is a Vertex project and location.
type Google struct {
	Project  string `json:"project"`
	Location string `json:"location"`
}

// ToolAccess is what agents and people reach through muster.
type ToolAccess struct {
	AgentManager      Toggle      `json:"agentManager"`
	AdditionalServers []MCPServer `json:"additionalServers"`
}

// MCPServer is one server registered with muster.
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
	Mode                string               `json:"mode" yaml:"mode"`
	Provider            string               `json:"provider" yaml:"provider,omitempty"`
	Audiences           []string             `json:"audiences" yaml:"audiences,omitempty"`
	AuthorizationServer *AuthorizationServer `json:"authorizationServer" yaml:"authorizationServer,omitempty"`
}

// AuthorizationServer is an oauth server's authorization server.
type AuthorizationServer struct {
	Issuer                     string    `json:"issuer" yaml:"issuer"`
	AuthorizationEndpoint      string    `json:"authorizationEndpoint" yaml:"authorizationEndpoint"`
	TokenEndpoint              string    `json:"tokenEndpoint" yaml:"tokenEndpoint"`
	Scopes                     string    `json:"scopes" yaml:"scopes"`
	GrantScope                 string    `json:"grantScope" yaml:"grantScope,omitempty"`
	ExpectedIssuer             string    `json:"expectedIssuer" yaml:"expectedIssuer,omitempty"`
	ClientCredentialsSecretRef SecretRef `json:"clientCredentialsSecretRef" yaml:"clientCredentialsSecretRef"`
}

// SecretRef names a Secret in a namespace.
type SecretRef struct {
	Name      string `json:"name" yaml:"name"`
	Namespace string `json:"namespace" yaml:"namespace"`
}

// Federation is the cross-installation section.
type Federation struct {
	ConnectorID    string   `json:"connectorId"`
	BrokerClientID string   `json:"brokerClientId"`
	Targets        []Target `json:"targets"`
	Hubs           []string `json:"hubs"`
}

// Target is an installation a hub federates.
type Target struct {
	Installation string   `json:"installation"`
	Private      bool     `json:"private"`
	Groups       []string `json:"groups"`
}

// Chart is the meta chart release range.
type Chart struct {
	Semver string `json:"semver"`
}

// MusterKnobs is muster's deployment knobs.
type MusterKnobs struct {
	Resources *Resources `json:"resources"`
}

// Toggle is an enabled flag.
type Toggle struct {
	Enabled bool `json:"enabled"`
}

// KlausGateway is the chat gateway section.
type KlausGateway struct {
	Enabled bool     `json:"enabled"`
	Slack   *Slack   `json:"slack"`
	OBO     *OBO     `json:"obo"`
	A2A     *A2A     `json:"a2a"`
	Reviews *Reviews `json:"reviews"`
}

// Slack is the gateway's Slack mode.
type Slack struct {
	Mode             string   `json:"mode" yaml:"mode"`
	ChannelMode      string   `json:"channelMode" yaml:"channelMode"`
	ChannelAllowlist []string `json:"channelAllowlist" yaml:"channelAllowlist,omitempty"`
}

// OBO is the on-behalf-of section.
type OBO struct {
	Connectors bool `json:"connectors"`
}

// A2A is the agent-to-agent section.
type A2A struct {
	Enabled         bool   `json:"enabled" yaml:"enabled"`
	DefaultAgent    string `json:"defaultAgent" yaml:"defaultAgent"`
	SATokenAudience string `json:"saTokenAudience" yaml:"saTokenAudience,omitempty"`
}

// Reviews is the team-reviews section.
type Reviews struct {
	Enabled        bool     `json:"enabled" yaml:"enabled"`
	Audience       string   `json:"audience" yaml:"audience,omitempty"`
	AllowedCallers []string `json:"allowedCallers" yaml:"allowedCallers,omitempty"`
}

// ClusterManager is the cluster-manager section.
type ClusterManager struct {
	Enabled       bool           `json:"enabled"`
	NetworkPolicy *NetworkPolicy `json:"networkPolicy"`
}

// NetworkPolicy is the cluster-manager's egress policy.
type NetworkPolicy struct {
	WorkloadClusterFQDNPatterns []string            `json:"workloadClusterFqdnPatterns" yaml:"workloadClusterFqdnPatterns,omitempty"`
	EgressFQDNs                 []map[string]string `json:"egressFqdns" yaml:"egressFqdns,omitempty"`
}

// policy is definitions/agent-platform/policy.yaml.
type policy struct {
	Components map[string]struct {
		Customers []string `yaml:"customers"`
	} `yaml:"components"`
	LoginConnectorPin struct {
		Customers []string `yaml:"customers"`
	} `yaml:"loginConnectorPin"`
}

// offered says whether the policy offers the customer a component; a component
// the policy does not list is offered to everyone.
func (p policy) offered(component, customer string) bool {
	rule, listed := p.Components[component]
	return !listed || slices.Contains(rule.Customers, customer)
}

// Parse validates raw against the schema and returns the typed input. raw is
// the decoded document (from YAML or JSON): map[string]any at the top. A key
// the schema does not know, a missing required key or a wrong shape is
// ErrInput naming the location.
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
	var in Input
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	if in.Secrets.MusterOAuth == "" {
		in.Secrets.MusterOAuth = "muster-oauth-credentials"
	}
	if in.Secrets.MusterValkey == "" {
		in.Secrets.MusterValkey = "muster-valkey-credentials"
	}
	return &in, nil
}

// check applies the rules the schema cannot express: the fleet policy, what
// this version renders, and the supplied secret values.
func (in *Input) check(secrets map[string]string) error {
	var pol policy
	raw, err := definitions.FS.ReadFile("agent-platform/policy.yaml")
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, &pol); err != nil {
		return fmt.Errorf("agent-platform: policy: %w", err)
	}
	customer := in.Installation.Customer
	if in.KlausGateway != nil && in.KlausGateway.Enabled && !pol.offered("klaus-gateway", customer) {
		return fmt.Errorf("%w: component klaus-gateway is not offered to customer %q", ErrPolicy, customer)
	}
	if in.ClusterManager != nil && in.ClusterManager.Enabled && !pol.offered("cluster-manager", customer) {
		return fmt.Errorf("%w: component cluster-manager is not offered to customer %q", ErrPolicy, customer)
	}
	if in.Identity.LoginConnectorID != "" && !slices.Contains(pol.LoginConnectorPin.Customers, customer) {
		return fmt.Errorf("%w: identity.loginConnectorId: a login connector pin is not offered to customer %q", ErrPolicy, customer)
	}
	if len(in.Federation.Targets) > 0 {
		return fmt.Errorf("%w: federation.targets (the hub outputs)", ErrNotRendered)
	}
	if in.Portal.Enabled && in.Portal.AIChat != nil {
		return fmt.Errorf("%w: portal.aiChat (the portal files)", ErrNotRendered)
	}
	if in.Portal.Enabled && len(in.Portal.SkillsRepositories) > 0 {
		return fmt.Errorf("%w: portal.skillsRepositories (the portal files)", ErrNotRendered)
	}
	if in.Portal.Enabled && len(in.Portal.ClientIDs) == 0 {
		return fmt.Errorf("%w: portal.clientIds: a portal that signs people in on this installation has at least one Dex client id", ErrInput)
	}

	needed := in.suppliedSecretFields()
	for _, field := range needed {
		if secrets[field] == "" {
			return fmt.Errorf("%w: %s", ErrEmptySecret, field)
		}
	}
	for field := range secrets {
		if !slices.Contains(needed, field) {
			return fmt.Errorf("%w: %s", ErrUnknownSecret, field)
		}
	}
	return nil
}

// suppliedSecretFields lists the secret values the person supplies for this
// input, by field name. Everything else the platform needs is generated by
// the commit step from the placeholders in the fileset.
func (in *Input) suppliedSecretFields() []string {
	var fields []string
	if in.Kagent.Enabled && in.Kagent.ModelKeySecret == modelKeyManaged {
		fields = append(fields, fieldModelKey)
	}
	for _, s := range in.ToolAccess.AdditionalServers {
		if s.Auth.Mode == "oauth" {
			fields = append(fields,
				"toolAccess.additionalServers."+s.Name+".client-id",
				"toolAccess.additionalServers."+s.Name+".client-secret")
		}
	}
	sort.Strings(fields)
	return fields
}
