package agentplatform

import (
	"crypto/sha256"
	"fmt"
	"slices"
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
// the musters of the installations the portal shows: the shared list with the
// platform's section, with the AI chat where the chat is on, and with the
// Grafana dashboards card where the portal's Grafana plugin is wired
// (installation.portals[*].grafanaWired, read from the record: the proxy
// endpoint the customer-portal definition renders for it), since the
// fragment's list is the one Backstage keeps. Such a portal shows every
// installation of its organisation that runs the platform, and its fragment
// is one file whichever installation's plan renders it: the kagent
// installations and the muster entries are the union of this installation
// and the ones the record lists for the portal
// (installation.portals[*].installations, read from the portal's
// gs.installations and the installations' markers), by name, so a plan never
// drops what another wrote. A hand-kept portal — one whose
// app-config on record carries a literal extension list of its own, the
// hub's Dev Portal among them (installation.portals[*].handKept, read from
// the record) — owns its extensions and its muster registry; there the
// Component writes only object-shaped keys (this installation's kagent
// entry, the fragment's mount, the chat's blocks), which merge, and the skill
// repositories, a list it reads back from the portal's own app-config, so the
// list it sets is the portal's own. The day the customer-portal definition
// renders such a portal the fact reads false and the Component takes the
// lists over; until the fragment carries them, that definition keeps the
// platform's section in the portal's app-config (its platformSection), so
// the portal runs with the section at every step. The portal's environment,
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
// enable of that installation alone with it supplied; until the Secret is on
// record the customer-portal definition keeps the credential in the portal's
// user secrets, supplied under the same field, so the chat keeps it through
// every step of the move.
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
	// portalTraces is the first portal chart that exports the backend's
	// traces from observability.otel; an earlier chart's schema refuses the
	// key.
	portalTraces = "2.68.0"
	// portalMetrics is the first portal chart that serves the backend's
	// Prometheus metrics from observability.metrics and renders a
	// ServiceMonitor; an earlier chart's schema refuses both keys.
	portalMetrics = "2.73.1"
	// portalTenant is the Mimir tenant of the portal's traces and metrics.
	portalTenant = "giantswarm"
	// portalOTLPEndpoint is the installation's OTLP gateway, which takes the
	// tenant from portalOTLPHeaders.
	portalOTLPEndpoint = "http://otlp-gateway.kube-system.svc.cluster.local:4317"
	portalOTLPHeaders  = "X-Scope-OrgID=" + portalTenant
)

// The actions service lists the actions of these plugins for the chat's
// actions server, every one but the hub's PagerDuty lookup.
var (
	chatActionSources  = []string{"auth", "catalog", "gs"}
	chatActionExcludes = []string{"gs:get-pagerduty-ids-for-entity"}
)

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

// musterEntry is an installation's muster as the portal reaches it, under
// the given name: the installation's in the muster plugin's list, "muster"
// in the chat's server list. Its authProvider is the portal's sign-in
// provider on that installation's Dex, oidc-<installation>, as every portal
// on record names it: the muster plugin and the chat send that provider's ID
// token on the portal's own installation and the token the cluster token
// broker mints from that Dex elsewhere, and muster trusts the portal's Dex
// client as an audience. The portal builds a dedicated OAuth provider only
// for an auth.providers key with the mcp- prefix and the platform declares
// none, so an mcp-* name here would promise a login that is not there and
// switch the picker to a token muster rejects the day someone declares one.
func musterEntry(name string, installation PortalInstallation) render.Map {
	return render.Map{e("name", name), e("url", "https://muster."+installation.BaseDomain+"/mcp"), e("authProvider", render.PortalAuthProvider(installation.Name))}
}

// own is this installation as a portal lists it.
func (in *Input) own() PortalInstallation {
	return PortalInstallation{Name: in.Installation.Name, BaseDomain: in.Installation.BaseDomain}
}

// portalInstallations are the installations the platform's portal section
// lists: this installation and, on a portal the customer-portal definition
// renders, every installation the record lists for it as running the
// platform (installation.portals[*].installations), by name, each once —
// the same list whichever installation's plan renders the section, so a
// plan never drops what another wrote. A hand-kept portal carries its own
// section for the installations it proxies: there the list is this
// installation alone.
func (in *Input) portalInstallations() []PortalInstallation {
	list := []PortalInstallation{in.own()}
	if p := in.hostedPortal(); p != nil && !p.HandKept {
		for _, i := range p.Installations {
			if i.Name != in.Installation.Name {
				list = append(list, i)
			}
		}
	}
	slices.SortFunc(list, func(a, b PortalInstallation) int { return strings.Compare(a.Name, b.Name) })
	return list
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
	return append(m, e("model", in.AIChat.Model), e("mcp", []render.Map{actions, musterEntry(chatMusterServer, in.chatMuster())}))
}

