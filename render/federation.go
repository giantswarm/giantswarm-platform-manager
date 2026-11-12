package render

import (
	"regexp"
	"strings"
)

// The federation login: a person signs in to a target's Dex through the
// connector the target registers for a hub, an OIDC authorization-code login
// at the hub's Dex. The target's Dex is the hub's client: the hub's Dex
// registers one client per target, redirecting to the target's Dex callback,
// and the target's connector presents that client. Its id and secret reach the
// connector through Dex's environment expansion of the connector config, from
// a Secret in Dex's namespace the target's dex-app values project into Dex's
// environment, so no secret sits in the plaintext values. One generated value
// is the client's secret on both sides: the hub renders its Dex-side Secret,
// the target's own definition the connector's, each naming the other (Peer).
// The token exchange through the connector verifies the subject token against
// the hub's keys and never presents the client, so it stands with or without
// the login.

// federationScopes are the scopes the connector requests at the hub's Dex:
// groups, so a login carries the groups a token exchange through the same
// connector takes from the subject token.
var federationScopes = []string{"openid", "profile", "email", "groups"}

// FederationClient is the id of the client a hub's Dex registers for target's
// Dex: one per target on each hub's Dex.
func FederationClient(target string) string { return "federation-" + target }

// FederationClientName is that client's display name on the hub's Dex.
func FederationClientName(target string) string { return target + " federation login" }

// FederationRedirectURI is the target's Dex callback, where the hub's Dex
// sends the login back to.
func FederationRedirectURI(targetBaseDomain string) string {
	return "https://dex." + targetBaseDomain + "/callback"
}

// FederationSecretName is the generated value shared by the hub's Dex-side
// Secret of the client for target and the target's connector Secret: named
// after both installations, since a target registers a connector per hub and
// a hub a client per target, so both filesets agree on the value they share.
func FederationSecretName(hub, target string) string {
	return "federation-" + hub + "-" + target + "-client-secret"
}

// ConnectorSecretName is the Secret in the target's Dex namespace that
// carries the hub client's id and secret for connector.
func ConnectorSecretName(connector string) string { return "dex-connector-" + connector }

// ConnectorSecretFile is that Secret's file name; it matches the fleet's
// .sops.yaml rules (.*(secret|credential).*) and the commit step's
// secret-file test.
func ConnectorSecretFile(connector string) string {
	return ConnectorSecretName(connector) + "-secret.yaml"
}

// Keys and kinds the Dex values and Secrets share.
const (
	keyName      = "name"
	keySecretRef = "secretRef"
	kindSecret   = "Secret"
)

// The keys of a connector Secret.
const (
	connectorClientIDKey     = "client-id"
	connectorClientSecretKey = "client-secret"
)

var envUnsafe = regexp.MustCompile(`[^A-Za-z0-9]`)

// connectorEnv is the prefix of connector's variables in Dex's environment,
// apart from the DEX_CLIENT_SECRET_ variables dex-app sets for the static
// clients' referenced secrets.
func connectorEnv(connector string) string {
	return "DEX_CONNECTOR_" + strings.ToUpper(envUnsafe.ReplaceAllString(connector, "_"))
}

// ConnectorClientIDEnv and ConnectorClientSecretEnv are the variables Dex's
// connector config expands to the hub client's id and secret.
func ConnectorClientIDEnv(connector string) string { return connectorEnv(connector) + "_CLIENT_ID" }
func ConnectorClientSecretEnv(connector string) string {
	return connectorEnv(connector) + "_CLIENT_SECRET"
}

// ConnectorEnvVars are the entries of the dex-app values' dex.envVars that
// project connector's Secret into Dex's environment. Optional: a Dex whose
// Secret is not there yet starts, its connector without a client, so the
// token exchange through it still works and the login fails.
func ConnectorEnvVars(connector string) []Map {
	ref := func(env, key string) Map {
		return Map{{Key: keyName, Value: env}, {Key: "valueFrom", Value: Map{{Key: "secretKeyRef", Value: Map{
			{Key: keyName, Value: ConnectorSecretName(connector)}, {Key: "key", Value: key}, {Key: "optional", Value: true}}}}}}
	}
	return []Map{
		ref(ConnectorClientIDEnv(connector), connectorClientIDKey),
		ref(ConnectorClientSecretEnv(connector), connectorClientSecretKey),
	}
}

// FederationConnectorConfig is the config of the connector a target's Dex
// registers for the hub at hubBaseDomain: an oidc connector trusting the
// hub's Dex as the issuer and taking the groups its tokens carry, the hub's
// client for the target through the environment, and the target's callback.
func FederationConnectorConfig(connector, hubBaseDomain, targetBaseDomain string) string {
	var b strings.Builder
	b.WriteString("issuer: https://dex." + hubBaseDomain + "\n")
	b.WriteString("insecureEnableGroups: true\n")
	b.WriteString("clientID: $" + ConnectorClientIDEnv(connector) + "\n")
	b.WriteString("clientSecret: $" + ConnectorClientSecretEnv(connector) + "\n")
	b.WriteString("redirectURI: " + FederationRedirectURI(targetBaseDomain) + "\n")
	b.WriteString("scopes:\n")
	for _, s := range federationScopes {
		b.WriteString("  - " + s + "\n")
	}
	return b.String()
}

// ConnectorSecret is the target's Secret of connector in Dex's namespace: the
// hub's client id in plaintext and the generated secret, peered with the hub's
// Dex-side Secret of the client (HubFederationSecret).
func ConnectorSecret(connector, hub, target string) (string, File) {
	value := FederationSecretName(hub, target)
	f := Secret(ConnectorSecretName(connector), DexNamespace, nil,
		ValueKey(connectorClientIDKey, FederationClient(target)),
		GeneratedKey(connectorClientSecretKey, value, Base64, 32)).
		Peered(value, Peer{Installation: hub, Path: PlatformSecretsPath(hub, DexClientSecretFile(FederationClient(target)))})
	return ConnectorSecretFile(connector), f
}

// HubFederationClient is the hub's entry of the dex-app values'
// oidc.extraStaticClients for target: the target's Dex callback as the one
// redirect URI, its secret read from the Secret HubFederationSecret renders.
func HubFederationClient(target, targetBaseDomain string) Map {
	client := FederationClient(target)
	return ExtraStaticClient(client, FederationClientName(target),
		Entry{Key: "redirectURIs", Value: []string{FederationRedirectURI(targetBaseDomain)}},
		Entry{Key: keySecretRef, Value: DexClientSecretRef(client)})
}

// HubFederationSecret is the hub's Dex-side Secret of the client for target,
// peered with the target's connector Secret, the file connector names under
// the target's agent-platform Secrets.
func HubFederationSecret(hub, target, connector string) (string, File) {
	client := FederationClient(target)
	value := FederationSecretName(hub, target)
	f := DexClientSecret(client, value).
		Peered(value, Peer{Installation: target, Path: PlatformSecretsPath(target, ConnectorSecretFile(connector))})
	return DexClientSecretFile(client), f
}
