package agentplatform

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The developer portal's agent-platform section. The portal itself is the
// customer-portal definition's: its app-config, values and the sources of its
// HelmRelease are that definition's files. This definition contributes a
// directory of its own next to them, a kustomize Component the portal's
// extras/backstage/kustomization.yaml lists: the platform's Backstage
// configuration as an extra app-config file (the chart mounts it from a
// ConfigMap and passes it as --config), the chart values that mount it, and a
// patch that appends those values to the portal HelmRelease's sources.
//
// Backstage merges its config files and Helm merges a HelmRelease's valuesFrom
// entries the same way: objects key by key, lists and scalars replaced by the
// later source, never appended. Appended last, the Component's lists win. The
// Component therefore sets a list only where it is the portal's sole source of
// it. A portal the customer-portal definition renders includes the shared
// extension list, so there the Component names the platform's extensions and
// the installation's muster: the shared list with the platform's section,
// with the AI chat where the chat is on, and with the Grafana dashboards
// card where the portal's Grafana plugin is wired
// (installation.portals[*].grafanaWired, read from the record: the proxy
// endpoint the customer-portal definition renders for it), since the
// fragment's list is the one Backstage keeps. A hand-kept portal — one whose
// app-config on record carries a literal extension list of its own, the
// hub's Dev Portal among them (installation.portals[*].handKept, read from
// the record) — owns its extensions and its muster registry; there the
// Component writes only object-shaped keys (the kagent installation, the
// fragment's mount, the chat's blocks), which merge. The day the
// customer-portal definition renders such a portal the fact reads false and
// the Component takes the lists over. The portal's environment,
// backstage.extraEnvVars, is the portal's own whatever the portal: the
// customer-portal definition's user-values carry the whole list (the avatars
// image source, the tunnel's CA variable) and the Component sets none, so no
// third source contends for it.
//
// The AI chat (aiChat.enabled, aiChat.model, aiChat.provider) is the
// platform's: its servers are the installation's muster and the portal's own
// MCP actions server, so the Component carries the whole of it — the aiChat
// block with the model and the two servers, mcpActions and backend.actions
// (what the actions service lists for the chat's actions server; without
// the sources it lists nothing and the server has no tool), and the chat's
// extensions through the shared include on a rendered portal. The chat runs
// Claude on Anthropic's API or on Vertex AI: on Anthropic's API the key is
// supplied at commit and lands in the Component's own credentials Secret as
// the chart value the chart exposes as ANTHROPIC_API_KEY; on Vertex the
// block names the provider and the Google project, location and the mounted
// credentials file, the Component's values carry the project and location
// the chart exports, and the service account's JSON is supplied at commit
// into the same Secret as the chart value the chart mounts at that file.
// The Secret is appended to the portal HelmRelease's values sources by the
// Component's patch. Where the portal's own app-config carries the chat by
// hand (installation.portals[*].handKeptChat, read from the record) its
// environment supplies the credential already, so the Component renders the
// blocks and no Secret and asks for no value — as it sets no list on a
// hand-kept portal — and a wave, which carries no supplied value, reconciles
// such a portal. The credential becomes the Component's when the hand-kept
// block goes (the customer-portal definition's planned move), through an
// enable of that installation alone with it supplied.
//
// The portal's chart line (installation.portals[*].chartLine) decides one
// key. Before backstage 1.1.0 the portal's agent-platform plugin composes the
// agent's HelmRelease in the browser and applies it, and the management
// clusters' Flux multi-tenancy policy refuses a HelmRelease without
// spec.serviceAccountName: that plugin reads the identity from
// agentPlatform.fluxServiceAccountName, which the fragment names for it. From
// 1.1.0 agents are created through agent-manager over muster as the signed-in
// person and no chart or plugin reads the key, so it is not written.

