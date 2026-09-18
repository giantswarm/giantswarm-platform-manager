package customerportal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// Errors the render refuses with. Every message names the key or field
// concerned.
var (
	// ErrInput is an input the schema rejects: an unknown key, a missing
	// required one, a value of the wrong shape.
	ErrInput = errors.New("customer-portal: input")
	// ErrEmptySecret is a supplied secret value the render needs and did not
	// get, or got empty.
	ErrEmptySecret = errors.New("customer-portal: empty secret value")
	// ErrUnknownSecret is a supplied secret value no input asks for.
	ErrUnknownSecret = errors.New("customer-portal: unknown secret value")
)

// The supplied secret fields: the GitHub App's credentials when the github
// plugin is on, the Sentry DSNs and report URI when sentry is on. Named after
// the chart values they fill.
const (
	fieldGitHubClientID      = "plugins.github.clientId"
	fieldGitHubClientSecret  = "plugins.github.clientSecret"  // #nosec G101 -- a field name, not a value
	fieldGitHubPrivateKey    = "plugins.github.privateKey"    // #nosec G101 -- a field name, not a value
	fieldGitHubWebhookSecret = "plugins.github.webhookSecret" // #nosec G101 -- a field name, not a value
	fieldSentryAppDSN        = "plugins.sentry.appDsn"
	fieldSentryBackendDSN    = "plugins.sentry.backendDsn"
	fieldSentryReportURI     = "plugins.sentry.reportUri"
)

// defaultPluginKeyID names the portal's plugin-to-plugin signing key pair.
const defaultPluginKeyID = "plugin-to-plugin"

// Input is the typed form of definitions/customer-portal/schema.json. The
// schema is the contract; this struct is how the renderer reads it.
type Input struct {
	Installation Installation `json:"installation"`
	Portal       Portal       `json:"portal"`
	Chart        Chart        `json:"chart"`
	Plugins      Plugins      `json:"plugins"`
	Tunnel       Toggle       `json:"tunnel"`
	PluginKeys   PluginKeys   `json:"pluginKeys"`
}

// Installation is the facts on record; see the schema for each field.
type Installation struct {
	Name          string `json:"name"`
	BaseDomain    string `json:"baseDomain"`
	Customer      string `json:"customer"`
	Provider      string `json:"provider"`
	Region        string `json:"region"`
	Pipeline      string `json:"pipeline"`
	AgentPlatform bool   `json:"agentPlatform"`
}

// Portal is the portal as the person names it.
type Portal struct {
	Domain           string `json:"domain"`
	Title            string `json:"title"`
	Organization     string `json:"organization"`
	SupportURL       string `json:"supportUrl"`
	TelemetryDeckApp string `json:"telemetrydeckAppId"`
}

// Chart is the portal chart's release range.
type Chart struct {
	Line string `json:"line"`
}

// Plugins is the portal's plugin set beyond the always-on ones.
type Plugins struct {
	GitHub  GitHub  `json:"github"`
	Grafana Grafana `json:"grafana"`
	Flux    Flux    `json:"flux"`
	Sentry  Toggle  `json:"sentry"`
}

// GitHub is the GitHub integration through a GitHub App.
type GitHub struct {
	Enabled bool `json:"enabled"`
	AppID   int  `json:"appId"`
}

// Grafana is the Grafana plugin.
type Grafana struct {
	Enabled bool   `json:"enabled"`
	Domain  string `json:"domain"`
}

// Flux is the Flux plugin.
type Flux struct {
	Enabled               bool                   `json:"enabled"`
	GitRepositoryPatterns []GitRepositoryPattern `json:"gitRepositoryPatterns"`
}

// GitRepositoryPattern maps a git repository URL to a browsable file URL.
type GitRepositoryPattern struct {
	TargetURL               string `json:"targetUrl" yaml:"targetUrl"`
	GitRepositoryURLPattern string `json:"gitRepositoryUrlPattern" yaml:"gitRepositoryUrlPattern"`
}

// Toggle is an enabled flag.
type Toggle struct {
	Enabled bool `json:"enabled"`
}

// PluginKeys names the generated plugin signing key pair.
type PluginKeys struct {
	KeyID string `json:"keyId"`
}

// Parse validates raw against the schema and returns the typed input. raw is
// the decoded document (from YAML or JSON): map[string]any at the top. A key
// the schema does not know, a missing required key or a wrong shape is
// ErrInput naming the location.
func Parse(raw any) (*Input, error) {
	schemaBytes, err := definitions.FS.ReadFile("customer-portal/schema.json")
	if err != nil {
		return nil, err
	}
	schemaDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("customer-portal: schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("schema.json", schemaDoc); err != nil {
		return nil, fmt.Errorf("customer-portal: schema: %w", err)
	}
	schema, err := compiler.Compile("schema.json")
	if err != nil {
		return nil, fmt.Errorf("customer-portal: schema: %w", err)
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
	if in.Portal.Title == "" {
		in.Portal.Title = "Dev Portal"
	}
	if in.PluginKeys.KeyID == "" {
		in.PluginKeys.KeyID = defaultPluginKeyID
	}
	return &in, nil
}

// check applies the rules the schema cannot express: the plugin inputs that
// come with a plugin, and the supplied secret values.
func (in *Input) check(secrets map[string]string) error {
	if in.Plugins.GitHub.Enabled && in.Plugins.GitHub.AppID == 0 {
		return fmt.Errorf("%w: plugins.github.appId: the GitHub App's id", ErrInput)
	}
	if in.Plugins.Grafana.Enabled && in.Plugins.Grafana.Domain == "" {
		return fmt.Errorf("%w: plugins.grafana.domain: the Grafana instance the plugin links to", ErrInput)
	}
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
// input, by field name. Everything else the portal needs is generated by the
// commit step from the placeholders in the fileset.
func (in *Input) suppliedSecretFields() []string {
	var fields []string
	if in.Plugins.GitHub.Enabled {
		fields = append(fields, fieldGitHubClientID, fieldGitHubClientSecret, fieldGitHubPrivateKey, fieldGitHubWebhookSecret)
	}
	if in.Plugins.Sentry.Enabled {
		fields = append(fields, fieldSentryAppDSN, fieldSentryBackendDSN, fieldSentryReportURI)
	}
	sort.Strings(fields)
	return fields
}
