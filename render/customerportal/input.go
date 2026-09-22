package customerportal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render"
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

// The supplied fields: the GitHub App's id and credentials when the github
// plugin is on (the id is no credential, but it lives only in the encrypted
// file, so no read-back recovers it and it is supplied like them), the
// Sentry DSNs and report URI when sentry is on. Named after the chart values
// they fill.
const (
	fieldGitHubAppID         = "plugins.github.appId"
	fieldGitHubClientID      = "plugins.github.clientId"
	fieldGitHubClientSecret  = "plugins.github.clientSecret"  // #nosec G101 -- a field name, not a value
	fieldGitHubPrivateKey    = "plugins.github.privateKey"    // #nosec G101 -- a field name, not a value
	fieldGitHubWebhookSecret = "plugins.github.webhookSecret" // #nosec G101 -- a field name, not a value
	fieldSentryAppDSN        = "plugins.sentry.appDsn"
	fieldSentryBackendDSN    = "plugins.sentry.backendDsn"
	fieldSentryReportURI     = "plugins.sentry.reportUri"
	// fieldTokenBroker prefixes the broker client's credentials; a federated
	// installation's are federation.<name>.clientId and clientSecret.
	fieldTokenBroker   = "federation.tokenBroker"
	suffixClientID     = ".clientId"
	suffixClientSecret = ".clientSecret" // #nosec G101 -- a field name, not a value
)

// federationField is the supplied field of a federated installation's or the
// broker's client credential.
func federationField(name, suffix string) string { return "federation." + name + suffix }

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
	Federation   *Federation  `json:"federation"`
	// missing are the required person inputs the document lacks, filled
	// with their Missing markers: a comparison renders them, a commit
	// refuses them.
	missing []missingInput
}

// Installation is the facts on record; see the schema for each field.
type Installation struct {
	Name          string   `json:"name"`
	BaseDomain    string   `json:"baseDomain"`
	Customer      string   `json:"customer"`
	Provider      string   `json:"provider"`
	Providers     []string `json:"providers"`
	Region        string   `json:"region"`
	Pipeline      string   `json:"pipeline"`
	AgentPlatform bool     `json:"agentPlatform"`
}

// Portal is the portal as the person names it.
type Portal struct {
	Domain              string         `json:"domain"`
	Title               string         `json:"title"`
	Organization        string         `json:"organization"`
	SupportURL          string         `json:"supportUrl"`
	TelemetryDeckApp    string         `json:"telemetrydeckAppId"`
	FriendlyLabels      []FriendlyName `json:"friendlyLabels"`
	FriendlyAnnotations []FriendlyName `json:"friendlyAnnotations"`
}

// FriendlyName is a label or annotation the portal's cluster pages show under
// a friendly name.
type FriendlyName struct {
	Key      string            `json:"key,omitempty" yaml:"key,omitempty"`
	Selector string            `json:"selector" yaml:"selector"`
	Variant  string            `json:"variant,omitempty" yaml:"variant,omitempty"`
	ValueMap map[string]string `json:"valueMap,omitempty" yaml:"valueMap,omitempty"`
}

// Federation is a portal over several installations of one customer.
type Federation struct {
	Installations      []FederatedInstallation `json:"installations"`
	SignInInstallation string                  `json:"signInInstallation"`
	TokenBroker        string                  `json:"tokenBroker"`
}

// FederatedInstallation is another installation the portal shows, with its
// facts on record.
type FederatedInstallation struct {
	Name          string   `json:"name"`
	BaseDomain    string   `json:"baseDomain"`
	Providers     []string `json:"providers"`
	Region        string   `json:"region"`
	Pipeline      string   `json:"pipeline"`
	AgentPlatform bool     `json:"agentPlatform"`
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

// GitHub is the GitHub integration through a GitHub App. AppID is never set
// in the document: the id is supplied at commit as plugins.github.appId, and
// the schema lists it for its place in the encrypted file.
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
// the schema does not know, a missing registry fact or a wrong shape is
// ErrInput naming the location. A required person input the document lacks
// is no refusal here: it is filled with its Missing marker and named by
// MissingInputs, for the render's mode to decide (a comparison renders the
// marker, a commit refuses the input). raw itself is left as it is.
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
	// produces into what the validator and encoding/json expect, and
	// copies the document: the markers are filled into the copy.
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	var copied map[string]any
	if err := json.Unmarshal(encoded, &copied); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	if copied == nil {
		copied = map[string]any{}
	}
	missing, err := fillMissing(copied, schemaBytes)
	if err != nil {
		return nil, err
	}
	filled := make(map[string]bool, len(missing))
	for _, m := range missing {
		filled[m.field] = true
	}
	if encoded, err = json.Marshal(copied); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInput, err)
	}
	if err := schema.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if !errors.As(err, &ve) || !explained(ve, filled) {
			return nil, fmt.Errorf("%w: %w", ErrInput, err)
		}
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
	if len(in.Installation.Providers) == 0 {
		in.Installation.Providers = []string{in.Installation.Provider}
	}
	in.missing = missing
	return &in, nil
}