const (
	// backstageNamespace is the portal's release namespace, where the chart
	// mounts ConfigMaps from.
	backstageNamespace = "backstage"
	// portalDir is the platform's directory under the portal's extras/backstage/.
	portalDir = render.PortalPlatformDir
	// portalAppConfigMap carries the platform's app-config fragment.
	portalAppConfigMap = "agent-platform-app-config-backstage"
	// portalAppConfigFile is the fragment's file name in the portal's container.
	portalAppConfigFile = "app-config.agent-platform.yaml"
	// portalValuesMap carries the platform's chart values.
	portalValuesMap = "agent-platform-values-backstage"
	// portalCredentialsSecret carries the chat's provider credentials as
	// chart values: the Anthropic API key the chart exposes as
	// ANTHROPIC_API_KEY, or the Google service account's JSON the chart
	// mounts at googleCredentialsPath. Its file's name matches the fleet's
	// sops rules.
	portalCredentialsSecret = "agent-platform-ai-chat-credentials-backstage" // #nosec G101 -- a Secret name, not a value
	portalCredentialsFile   = "ai-chat-credentials.enc.yaml"                 // #nosec G101 -- a file name, not a value
	// fieldAnthropicKey is the supplied field carrying the chat's Anthropic
	// API key; fieldGoogleCredentials the one carrying a Vertex chat's
	// service-account JSON.
	fieldAnthropicKey      = "aiChat.anthropic.apiKey"       // #nosec G101 -- a field name, not a value
	fieldGoogleCredentials = "aiChat.google.credentialsJson" // #nosec G101 -- a field name, not a value
	// The chat's providers of Claude: Anthropic's API, or Vertex AI.
	providerAnthropic = "anthropic"
	providerVertex    = "vertex"
	// googleCredentialsPath is where the chart mounts google.credentialsJson,
	// the file the chat plugin reads the Vertex credentials from.
	googleCredentialsPath = "/app/google/credentials.json" // #nosec G101 -- a mount path, not a value
	// anthropicKeyEnv is the variable the chart exposes the key as; the
	// doubled dollar survives the fleet's variable substitution, so Backstage
	// reads the variable, as every portal app-config on record writes it.
	anthropicKeyEnv = "$${ANTHROPIC_API_KEY}"
	// chatActionsServer is the chat's entry for the portal's own MCP actions
	// server, in-process at chatActionsPath, called with the signed-in
	// person's Backstage token.
	chatActionsServer = "backstage-actions"
	chatActionsPath   = "/api/mcp-actions/v1"
	// chatMusterServer is the chat's entry for the installation's muster.
	chatMusterServer = "muster"
	// fluxServiceAccount is the tenant identity of the agents' HelmReleases,
	// created by the fleet's agent-platform base in the kagent namespace.
	fluxServiceAccount = "kagent-flux"
	// portalPluginRemoval is the first portal chart whose agent-platform
	// plugin creates agents through agent-manager and reads no
	// fluxServiceAccountName.
	portalPluginRemoval = "1.1.0"
	// portalFragmentChecksum is the first portal chart whose extraAppConfig
	// entries take a checksum and roll the pod when it changes; an earlier
	// chart's schema refuses the key.
	portalFragmentChecksum = "2.60.2"
)

// The actions service lists the actions of these plugins for the chat's
// actions server, every one but the hub's PagerDuty lookup.
var (
	chatActionSources  = []string{"auth", "catalog", "gs"}
	chatActionExcludes = []string{"gs:get-pagerduty-ids-for-entity"}
)

// portalAuthProvider is the portal's sign-in provider on this installation's
// Dex, named as every installation-hosted portal names it.
func (in *Input) portalAuthProvider() string { return render.PortalAuthProvider(in.Installation.Name) }

// aiChat says whether the portal section carries the AI chat: the person's
// choice; checkRecord has refused it without a hosted portal.
func (in *Input) aiChat() bool { return in.AIChat.Enabled }

// chatKeyIsComponents says whether the Component carries the chat's
// credential: the chat is on and the hosted portal's own app-config does not
// carry the chat by hand — there the portal's environment supplies it already.
func (in *Input) chatKeyIsComponents() bool {
	return in.aiChat() && !in.hostedPortal().HandKeptChat
}

