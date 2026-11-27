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
// A target that reaches the hub's Dex through a tunnel (HubConnector.Tunnel)
// fetches discovery, keys, tokens and user info from the tunnel's Service and
// trusts its certificate by the tunnel's trust bundle, which TunnelDexValues
// mounts; the tokens' issuer stays the hub's public one (issuerAlias), and the
// person's browser still signs in at the hub's public authorization endpoint,
// which discovery names.
func FederationConnectorConfig(hub HubConnector, target, targetBaseDomain string) Map {
	issuer := "https://dex." + hub.BaseDomain
	config := Map{{Key: "issuer", Value: issuer}}
	if t := hub.Tunnel; t != nil {
		tunnel := "https://" + TunnelHost(t.Service, t.Namespace)
		config = Map{
			{Key: "issuer", Value: tunnel},
			{Key: "issuerAlias", Value: issuer},
			{Key: "providerDiscoveryOverrides", Value: Map{
				{Key: "tokenURL", Value: tunnel + "/token"},
				{Key: "jwksURL", Value: tunnel + "/keys"},
				{Key: "userInfoURL", Value: tunnel + "/userinfo"},
			}},
			{Key: "rootCAs", Value: []string{tunnelBundleDir(t.BundleSecret) + "/" + TunnelBundleKey}},
		}
	}
	return append(config,
		Entry{Key: "insecureEnableGroups", Value: true},
		Entry{Key: "clientID", Value: FederationClient(target)},
		Entry{Key: "redirectURI", Value: FederationRedirectURI(targetBaseDomain)},
		Entry{Key: "scopes", Value: federationScopes},
	)
}

// HubTunnel is the tunnel through which an installation reaches a hub's Dex,
// as teleport-fleet declares it and the installation's tunnelport release
// runs it: the tunnel's Service in the installation's tunnelport namespace,
// and the Secret its trust bundle is written to.
type HubTunnel struct {
	Service      string `json:"service"`
	Namespace    string `json:"namespace"`
	BundleSecret string `json:"bundleSecret"`
}

// tunnelBundleRoot is where Dex mounts the tunnels' trust bundles, one
// directory per bundle Secret.
const tunnelBundleRoot = "/etc/dex-tunnelport"

func tunnelBundleDir(secret string) string { return tunnelBundleRoot + "/" + secret }

// TunnelDexValues is the dex-app values' dex mapping that mounts the trust
// bundle of every tunnel connectors reach their hubs through into Dex: the
// dex subchart's volumes and volumeMounts, which the chart's Deployment
// renders after its own, one volume per bundle Secret named after it, holding
// the bundle's key alone, since the Secret also holds the SVID's private key.
// Dex reads rootCAs when it opens the connector, so a rotated bundle takes
// effect at Dex's next start. Nil where no connector is tunnelled.
func TunnelDexValues(connectors []FederatedConnector) Map {
	var volumes, mounts []Map
	var seen []string
	for _, c := range connectors {
		t := c.Hub.Tunnel
		if t == nil || slices.Contains(seen, t.BundleSecret) {
			continue
		}
		seen = append(seen, t.BundleSecret)
		volumes = append(volumes, Map{
			{Key: keyName, Value: t.BundleSecret},
			{Key: "secret", Value: Map{
				{Key: "secretName", Value: t.BundleSecret},
				{Key: "items", Value: []Map{{{Key: keyKey, Value: TunnelBundleKey}, {Key: "path", Value: TunnelBundleKey}}}},
			}},
		})
		mounts = append(mounts, Map{
			{Key: keyName, Value: t.BundleSecret},
			{Key: "mountPath", Value: tunnelBundleDir(t.BundleSecret)},
			{Key: "readOnly", Value: true},
		})
	}
	if volumes == nil {
		return nil
	}
	return Map{{Key: "volumes", Value: volumes}, {Key: "volumeMounts", Value: mounts}}
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