// check applies the rules the schema cannot express — the required person
// inputs the document lacks, which a commit refuses and a comparison
// renders as markers — and the supplied values.
func (in *Input) check(secrets map[string]string, mode render.Mode) error {
	if mode == render.ModeCommit && len(in.missing) > 0 {
		return fmt.Errorf("%w: %s", ErrInput, missingClause(in.missing))
	}
	if in.Plugins.GitHub.AppID != 0 {
		return fmt.Errorf("%w: %s: supplied at commit like the GitHub App's credentials, not an input", ErrInput, fieldGitHubAppID)
	}
	if !slices.Contains(in.Installation.Providers, in.Installation.Provider) {
		return fmt.Errorf("%w: installation.providers: the installation's own provider %s is not among them", ErrInput, in.Installation.Provider)
	}
	if err := in.checkFederation(); err != nil {
		return err
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
	if in.Plugins.GitHub.Enabled && !appIDSupplied(secrets[fieldGitHubAppID]) {
		return fmt.Errorf("%w: %s: the GitHub App's id is a positive integer", ErrInput, fieldGitHubAppID)
	}
	return nil
}

// appIDSupplied reports whether a supplied GitHub App id is one: a positive
// integer, or a dry run's marker.
func appIDSupplied(value string) bool {
	if value == Supplied(fieldGitHubAppID) {
		return true
	}
	n, err := strconv.Atoi(value)
	return err == nil && n > 0
}

// checkFederation applies the federation's rules: every listed installation
// is another one, listed once; the sign-in installation and the token broker
// are the portal's own or one it federates.
func (in *Input) checkFederation() error {
	if in.Federation == nil {
		return nil
	}
	names := map[string]bool{in.Installation.Name: true}
	for _, f := range in.Federation.Installations {
		if names[f.Name] {
			return fmt.Errorf("%w: federation.installations: %s is the portal's own installation or listed twice", ErrInput, f.Name)
		}
		names[f.Name] = true
	}
	for field, name := range map[string]string{"signInInstallation": in.Federation.SignInInstallation, "tokenBroker": in.Federation.TokenBroker} {
		if name != "" && !names[name] {
			return fmt.Errorf("%w: federation.%s: %s is neither the portal's own installation nor one it federates", ErrInput, field, name)
		}
	}
	return nil
}

// suppliedSecretFields lists the values the person supplies for this input
// at commit, by field name: the secrets, and the GitHub App's id, which
// lives only in the encrypted file. Everything else the portal needs is
// generated by the commit step from the placeholders in the fileset.
func (in *Input) suppliedSecretFields() []string {
	var fields []string
	if in.Plugins.GitHub.Enabled {
		fields = append(fields, fieldGitHubAppID, fieldGitHubClientID, fieldGitHubClientSecret, fieldGitHubPrivateKey, fieldGitHubWebhookSecret)
	}
	if in.Plugins.Sentry.Enabled {
		fields = append(fields, fieldSentryAppDSN, fieldSentryBackendDSN, fieldSentryReportURI)
	}
	for _, inst := range in.providerInstallations() {
		if inst.Name != in.Installation.Name {
			fields = append(fields, federationField(inst.Name, suffixClientID), federationField(inst.Name, suffixClientSecret))
		}
	}
	if in.tokenBroker() != "" {
		fields = append(fields, fieldTokenBroker+suffixClientID, fieldTokenBroker+suffixClientSecret)
	}
	sort.Strings(fields)
	return fields
}

// providerInstallations are the installations the portal has a Dex provider
// and client credentials for: every one it shows — or, with a token broker,
// the sign-in installation alone, since cluster tokens for the others come
// through the broker.
func (in *Input) providerInstallations() []FederatedInstallation {
	if in.tokenBroker() != "" {
		return []FederatedInstallation{in.installation(in.signInInstallation())}
	}
	return in.installations()
}

// installations are the installations the portal shows, its own among the
// federation's, by name.
func (in *Input) installations() []FederatedInstallation {
	own := in.Installation
	all := []FederatedInstallation{{Name: own.Name, BaseDomain: own.BaseDomain, Providers: own.Providers, Region: own.Region, Pipeline: own.Pipeline, AgentPlatform: own.AgentPlatform}}
	if in.Federation != nil {
		all = append(all, in.Federation.Installations...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all
}

// installation is the listed installation of that name.
func (in *Input) installation(name string) FederatedInstallation {
	for _, i := range in.installations() {
		if i.Name == name {
			return i
		}
	}
	return FederatedInstallation{}
}

// signInInstallation is the installation whose Dex signs people in to the
// portal: the federation's, else the portal's own.
func (in *Input) signInInstallation() string {
	if in.Federation != nil && in.Federation.SignInInstallation != "" {
		return in.Federation.SignInInstallation
	}
	return in.Installation.Name
}

// tokenBroker is the installation whose muster brokers cluster tokens for
// the others; empty without one.
func (in *Input) tokenBroker() string {
	if in.Federation == nil {
		return ""
	}
	return in.Federation.TokenBroker
}
