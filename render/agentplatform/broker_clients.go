package agentplatform

// A manager's isolated broker client is identified by both its ID and Secret.
// A fleet client may use the same ID with its own credential Secret.
var managerBrokerClients = map[string]string{
	componentAgentManager:   agentManagerBrokerSecret,
	componentClusterManager: componentClusterManager + "-github-broker",
	componentModelManager:   componentModelManager + "-github-broker",
	componentSkillManager:   componentSkillManager + "-github-broker",
}

// IsManagerBrokerClient says whether a broker client on record, by its id and
// the Secret its clientCredentialsSecretRef names ("" for none), is one the
// render writes for a manager's own exchange, not the fleet's broker client.
func IsManagerBrokerClient(id, secret string) bool {
	want, ok := managerBrokerClients[id]
	return ok && secret == want
}
