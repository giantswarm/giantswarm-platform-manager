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
	// PortalDir is the portal's own directory under its extras/backstage/:
	// the kustomization, the app-config and values ConfigMaps and the Secrets
	// the customer-portal definition renders, listed by the tree's
	// kustomization.
	PortalDir = "backstage"
	// PortalPlatformDir is the platform's directory under the portal's
	// extras/backstage/: the kustomize Component the agent-platform definition
	// renders and the portal's kustomization lists.
	PortalPlatformDir = "agent-platform"
	// PortalGrafanaProxy is the portal's proxy endpoint for its Grafana
	// plugin, the path the plugin's dashboards card reads Grafana's search
	// API through. The customer-portal definition renders the entry where
	// the plugin is wired; its presence on record is the fact the plugin
	// reads back as wired and the agent-platform definition reads for the
	// portal's extension list.
	PortalGrafanaProxy = "/grafana/api"
	// portalSharedConfig is the fleet base's shared config the portal's
	// app-config includes by anchor.
	portalSharedConfig = "shared-config.yaml#"
	// portalExtensions is the anchor of the fleet's baseline extension list;
	// the anchors of its supersets append one suffix per addition.
	portalExtensions                  = "extensions"
	portalExtensionsAgentPlatform     = "AgentPlatform"
	portalExtensionsAiChat            = "AiChat"
	portalExtensionsGrafanaDashboards = "GrafanaDashboards"
)

// PortalSharedInclude is the $include of one anchor of the fleet base's
// shared-config.yaml.
func PortalSharedInclude(anchor string) string { return portalSharedConfig + anchor }

// PortalExtensionsInclude is the $include of the portal's extension list.
// Backstage replaces app.extensions wholesale per app-config file and
// $include cannot append to a list, so the fleet's shared config carries one
// full list per combination, named by what it adds to the baseline: the
// agent platform's section where the platform runs, the AI chat's page and
// drawer where the platform's section carries the chat (the chat is the
// platform's, and the fleet carries no list with the chat without the
// platform's section, so the suffix follows the platform's), the Grafana
// dashboards card where the portal's Grafana plugin is wired (the card reads
// through PortalGrafanaProxy and is disabled in the app until switched on).
// The customer-portal definition includes the list without the platform in
// the portal's app-config; the agent-platform definition's Component includes
// the one with it in its fragment, which wins.
func PortalExtensionsInclude(agentPlatform, aiChat, grafanaWired bool) string {
	anchor := portalExtensions
	if agentPlatform {
		anchor += portalExtensionsAgentPlatform
		if aiChat {
			anchor += portalExtensionsAiChat
		}
	}
	if grafanaWired {
		anchor += portalExtensionsGrafanaDashboards
	}
	return PortalSharedInclude(anchor)
}

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

// TunnelServiceHost is the in-cluster host of a tunnelled app's Service on a
// hub, the way the agent-platform definition names it and a portal's
// kubernetes plugin reaches a private installation's API through it:
// <app>-<installation>.agent-platform.svc.cluster.local:8443.
func TunnelServiceHost(app, installation string) string {
	return app + "-" + installation + ".agent-platform.svc.cluster.local:8443"
}