// aiChatVertex says whether the chat runs Claude on Vertex AI.
func (in *Input) aiChatVertex() bool { return in.aiChat() && in.AIChat.Provider == providerVertex }

// chatSecretFields are the supplied secret values the chat needs where the
// Component carries its credential: its Anthropic API key, or on Vertex the
// service account's JSON.
func (in *Input) chatSecretFields() []string {
	switch {
	case !in.chatKeyIsComponents():
		return nil
	case in.aiChatVertex():
		return []string{fieldGoogleCredentials}
	}
	return []string{fieldAnthropicKey}
}

// portalOwnsLists says whether the Component is the portal's sole source of its
// list-shaped keys (app.extensions, muster.installations) and so sets them. A
// hand-kept portal carries its own; a list the Component set there would
// replace it.
func (in *Input) portalOwnsLists() bool {
	p := in.hostedPortal()
	return p != nil && !p.HandKept
}

// musterEntry is the installation's muster as the portal reaches it, under
// the given name: the installation's in the muster plugin's list, "muster"
// in the chat's server list. Its authProvider is the portal's sign-in
// provider on this installation's Dex, oidc-<installation>: the muster
// plugin and the chat send that provider's ID token on the home installation
// and the token the cluster token broker mints from that Dex elsewhere, and
// muster trusts the portal's Dex client as an audience. The portal builds a
// dedicated OAuth provider only for an auth.providers key with the mcp-
// prefix and the platform declares none, so an mcp-* name here would promise
// a login that is not there and switch the picker to a token muster rejects
// the day someone declares one.
func (in *Input) musterEntry(name string) render.Map {
	return render.Map{e("name", name), e("url", "https://"+in.host("muster")+"/mcp"), e("authProvider", in.portalAuthProvider())}
}

// aiChatSection is the chat's aiChat block: the provider — Anthropic's API
// with the key from the chart's environment, or Vertex AI with the Google
// project, location and the mounted credentials file —, the model, and the
// chat's servers: the portal's own MCP actions server with the signed-in
// person's Backstage token, and the installation's muster.
func (in *Input) aiChatSection() render.Map {
	var m render.Map
	if in.aiChatVertex() {
		g := in.AIChat.Google
		m = render.Map{e("anthropic", render.Map{e("provider", providerVertex)}),
			e("google", render.Map{e("project", g.Project), e("location", g.Location), e("keyFilename", googleCredentialsPath)})}
	} else {
		m = render.Map{e("anthropic", render.Map{e("apiKey", anthropicKeyEnv)})}
	}
	actions := render.Map{e("name", chatActionsServer), e("url", "https://"+in.hostedPortal().Domain+chatActionsPath), e("useBackstageUserToken", true)}
	return append(m, e("model", in.AIChat.Model), e("mcp", []render.Map{actions, in.musterEntry(chatMusterServer)}))
}

// chatActions is backend.actions: the plugins whose actions the actions
// service lists for the chat's MCP actions server, and the filter.
func chatActions() render.Map {
	var excludes []render.Map
	for _, id := range chatActionExcludes {
		excludes = append(excludes, render.Map{e("id", id)})
	}
	return render.Map{e("pluginSources", chatActionSources), e("filter", render.Map{e("exclude", excludes)})}
}

