package render

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// The token-exchange connector: what an installation's Dex registers for a
// hub whose muster exchanges tokens into it. The hub's definition names the
// connector in its broker targets and identity providers (connectorId); the
// installation's own definition — agent-platform where the platform runs
// there, cluster-mcp-servers otherwise — renders it on the installation's
// Dex, next to the hub's token-exchange client. So one owner writes each
// installation's dex-app patch, and both sides render the name from the same
// facts of the registry and the same policy.

// HubConnector is a hub that brokers into an installation, with the facts the
// connector the installation's Dex registers for it is rendered from: the
// hub's name, its organisation, its base domain — its Dex is the connector's
// issuer — and whether it is the first of its organisation's hubs into the
// installation, which names the connector (ConnectorNames.Name). The
// registry's installation.federation.connectors, in both definitions'
// schemas.
type HubConnector struct {
	Hub        string `json:"hub"`
	Customer   string `json:"customer"`
	BaseDomain string `json:"baseDomain"`
	First      bool   `json:"first"`
}

// ConnectorNames are the agent-platform policy's federation.connector: the
// names of the connector a target's Dex registers for a hub, as Go templates
// over the hub's record ({{ .Customer }}, {{ .Name }}) — First for the
// target's first hub of the organisation, Further for every further one —
// and FromBase, the organisations whose hubs' connectors the fleet's Dex base
// registers on every installation, so no definition renders them.
type ConnectorNames struct {
	First    string   `yaml:"first"`
	Further  string   `yaml:"further"`
	FromBase []string `yaml:"fromBase"`
}

// ErrConnectorPolicy is a federation.connector entry of the policy that
// cannot name a connector: a template that does not parse, names a field the
// hub's record lacks or renders empty.
var ErrConnectorPolicy = errors.New("federation.connector")

// ConnectorPolicy reads federation.connector of the agent-platform policy
// (definitions/agent-platform/policy.yaml), the one rule both sides of the
// exchange name connectors by, whichever definition renders the Dex side.
func ConnectorPolicy() (ConnectorNames, error) {
	raw, err := definitions.FS.ReadFile("agent-platform/policy.yaml")
	if err != nil {
		return ConnectorNames{}, err
	}
	var pol struct {
		Federation struct {
			Connector ConnectorNames `yaml:"connector"`
		} `yaml:"federation"`
	}
	if err := yaml.Unmarshal(raw, &pol); err != nil {
		return ConnectorNames{}, fmt.Errorf("%w: %w", ErrConnectorPolicy, err)
	}
	return pol.Federation.Connector, nil
}

// hubRecord is what the templates see of a hub: the fields of the hub's
// record the policy's templates name.
type hubRecord struct{ Customer, Name string }

// Name is the connector a target's Dex registers for the hub hub of the
// organisation customer: the organisation's plain name where the hub is the
// first of the organisation's hubs into the target, the hub's own otherwise.
func (n ConnectorNames) Name(first bool, customer, hub string) (string, error) {
	key, text := "federation.connector.further", n.Further
	if first {
		key, text = "federation.connector.first", n.First
	}
	return PolicyTemplate(key, text, hubRecord{Customer: customer, Name: hub})
}

// Rendered says whether a definition renders the connector of a hub of the
// organisation customer: the fleet's Dex base registers the connectors of the
// organisations under fromBase on every installation, so a target renders
// none for their hubs.
func (n ConnectorNames) Rendered(customer string) bool { return !slices.Contains(n.FromBase, customer) }

// PolicyTemplate renders one of the policy's Go templates over data; a
// template that does not parse, names a field data lacks or renders empty is
// ErrConnectorPolicy naming the key.
func PolicyTemplate(key, text string, data any) (string, error) {
	tpl, err := template.New(key).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrConnectorPolicy, key, err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, data); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrConnectorPolicy, key, err)
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("%w: %s: renders empty", ErrConnectorPolicy, key)
	}
	return b.String(), nil
}

// ExchangeConnectors are the entries of the dex-app values' oidc.customer.connectors
// an installation's Dex registers for its hubs, in the hubs' order: for every
// hub the policy renders for, an oidc connector that trusts the hub's Dex as
// the issuer and takes the groups the hub's token carries, with no client of
// its own — Dex's token exchange verifies the subject token against the
// issuer's keys. Nil where there is none to render; the plan keeps every
// other entry of oidc.customer, the installation's own login connectors
// among them, as another owner's.
func ExchangeConnectors(names ConnectorNames, hubs []HubConnector) ([]Map, error) {
	var list []Map
	for _, h := range hubs {
		if !names.Rendered(h.Customer) {
			continue
		}
		id, err := names.Name(h.First, h.Customer, h.Hub)
		if err != nil {
			return nil, err
		}
		list = append(list, Map{
			{Key: "id", Value: id},
			{Key: "connectorType", Value: "oidc"},
			{Key: "connectorName", Value: h.Hub + " token exchange"},
			{Key: "connectorConfig", Value: "issuer: https://dex." + h.BaseDomain + "\ninsecureEnableGroups: true\n"},
		})
	}
	return list, nil
}
