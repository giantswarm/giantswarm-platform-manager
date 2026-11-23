package render

import "slices"

// The federation login: a person signs in to a target's Dex through the
// connector the target registers for a hub, an OIDC authorization-code login
// at the hub's Dex. The target's Dex is the hub's client: the hub's Dex
// registers one client per target, redirecting to the target's Dex callback,
// and the target's connector presents that client: its id in the connector's
// config, its secret from a Secret in Dex's namespace the connector's
// clientSecretRef names, so no secret sits in the plaintext values. One generated value
// is the client's secret on both sides: the hub renders its Dex-side Secret,
// the target's own definition the connector's, each naming the other (Peer).
// The token exchange through the connector verifies the subject token against
// the hub's keys and never presents the client, so it stands with or without
// the login.

// federationScopes are the scopes the connector requests at the hub's Dex:
// groups carry the caller's authorization, and offline_access gives the
// connector a refresh token for sessions that outlive the hub's access token.
var federationScopes = []string{"openid", "profile", "email", "groups", "offline_access"}

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
	keyKey       = "key"
	keySecretRef = "secretRef"
	kindSecret   = "Secret"
)

// connectorClientSecretKey is the key of a connector Secret.
const connectorClientSecretKey = "client-secret"

// ConnectorSecretRef is the connector's clientSecretRef in the dex-app
// values: dex-app sets a variable of Dex's environment from the key and
// writes the connector's clientSecret as that variable.
func ConnectorSecretRef(connector string) Map {
	return Map{{Key: keyName, Value: ConnectorSecretName(connector)}, {Key: keyKey, Value: connectorClientSecretKey}}
}

// FederationConnectorConfig is the config of the connector the target's Dex
// at targetBaseDomain registers for hub: an oidc connector trusting the hub's
// Dex, https://dex.<hub base domain>, as the issuer and taking the groups its
// tokens carry, the hub's login client for target, and the target's callback.
// The client's secret is the entry's clientSecretRef.
//
// A target that reaches the hub's Dex through its tunnel (HubConnector.Tunnel)
// fetches discovery, keys, tokens and user info from the tunnel's Service and
// trusts its certificate by the mounted SPIFFE bundle (TunnelDexValues); the
// tokens' issuer stays the hub's public one (issuerAlias), and the person's
// browser still signs in at the hub's public authorization endpoint, which
// discovery names.
func FederationConnectorConfig(hub HubConnector, target, targetBaseDomain string) Map {
	issuer := "https://dex." + hub.BaseDomain
	config := Map{{Key: "issuer", Value: issuer}}
	if hub.Tunnel {
		tunnel := HubTunnelURL(hub.Hub)
		config = Map{
			{Key: "issuer", Value: tunnel},
			{Key: "issuerAlias", Value: issuer},
			{Key: "providerDiscoveryOverrides", Value: Map{
				{Key: "tokenURL", Value: tunnel + "/token"},
				{Key: "jwksURL", Value: tunnel + "/keys"},
				{Key: "userInfoURL", Value: tunnel + "/userinfo"},
			}},
			{Key: "rootCAs", Value: []string{tunnelBundleDir + "/" + tunnelBundleKey}},
		}
	}
	return append(config,
		Entry{Key: "insecureEnableGroups", Value: true},
		Entry{Key: "clientID", Value: FederationClient(target)},
		Entry{Key: "redirectURI", Value: FederationRedirectURI(targetBaseDomain)},
		Entry{Key: "scopes", Value: federationScopes},
	)
}

// HubTunnelApp is the tunnelport RemoteApp in the target's DexNamespace
// through which the target reaches hub's Dex, and the Service it renders:
// ghostunnel terminating TLS with the target's SPIFFE SVID in front of a
// Teleport application tunnel to the hub's Dex.
func HubTunnelApp(hub string) string { return "dex-" + hub }

// HubTunnelURL is the base URL of hub's Dex through the target's tunnel.
func HubTunnelURL(hub string) string {
	return "https://" + HubTunnelApp(hub) + "." + DexNamespace + ".svc.cluster.local:" + hubTunnelPort
}

// hubTunnelPort is the TLS port of a tunnel's Service.
const hubTunnelPort = "8443"

// The SPIFFE trust bundle a tunnel's certificate verifies against: tbot keeps
// it in a Secret in the tunnel's namespace, next to the SVID's private key,
// so Dex mounts the bundle's key alone. Dex reads rootCAs when it opens the
// connector, so a rotated bundle takes effect at Dex's next start.
const (
	tunnelBundleSecret = "tunnelport-spiffe-bundle" // #nosec G101 -- a Secret's name, not a value
	tunnelBundleKey    = "svid_bundle.pem"
	tunnelBundleVolume = "tunnelport-spiffe-bundle"
	tunnelBundleDir    = "/etc/dex-tunnelport"
)

// TunnelDexValues is the dex-app values' dex mapping that mounts the tunnel's
// trust bundle into Dex where one of connectors reaches its hub through a
// tunnel: the dex subchart's volumes and volumeMounts, which the chart's
// Deployment renders after its own. Nil where none does.
func TunnelDexValues(connectors []FederatedConnector) Map {
	if !slices.ContainsFunc(connectors, func(c FederatedConnector) bool { return c.Hub.Tunnel }) {
		return nil
	}
	volume := Map{
		{Key: keyName, Value: tunnelBundleVolume},
		{Key: "secret", Value: Map{
			{Key: "secretName", Value: tunnelBundleSecret},
			{Key: "items", Value: []Map{{{Key: keyKey, Value: tunnelBundleKey}, {Key: "path", Value: tunnelBundleKey}}}},
		}},
	}
	mount := Map{
		{Key: keyName, Value: tunnelBundleVolume},
		{Key: "mountPath", Value: tunnelBundleDir},
		{Key: "readOnly", Value: true},
	}
	return Map{
		{Key: "volumes", Value: []Map{volume}},
		{Key: "volumeMounts", Value: []Map{mount}},
	}
}

// ConnectorSecret is the target's Secret of connector in Dex's namespace: the
// hub's client id in plaintext and the generated secret, peered with the hub's
// Dex-side Secret of the client (HubFederationSecret).
func ConnectorSecret(connector, hub, target string) (string, File) {
	value := FederationSecretName(hub, target)
	f := Secret(ConnectorSecretName(connector), DexNamespace, nil,
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
