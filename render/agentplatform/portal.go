package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// The developer portal's agent-platform section. The portal itself is the
// customer-portal definition's: its app-config, values and the sources of its
// HelmRelease are that definition's files. This definition contributes a
// directory of its own next to them, a kustomize Component the portal's
// extras/backstage/kustomization.yaml lists: the platform's Backstage
// configuration as an extra app-config file (the chart mounts it from a
// ConfigMap and passes it as --config), the chart values that mount it and
// name the installation's avatars host, and a patch that appends those values
// to the portal HelmRelease's sources. Appended last, the platform's values
// win: Helm replaces lists, so a portal that sets backstage.extraEnvVars
// itself loses that list to the platform's.

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
	// fluxServiceAccount is the tenant identity of the agents' HelmReleases,
	// created by the fleet's agent-platform base.
	fluxServiceAccount = "kagent-flux"
	// extensionsInclude is the shared base's list of the platform's portal extensions.
	extensionsInclude = "shared-config.yaml#extensionsAgentPlatform"
	// avatarsEnv is the CSP image source slot of the shared base's config.
	avatarsEnv = "BACKSTAGE_AVATARS_IMG_SRC"
)

// portalAuthProvider is the portal's sign-in provider on this installation's
// Dex, named as every installation-hosted portal names it.
func (in *Input) portalAuthProvider() string { return render.PortalAuthProvider(in.Installation.Name) }

// musterEntry is the installation's muster as the portal reaches it.
func (in *Input) musterEntry() render.Map {
	return render.Map{e("name", in.Installation.Name), e("url", "https://"+in.host("muster")+"/mcp"), e("authProvider", in.portalAuthProvider())}
}

// portalAppConfig is the platform's app-config fragment: the platform's
// extensions through the shared include, the agent-platform plugin's section
// where kagent runs, and the installation's muster.
func (in *Input) portalAppConfig() render.Map {
	m := render.Map{e("app", render.Map{e("extensions", render.Map{e("$include", extensionsInclude)})})}
	if in.kagent() {
		m = append(m, e("agentPlatform", render.Map{e("fluxServiceAccountName", fluxServiceAccount),
			e("kagent", render.Map{e("installations", render.Map{e(in.Installation.Name, render.Map{})})})}))
	}
	return append(m, e("muster", render.Map{e("installations", []render.Map{in.musterEntry()})}))
}

// portalValues are the platform's chart values: the fragment mounted as an
// extra app-config file and the installation's avatars host as the CSP image
// source.
func (in *Input) portalValues() render.Map {
	return render.Map{e("backstage", render.Map{
		e("extraAppConfig", []render.Map{{e("filename", portalAppConfigFile), e("configMapRef", portalAppConfigMap)}}),
		e("extraEnvVars", []render.Map{{e("name", avatarsEnv), e("value", "https://"+in.host("avatars"))}}),
	})}
}

// configMap renders a ConfigMap with one key.
func configMap(name, namespace, key string, value any) render.Map {
	return render.Map{e("apiVersion", "v1"), e("kind", "ConfigMap"),
		e("metadata", render.Map{e("name", name), e("namespace", namespace)}),
		e("data", render.Map{e(key, string(render.MustYAML(value)))})}
}

// portalFiles renders the platform's directory under the portal's extras/backstage/.
func (in *Input) portalFiles(r *render.Result, repo render.Repository, dir string) {
	r.Add(repo, dir+"/app-config.yaml", yamlFile(configMap(portalAppConfigMap, backstageNamespace, portalAppConfigFile, in.portalAppConfig())))
	r.Add(repo, dir+"/values.yaml", yamlFile(configMap(portalValuesMap, fluxNamespace, "values", in.portalValues())))
	ops := []render.Map{{e("op", "add"), e("path", "/spec/valuesFrom/-"),
		e("value", render.Map{e("kind", "ConfigMap"), e("name", portalValuesMap), e("valuesKey", "values")})}}
	component := render.Map{e("apiVersion", "kustomize.config.k8s.io/v1alpha1"), e("kind", "Component"),
		e("resources", []string{"app-config.yaml", "values.yaml"}),
		e("patches", []render.Map{{e("patch", string(render.MustYAML(ops))), e("target", render.Map{e("kind", "HelmRelease"), e("name", "backstage")})}})}
	r.Add(repo, dir+"/kustomization.yaml", yamlFile(component))
}
