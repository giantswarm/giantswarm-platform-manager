package agentplatform

// managerBrokerClients are the broker clients the render writes under
// muster.muster.oauth.server.tokenExchangeBroker.brokerClients for a
// manager's own credential grant exchange, beside the fleet's broker client,
// each by its client id and the Secret its clientCredentialsSecretRef names:
// agent-manager's (agentManager.githubTokenExchange.enabled, brokerValues).
// The id alone does not tell them apart: with the exchange off the render
// takes a fleet broker client of the same id (validateAgentManagerCredentials
// reserves the id only with the exchange on) and writes it with the fleet's
// Secret, brokerClients.
var managerBrokerClients = map[string]string{componentAgentManager: agentManagerBrokerSecret}

// IsManagerBrokerClient says whether a broker client on record, by its id and
// the Secret its clientCredentialsSecretRef names ("" for none), is one the
// render writes for a manager's own exchange, not the fleet's broker client.
func IsManagerBrokerClient(id, secret string) bool {
	want, ok := managerBrokerClients[id]
	return ok && secret == want
}