// portalAppConfig is the platform's app-config fragment: the agent-platform
// plugin's section where kagent runs (the agents' Flux identity where the
// portal's plugin reads it, and the installation among the kagent
// installations); where the Component owns the portal's lists, the
// platform's extensions through the shared include (with the chat's
// entries where the chat is on, with the Grafana dashboards card where the
// portal's plugin is wired) and the installation's muster; and, with the
// chat on, the aiChat block, the actions server's tool naming and the
// actions the service lists for it.
func (in *Input) portalAppConfig() render.Map {
	m := render.Map{}
	if in.portalOwnsLists() {
		m = append(m, e("app", render.Map{e("extensions", render.Map{e("$include", render.PortalExtensionsInclude(true, in.aiChat(), in.hostedPortal().GrafanaWired))})}))
	}
	if in.kagent() {
		platform := render.Map{}
		if in.portalReadsFluxServiceAccount() {
			platform = append(platform, e("fluxServiceAccountName", fluxServiceAccount))
		}
		platform = append(platform, e("kagent", render.Map{e("installations", render.Map{e(in.Installation.Name, render.Map{})})}))
		m = append(m, e("agentPlatform", platform))
	}
	if in.portalOwnsLists() {
		m = append(m, e("muster", render.Map{e("installations", []render.Map{in.musterEntry(in.Installation.Name)})}))
	}
	if in.aiChat() {
		m = append(m, e("aiChat", in.aiChatSection()),
			e("mcpActions", render.Map{e("namespacedToolNames", false)}),
			e("backend", render.Map{e("actions", chatActions())}))
	}
	return m
}

// portalValues are the platform's chart values: the fragment mounted as an
// extra app-config file, with its checksum where the portal's chart rolls
// the pod on it (Backstage reads the file at start, and a changed ConfigMap
// alone changes nothing the HelmRelease sees), and, for a chat on Vertex,
// the Google project and location the chart exports to the pod. No list:
// the portal's environment is the customer-portal definition's.
func (in *Input) portalValues() render.Map {
	fragment := render.Map{e("filename", portalAppConfigFile), e("configMapRef", portalAppConfigMap)}
	if in.portalRollsOnFragment() {
		fragment = append(fragment, e("checksum", fmt.Sprintf("%x", sha256.Sum256(render.MustYAML(in.portalAppConfig())))))
	}
	m := render.Map{e("backstage", render.Map{e("extraAppConfig", []render.Map{fragment})})}
	if in.aiChatVertex() {
		m = append(m, e("google", render.Map{e("project", in.AIChat.Google.Project), e("location", in.AIChat.Google.Location)}))
	}
	return m
}

// portalChartFloor is the lowest chart version a portal's chart line admits:
// the lower bound of a bounded range (>=A <B, the form the customer-portal
// definition writes) or the tag itself. Another form is an error naming it.
func portalChartFloor(line string) (*semver.Version, error) {
	fields := strings.Fields(line)
	var floor string
	switch {
	case len(fields) == 2 && strings.HasPrefix(fields[0], ">=") && strings.HasPrefix(fields[1], "<"):
		floor = strings.TrimPrefix(fields[0], ">=")
	case len(fields) == 1 && !strings.ContainsAny(fields[0], "<>=~^*xX|"):
		floor = fields[0]
	default:
		return nil, fmt.Errorf("the chart line %q is neither a bounded range >=A <B nor a tag", line)
	}
	v, err := semver.NewVersion(floor)
	if err != nil {
		return nil, fmt.Errorf("the chart line %q: %w", line, err)
	}
	return v, nil
}

// portalChartAdmits says whether a portal's chart line admits a chart at or
// above v, the chart Flux resolves the line to once v is released: the upper
// bound of a bounded range lies above v, or the tag is v or later.
func portalChartAdmits(line string, v *semver.Version) bool {
	floor, err := portalChartFloor(line)
	if err != nil {
		return false
	}
	if fields := strings.Fields(line); len(fields) == 2 {
		ceiling, err := semver.NewVersion(strings.TrimPrefix(fields[1], "<"))
		return err == nil && ceiling.GreaterThan(v)
	}
	return !floor.LessThan(v)
}

// portalRollsOnFragment says whether the hosted portal's chart takes the
// fragment's checksum: its chart line resolves to portalFragmentChecksum or
// later. A portal whose line is not on record gets no checksum, which an
// earlier chart would refuse.
func (in *Input) portalRollsOnFragment() bool {
	p := in.hostedPortal()
	return p != nil && portalChartAdmits(p.ChartLine, semver.MustParse(portalFragmentChecksum))
}