// chatMuster is the muster the chat's server list names: the portal host's,
// which federates the servers of every installation the portal shows,
// where the host runs the platform (this installation, or among the
// portal's installations on record); else this installation's. So the
// entry is the same whichever installation's plan renders the fragment, as
// long as the host runs the platform.
func (in *Input) chatMuster() PortalInstallation {
	host := in.portalHost()
	for _, i := range in.portalInstallations() {
		if i.Name == host {
			return i
		}
	}
	return in.own()
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
// plugin's section (where kagent runs, the agents' Flux identity where the
// portal's plugin reads it and the portal's installations
// (portalInstallations) among the kagent installations; the skill
// repositories the agent creation discovers skills in, where there are
// any); where the Component owns the portal's lists, the platform's
// extensions through the shared include (with the chat's entries where the
// chat is on, with the Hive's pages where it is on, with the Grafana
// dashboards card where the portal's plugin is wired) and the portal's
// installations' musters; and, with the chat on, the aiChat block, the
// actions server's tool naming and the actions the service lists for it;
// where the portal reads GitHub through this installation's muster, the gs
// block for it (github.go); with the Hive on, its plans and roadmap blocks
// (hive.go), and its pages in the shared list.
func (in *Input) portalAppConfig() render.Map {
	m := render.Map{}
	if in.portalOwnsLists() {
		m = append(m, e("app", render.Map{e("extensions", render.Map{e("$include", render.PortalExtensionsInclude(true, in.aiChat(), in.hive(), in.hostedPortal().GrafanaWired))})}))
	}
	installations := in.portalInstallations()
	platform := render.Map{}
	if in.kagent() {
		if in.portalReadsFluxServiceAccount() {
			platform = append(platform, e("fluxServiceAccountName", fluxServiceAccount))
		}
		kagent := render.Map{}
		for _, i := range installations {
			kagent = append(kagent, e(i.Name, render.Map{}))
		}
		platform = append(platform, e("kagent", render.Map{e("installations", kagent)}))
	}
	if len(in.SkillRepositories) > 0 {
		platform = append(platform, e("skills", render.Map{e("repositories", in.SkillRepositories)}))
	}
	if len(platform) > 0 {
		m = append(m, e("agentPlatform", platform))
	}
	if in.portalOwnsLists() {
		musters := make([]render.Map, 0, len(installations))
		for _, i := range installations {
			musters = append(musters, musterEntry(i.Name, i))
		}
		m = append(m, e("muster", render.Map{e("installations", musters)}))
	}
	if in.portalGitHub() {
		m = append(m, e("gs", in.portalGitHubConfig()))
	}
	if in.hive() {
		m = append(m, in.hiveSection()...)
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
// alone changes nothing the HelmRelease sees), the OTLP export of the
// backend's traces and its scraped metrics where the chart takes them, and,
// for a chat on Vertex, the Google project and location the chart exports to
// the pod. No list: the portal's environment is the customer-portal
// definition's.
func (in *Input) portalValues() render.Map {
	fragment := render.Map{e("filename", portalAppConfigFile), e("configMapRef", portalAppConfigMap)}
	if in.portalRollsOnFragment() {
		fragment = append(fragment, e("checksum", fmt.Sprintf("%x", sha256.Sum256(render.MustYAML(in.portalAppConfig())))))
	}
	m := render.Map{e("backstage", render.Map{e("extraAppConfig", []render.Map{fragment})})}
	if in.portalExportsTraces() {
		observability := render.Map{e("otel", render.Map{
			e("endpoint", portalOTLPEndpoint), e("protocol", "grpc"), e("headers", portalOTLPHeaders)})}
		if in.portalServesMetrics() {
			observability = append(observability, e("metrics", render.Map{e("enabled", true)}))
		}
		m = append(m, e("observability", observability))
	}
	if in.portalServesMetrics() {
		m = append(m, e("serviceMonitor", render.Map{e("enabled", true),
			e("labels", render.Map{e("observability.giantswarm.io/tenant", portalTenant)})}))
	}
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

// portalExportsTraces says whether the hosted portal's chart takes
// observability.otel: its chart line resolves to portalTraces or later. A
// portal whose line is not on record exports nothing, which an earlier chart
// would refuse.
func (in *Input) portalExportsTraces() bool {
	p := in.hostedPortal()
	return p != nil && portalChartAdmits(p.ChartLine, semver.MustParse(portalTraces))
}

// portalServesMetrics says whether the hosted portal's chart takes
// observability.metrics and serviceMonitor: its chart line resolves to
// portalMetrics or later.
func (in *Input) portalServesMetrics() bool {
	p := in.hostedPortal()
	return p != nil && portalChartAdmits(p.ChartLine, semver.MustParse(portalMetrics))
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
// the chat's credentials Secret where the Component carries the key, the
// broker client's Secret where the portal reads GitHub through this
// installation's muster (github.go), and
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
	if in.portalGitHub() {
		r.Add(repo, dir+"/"+portalBrokerFile, in.portalBrokerCredentials())
		resources = append(resources, portalBrokerFile)
		ops = append(ops, valuesSource("Secret", portalBrokerSecret))
	}
	component := render.Map{e("apiVersion", "kustomize.config.k8s.io/v1alpha1"), e("kind", "Component"),
		e("resources", resources),
		e("patches", []render.Map{{e("patch", string(render.MustYAML(ops))), e("target", render.Map{e("kind", "HelmRelease"), e("name", "backstage")})}})}
	r.Add(repo, dir+"/kustomization.yaml", yamlFile(component))
}
