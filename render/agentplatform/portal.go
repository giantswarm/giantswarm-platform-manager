package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// The developer portal's agent-platform section. The portal itself is the
// customer-portal definition's: its app-config, values and the sources of its
// HelmRelease are that definition's files. This definition contributes a
// directory of its own next to them, a kustomize Component the portal's
// extras/backstage/kustomization.yaml lists: the platform's Backstage
// configuration as an extra app-config file (the chart mounts it from a
// ConfigMap and passes it as --config), the chart values that mount it and
// name the installation's avatars host, the Google credentials of a Vertex
// chat, and a patch that appends those values to the portal HelmRelease's
// sources. Appended last, the platform's values win: Helm replaces lists, so
// a portal that sets backstage.extraEnvVars itself loses that list to the
// platform's.

const (
	// backstageNamespace is the portal's release namespace, where the chart
	// mounts ConfigMaps from.
	backstageNamespace = "backstage"
	// portalDir is the platform's directory under the portal's extras/backstage/.
	portalDir = "agent-platform"
	// portalAppConfigMap carries the platform's app-config fragment.
	portalAppConfigMap = "agent-platform-app-config-backstage"
	// portalAppConfigFile is the fragment's file name in the portal's container.
	portalAppConfigFile = "app-config.agent-platform.yaml"
	// portalValuesMap carries the platform's chart values.
	portalValuesMap = "agent-platform-values-backstage"
	// portalGoogleSecret carries the Vertex chat's Google credentials as chart values.
	portalGoogleSecret = "agent-platform-google-credentials-backstage" // #nosec G101 -- a Secret name, not a value
	portalGoogleFile   = "google-credentials.enc.yaml"                 // #nosec G101 -- a file name, not a value
	// googleCredentialsPath is where the chart mounts google.credentialsJson.
	googleCredentialsPath = "/app/google/credentials.json" // #nosec G101 -- a mount path, not a value
	// fieldGoogleCredentials is the supplied field carrying the Vertex service
	// account's credentials JSON.
	fieldGoogleCredentials = "portal.aiChat.google.credentialsJson" // #nosec G101 -- a field name, not a value
	// fieldSkillsToken is the optional supplied field carrying the token that
	// checks out a private skills repository.
	fieldSkillsToken = "portal.skillsToken" // #nosec G101 -- a field name, not a value
	// skillsTokenSecret is the Secret kagent's skills checkout reads the token from.
	skillsTokenSecret = "kagent-skills-token"              // #nosec G101 -- a Secret name, not a value
	skillsTokenFile   = "kagent-private-skills-token.yaml" // #nosec G101 -- a file name, not a value
	// fluxServiceAccount is the tenant identity of the agents' HelmReleases,
	// created by the fleet's agent-platform base.
	fluxServiceAccount = "kagent-flux"
	// extensionsInclude is the shared base's list of the platform's portal extensions.
	extensionsInclude = "shared-config.yaml#extensionsAgentPlatform"
	// avatarsEnv is the CSP image source slot of the shared base's config.
	avatarsEnv = "BACKSTAGE_AVATARS_IMG_SRC"
	// providerVertex is the aiChat provider that runs the model on Vertex AI.
	providerVertex = "vertex"
	// anthropicKeyEnv is the environment variable the chart sets from the
	// portal's own Anthropic key; $$ survives the fleet's variable substitution
	// so Backstage sees ${ANTHROPIC_API_KEY}.
	anthropicKeyEnv = "$${ANTHROPIC_API_KEY}"
)

// portalHost is the installation whose management-clusters tree hosts the portal.
func (in *Input) portalHost() string {
	if in.Portal.Installation != "" {
		return in.Portal.Installation
	}
	return in.Installation.Name
}

// portalAuthProvider is the portal's sign-in provider on this installation's
// Dex, named as every installation-hosted portal names it.
func (in *Input) portalAuthProvider() string { return "oidc-" + in.Installation.Name }

func (in *Input) aiChatEnabled() bool {
	return in.Portal.Enabled && in.Portal.AIChat != nil && in.Portal.AIChat.Enabled
}

func (in *Input) aiChatVertex() bool {
	return in.aiChatEnabled() && in.Portal.AIChat.Provider == providerVertex
}

// portalSecretFields are the supplied secret values the portal section needs:
// the Google credentials of a Vertex chat.
func (in *Input) portalSecretFields() []string {
	if in.aiChatVertex() {
		return []string{fieldGoogleCredentials}
	}
	return nil
}

// optionalSecretFields are the supplied secret values the person may give:
// the token of a private skills repository, rendered only when given.
func (in *Input) optionalSecretFields() []string {
	if in.Portal.Enabled && len(in.Portal.SkillsRepositories) > 0 {
		return []string{fieldSkillsToken}
	}
	return nil
}

