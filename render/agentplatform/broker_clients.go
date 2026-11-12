package agentplatform

import "slices"

// managerBrokerClients are the broker clients the render writes under
// muster.muster.oauth.server.tokenExchangeBroker.brokerClients for a
// manager's own credential grant exchange, beside the fleet's broker client:
// agent-manager's (agentManager.githubTokenExchange.enabled, brokerValues),
// keyed by its component name. The render refuses a fleet broker client
// under one of these ids (validateAgentManagerCredentials), so none of them
// is ever the fleet's.
var managerBrokerClients = []string{componentAgentManager}

// IsManagerBrokerClient says whether a broker client id on record is one the
// render writes for a manager's own exchange, not the fleet's broker client.
func IsManagerBrokerClient(id string) bool {
	return slices.Contains(managerBrokerClients, id)
}
