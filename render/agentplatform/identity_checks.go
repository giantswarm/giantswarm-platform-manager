package agentplatform

import (
	"fmt"
	"slices"
	"strings"
)

func (p *policy) identityCheckConnectors(installation Installation) ([]string, error) {
	connectors, selected := p.Federation.IdentityCheckConnectors[installation.Name]
	if !selected {
		return nil, nil
	}
	if connectors == nil {
		return nil, fmt.Errorf("%w: federation.identityCheckConnectors.%s must be a list", ErrPolicy, installation.Name)
	}
	for _, connector := range connectors {
		if strings.TrimSpace(connector) == "" {
			return nil, fmt.Errorf("%w: federation.identityCheckConnectors.%s contains an empty connector", ErrPolicy, installation.Name)
		}
	}
	names := p.Federation.Connector
	for _, hub := range installation.Federation.Connectors {
		if !slices.Contains(names.Staff, hub.Customer) {
			continue
		}
		connector, err := names.Exchange(hub.First, hub.Customer, hub.Hub, installation.Name)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrPolicy, err)
		}
		if !slices.Contains(connectors, connector) {
			return nil, fmt.Errorf("%w: federation.identityCheckConnectors.%s must include the selected staff exchange connector %s (federation.connector.exchangeThrough.%s)", ErrPolicy, installation.Name, connector, hub.Customer)
		}
	}
	return slices.Clone(connectors), nil
}