// musterEntry is the installation's muster as the portal reaches it, under
// the given name: the installation's in the muster plugin's list, "muster" in
// the chat's server list.
func (in *Input) musterEntry(name string) render.Map {
	return render.Map{e("name", name), e("url", "https://"+in.host("muster")+"/mcp"), e("authProvider", in.portalAuthProvider())}
}

// portalAppConfig is the platform's app-config fragment: the platform's
// extensions through the shared include, the agent-platform plugin's section,
// the installation's muster, and the AI chat on the platform's model provider.
func (in *Input) portalAppConfig() render.Map {
	m := render.Map{e("app", render.Map{e("extensions", render.Map{e("$include", extensionsInclude)})})}
	ap := render.Map{}
	if in.Kagent.Enabled {
		ap = append(ap, e("fluxServiceAccountName", fluxServiceAccount),
			e("kagent", render.Map{e("installations", render.Map{e(in.Installation.Name, render.Map{})})}))
	}
	if len(in.Portal.SkillsRepositories) > 0 {
		ap = append(ap, e("skills", render.Map{e("repositories", in.Portal.SkillsRepositories)}))
	}
	if len(ap) > 0 {
		m = append(m, e("agentPlatform", ap))
	}
	m = append(m, e("muster", render.Map{e("installations", []render.Map{in.musterEntry(in.Installation.Name)})}))
	if in.aiChatEnabled() {
		c := in.Portal.AIChat
		chat := render.Map{}
		if c.Model != "" {
			chat = append(chat, e("model", c.Model))
		}
		if c.Provider == providerVertex {
			chat = append(chat, e("anthropic", render.Map{e("provider", providerVertex)}),
				e("google", render.Map{e("project", c.Google.Project), e("location", c.Google.Location), e("keyFilename", googleCredentialsPath)}))
		} else {
			chat = append(chat, e("anthropic", render.Map{e("apiKey", anthropicKeyEnv)}))
		}
		chat = append(chat, e("mcp", []render.Map{in.musterEntry("muster")}))
		m = append(m, e("aiChat", chat), e("mcpActions", render.Map{e("namespacedToolNames", false)}))
	}
	return m
}

// portalValues are the platform's chart values: the fragment mounted as an
// extra app-config file, the installation's avatars host as the CSP image
// source, and the Vertex project and location.
func (in *Input) portalValues() render.Map {
	m := render.Map{e("backstage", render.Map{
		e("extraAppConfig", []render.Map{{e("filename", portalAppConfigFile), e("configMapRef", portalAppConfigMap)}}),
		e("extraEnvVars", []render.Map{{e("name", avatarsEnv), e("value", "https://"+in.host("avatars"))}}),
	})}
	if in.aiChatVertex() {
		g := in.Portal.AIChat.Google
		m = append(m, e("google", render.Map{e("project", g.Project), e("location", g.Location)}))
	}
	return m
}

// configMap renders a ConfigMap with one key.
func configMap(name, namespace, key string, value any) render.Map {
	return render.Map{e("apiVersion", "v1"), e("kind", "ConfigMap"),
		e("metadata", render.Map{e("name", name), e("namespace", namespace)}),
		e("data", render.Map{e(key, string(render.MustYAML(value)))})}
}

// portalFiles renders the platform's directory under the portal's extras/backstage/.
func (in *Input) portalFiles(r *render.Result, repo render.Repository, dir string, secrets map[string]string) {
	r.Add(repo, dir+"/app-config.yaml", yamlFile(configMap(portalAppConfigMap, backstageNamespace, portalAppConfigFile, in.portalAppConfig())))
	r.Add(repo, dir+"/values.yaml", yamlFile(configMap(portalValuesMap, fluxNamespace, "values", in.portalValues())))
	resources := []string{"app-config.yaml", "values.yaml"}
	sources := []render.Map{{e("kind", "ConfigMap"), e("name", portalValuesMap), e("valuesKey", "values")}}
	if in.aiChatVertex() {
		resources = append(resources, portalGoogleFile)
		credentials := render.Map{e("google", render.Map{e("credentialsJson", secrets[fieldGoogleCredentials])})}
		r.Add(repo, dir+"/"+portalGoogleFile, render.Secret(portalGoogleSecret, fluxNamespace, teamLabels,
			render.ValueKey("values", string(render.MustYAML(credentials)))))
		sources = append(sources, render.Map{e("kind", "Secret"), e("name", portalGoogleSecret), e("valuesKey", "values")})
	}
	var ops []render.Map
	for _, s := range sources {
		ops = append(ops, render.Map{e("op", "add"), e("path", "/spec/valuesFrom/-"), e("value", s)})
	}
	component := render.Map{e("apiVersion", "kustomize.config.k8s.io/v1alpha1"), e("kind", "Component"),
		e("resources", resources),
		e("patches", []render.Map{{e("patch", string(render.MustYAML(ops))), e("target", render.Map{e("kind", "HelmRelease"), e("name", "backstage")})}})}
	r.Add(repo, dir+"/kustomization.yaml", yamlFile(component))
}
