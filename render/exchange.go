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
// and the organisations the policy treats apart:
//   - Staff: Dex registers their hubs' connectors under oidc.giantswarm, each
//     named by Further, since dex-operator registers First there.
//   - FurtherByHand: their further hubs' connectors are kept by hand on the
//     targets, so no definition renders them.
//   - ExchangeThrough: the connector their first hub's broker exchanges tokens
//     through, where it is not the rendered one. A connector's id is part of
//     every Dex subject issued through it.
type ConnectorNames struct {
	First           string            `yaml:"first"`
	Further         string            `yaml:"further"`
	Staff           []string          `yaml:"staff"`
	FurtherByHand   []string          `yaml:"furtherByHand"`
	ExchangeThrough map[string]string `yaml:"exchangeThrough"`
}

// The dex-app values' connector owners: oidc.<owner>.connectors.
const (
	OwnerGiantswarm = "giantswarm"
	OwnerCustomer   = "customer"
)

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
// first of the organisation's hubs into the target, the hub's own otherwise
// and for every hub of a staff organisation.
func (n ConnectorNames) Name(first bool, customer, hub string) (string, error) {
	key, text := "federation.connector.further", n.Further
	if first && !slices.Contains(n.Staff, customer) {
		key, text = "federation.connector.first", n.First
	}
	return PolicyTemplate(key, text, hubRecord{Customer: customer, Name: hub})
}

// Rendered says whether a definition renders the connector of a hub of the
// organisation customer, the target's first hub of the organisation or a
// further one: every one but a further hub's of an organisation under
// furtherByHand, which is kept by hand on the target.
func (n ConnectorNames) Rendered(customer string, first bool) bool {
	return first || !slices.Contains(n.FurtherByHand, customer)
}

// Exchange is the connector the broker of the hub hub of the organisation
// customer exchanges tokens through at a target's Dex: the rendered one
// (Name), or for the organisation's first hub the one ExchangeThrough names.
func (n ConnectorNames) Exchange(first bool, customer, hub string) (string, error) {
	if id := n.ExchangeThrough[customer]; first && id != "" {
		return id, nil
	}
	return n.Name(first, customer, hub)
}

// Owner is the dex-app values' owner a target's Dex registers the connector
// of a hub of the organisation customer under.
func (n ConnectorNames) Owner(customer string) string {
	if slices.Contains(n.Staff, customer) {
		return OwnerGiantswarm
	}
	return OwnerCustomer
}

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

// FederatedConnector is a connector a target's Dex registers for a hub that
// brokers into it, as a definition renders it: the policy's name and the
// hub's record.
type FederatedConnector struct {
	ID    string
	Owner string
	Hub   HubConnector
}

// FederatedConnectors are the connectors a target's Dex registers for its
// hubs, in the hubs' order: one for every hub the policy renders for
// (ConnectorNames.Rendered). Nil where there is none to render.
func FederatedConnectors(names ConnectorNames, hubs []HubConnector) ([]FederatedConnector, error) {
	var list []FederatedConnector
	for _, h := range hubs {
		if !names.Rendered(h.Customer, h.First) {
			continue
		}
		id, err := names.Name(h.First, h.Customer, h.Hub)
		if err != nil {
			return nil, err
		}
		list = append(list, FederatedConnector{ID: id, Owner: names.Owner(h.Customer), Hub: h})
	}
	return list, nil
}

// Entry is the connector's entry of the dex-app values'
// oidc.<Owner>.connectors map on the target installation at
// targetBaseDomain, keyed by ID: an oidc connector trusting the hub's Dex as
// the issuer and taking the groups its tokens carry, presenting the hub's
// login client for the target, whose secret the chart reads from the
// connector Secret (clientSecretRef). The plan keeps every other entry under
// the owner, the installation's own login connectors among them, as another
// owner's.
func (c FederatedConnector) Entry(target, targetBaseDomain string) Map {
	return Map{
		{Key: "connectorType", Value: "oidc"},
		{Key: "connectorName", Value: c.Hub.Hub},
		{Key: "config", Value: FederationConnectorConfig(c.Hub.BaseDomain, target, targetBaseDomain)},
		{Key: "clientSecretRef", Value: ConnectorSecretRef(c.ID)},
	}
}

// ConnectorsByOwner are the entries of the dex-app values' oidc mapping that
// carry connectors on the target installation at targetBaseDomain:
// <owner>.connectors, a map keyed by id, for each owner in the order it first
// occurs.
func ConnectorsByOwner(connectors []FederatedConnector, target, targetBaseDomain string) Map {
	var owners []string
	byOwner := map[string]Map{}
	for _, c := range connectors {
		if _, ok := byOwner[c.Owner]; !ok {
			owners = append(owners, c.Owner)
		}
		byOwner[c.Owner] = append(byOwner[c.Owner], Entry{Key: c.ID, Value: c.Entry(target, targetBaseDomain)})
	}
	out := make(Map, 0, len(owners))
	for _, owner := range owners {
		out = append(out, Entry{Key: owner, Value: Map{{Key: "connectors", Value: byOwner[owner]}}})
	}
	return out
}
