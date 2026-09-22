package render

// The developer portal as the two definitions that touch it agree on it. The
// customer-portal definition renders the portal; the agent-platform definition
// renders the platform's section of it as a kustomize Component and, when the
// platform is enabled, owns the installation's dex-app configmap patch and so
// carries the portal's Dex client in it; the client's Secret stays the
// customer-portal definition's file. Both build the entry and the Component's
// name here, so the two filesets agree byte for byte and no path or object is
// rendered by both.

const (
	// PortalDexClientID is the portal's Dex client id on every installation
	// that hosts a portal.
	PortalDexClientID = "backstage"
	// PortalDexClientName is the client's display name in Dex.
	PortalDexClientName = "Dev Portal"
	// DexSecretKey is the key every Dex client Secret carries.
	DexSecretKey = "secret"
	// PortalPlatformDir is the platform's directory under the portal's
	// extras/backstage/: the kustomize Component the agent-platform definition
	// renders and the portal's kustomization lists.
	PortalPlatformDir = "agent-platform"
)

// DexClientSecretName is the Secret in Dex's namespace that carries a
// component's client secret.
func DexClientSecretName(component string) string { return "dex-client-" + component }

// PortalAuthProvider is the portal's sign-in provider on an installation's
// Dex, named as every installation-hosted portal names it.
func PortalAuthProvider(installation string) string { return "oidc-" + installation }

// PortalRedirectURI is where Dex sends the portal's login back to: the
// provider's handler frame.
func PortalRedirectURI(domain, installation string) string {
	return "https://" + domain + "/api/auth/" + PortalAuthProvider(installation) + "/handler/frame"
}

// PortalDexClient is the portal's entry in the dex-app configmap patch's
// oidc.extraStaticClients: the client id, its redirect URI on the portal's
// domain and a reference to the Secret the customer-portal definition renders
// in Dex's namespace.
func PortalDexClient(domain, installation string) Map {
	return Map{
		{Key: "id", Value: PortalDexClientID},
		{Key: "name", Value: PortalDexClientName},
		{Key: "redirectURIs", Value: []string{PortalRedirectURI(domain, installation)}},
		{Key: "secretRef", Value: Map{{Key: "name", Value: DexClientSecretName(PortalDexClientID)}, {Key: "key", Value: DexSecretKey}}},
	}
}

// PortalPlatformComponent is the entry the portal's extras/backstage/kustomization.yaml
// lists under components for the platform's directory.
func PortalPlatformComponent() string { return "./" + PortalPlatformDir + "/" }