// portalReadsFluxServiceAccount says whether the hosted portal may run a
// chart before portalPluginRemoval and so reads the agents' Flux identity
// from the fragment: its chart line's floor lies below the removal. The key
// is inert on a later chart, so a line that straddles the removal names it.
// checkRecord has refused a record whose line is missing or of another form
// where kagent runs, so an error here is a portal the fragment carries no
// agentPlatform section for anyway.
func (in *Input) portalReadsFluxServiceAccount() bool {
	p := in.hostedPortal()
	if p == nil {
		return false
	}
	floor, err := portalChartFloor(p.ChartLine)
	return err == nil && floor.LessThan(semver.MustParse(portalPluginRemoval))
}

// configMap renders a ConfigMap with one key.
func configMap(name, namespace, key string, value any) render.Map {
	return render.Map{e("apiVersion", "v1"), e("kind", "ConfigMap"),
		e("metadata", render.Map{e("name", name), e("namespace", namespace)}),
		e("data", render.Map{e(key, string(render.MustYAML(value)))})}
}

// chatCredentials is the chat's credentials Secret, in the values form the
// HelmRelease reads a Secret in: the Anthropic API key the person supplies
// at commit as the chart value the chart exposes as ANTHROPIC_API_KEY —
// base64-encoded, since the chart copies the value under its secrets
// Secret's data, which Kubernetes takes base64-encoded — or on Vertex the
// service account's JSON as the chart value the chart mounts at
// googleCredentialsPath.
func (in *Input) chatCredentials(secrets map[string]string) render.File {
	values := render.Map{e("anthropic", render.Map{e("apiKey", render.Base64Leaf(secrets[fieldAnthropicKey]))})}
	if in.aiChatVertex() {
		values = render.Map{e("google", render.Map{e("credentialsJson", secrets[fieldGoogleCredentials])})}
	}
	return render.Secret(portalCredentialsSecret, fluxNamespace, teamLabels, render.ValueKey("values", string(render.MustYAML(values))))
}

// valuesSource is one entry the Component's patch appends to the portal
// HelmRelease's values sources.
func valuesSource(kind, name string) render.Map {
	return render.Map{e("op", "add"), e("path", "/spec/valuesFrom/-"),
		e("value", render.Map{e("kind", kind), e("name", name), e("valuesKey", "values")})}
}

// portalFiles renders the platform's directory under the portal's
// extras/backstage/: the fragment's ConfigMap, the values that mount it,
// the chat's credentials Secret where the Component carries the key, and
// the Component listing them with the patch that appends the values sources
// to the portal's HelmRelease.
func (in *Input) portalFiles(r *render.Result, repo render.Repository, dir string, secrets map[string]string) {
	r.Add(repo, dir+"/app-config.yaml", yamlFile(configMap(portalAppConfigMap, backstageNamespace, portalAppConfigFile, in.portalAppConfig())))
	r.Add(repo, dir+"/values.yaml", yamlFile(configMap(portalValuesMap, fluxNamespace, "values", in.portalValues())))
	resources := []string{"app-config.yaml", "values.yaml"}
	ops := []render.Map{valuesSource("ConfigMap", portalValuesMap)}
	if in.chatKeyIsComponents() {
		r.Add(repo, dir+"/"+portalCredentialsFile, in.chatCredentials(secrets))
		resources = append(resources, portalCredentialsFile)
		ops = append(ops, valuesSource("Secret", portalCredentialsSecret))
	}
	component := render.Map{e("apiVersion", "kustomize.config.k8s.io/v1alpha1"), e("kind", "Component"),
		e("resources", resources),
		e("patches", []render.Map{{e("patch", string(render.MustYAML(ops))), e("target", render.Map{e("kind", "HelmRelease"), e("name", "backstage")})}})}
	r.Add(repo, dir+"/kustomization.yaml", yamlFile(component))
}
