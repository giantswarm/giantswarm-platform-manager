package agentplatform

import (
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
// the installation's muster. A hand-kept portal — one whose app-config on
// record carries a literal extension list of its own, the hub's Dev Portal
// among them (installation.portals[*].handKept, read from the record) — owns
// its extensions and its muster registry; there the Component writes only
// object-shaped keys (the kagent installation, the fragment's mount), which
// merge. The day the customer-portal definition renders such a portal the
// fact reads false and the Component takes the lists over. The portal's
// environment, backstage.extraEnvVars, is the portal's own whatever the
// portal: the customer-portal definition's user-values carry the whole list
// (the avatars image source, the tunnel's CA variable) and the Component sets
// none, so no third source contends for it.
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
	// fluxServiceAccount is the tenant identity of the agents' HelmReleases,
	// created by the fleet's agent-platform base in the kagent namespace.
	fluxServiceAccount = "kagent-flux"
	// portalPluginRemoval is the first portal chart whose agent-platform
	// plugin creates agents through agent-manager and reads no
	// fluxServiceAccountName.
	portalPluginRemoval = "1.1.0"
	// extensionsInclude is the shared base's list of the platform's portal extensions.
	extensionsInclude = "shared-config.yaml#extensionsAgentPlatform"
)

// portalAuthProvider is the portal's sign-in provider on this installation's
// Dex, named as every installation-hosted portal names it.
func (in *Input) portalAuthProvider() string { return render.PortalAuthProvider(in.Installation.Name) }

// portalOwnsLists says whether the Component is the portal's sole source of its
// list-shaped keys (app.extensions, muster.installations) and so sets them. A
// hand-kept portal carries its own; a list the Component set there would
// replace it.
func (in *Input) portalOwnsLists() bool {
	p := in.hostedPortal()
	return p != nil && !p.HandKept
}

// musterEntry is the installation's muster as the portal reaches it. Its
// authProvider is the portal's sign-in provider on this installation's Dex,
// oidc-<installation>: the muster plugin sends that provider's ID token on
// the home installation and the token the cluster token broker mints from
// that Dex elsewhere, and muster trusts the portal's Dex client as an
// audience. The portal builds a dedicated OAuth provider only for an
// auth.providers key with the mcp- prefix and the platform declares none, so
// an mcp-* name here would promise a login that is not there and switch the
// picker to a token muster rejects the day someone declares one.
func (in *Input) musterEntry() render.Map {
	return render.Map{e("name", in.Installation.Name), e("url", "https://"+in.host("muster")+"/mcp"), e("authProvider", in.portalAuthProvider())}
}

// portalAppConfig is the platform's app-config fragment: the agent-platform
// plugin's section where kagent runs (the agents' Flux identity where the
// portal's plugin reads it, and the installation among the kagent
// installations) and, where the Component owns the portal's lists, the
// platform's extensions through the shared include and the installation's
// muster.
func (in *Input) portalAppConfig() render.Map {
	m := render.Map{}
	if in.portalOwnsLists() {
		m = append(m, e("app", render.Map{e("extensions", render.Map{e("$include", extensionsInclude)})}))
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
		m = append(m, e("muster", render.Map{e("installations", []render.Map{in.musterEntry()})}))
	}
	return m
}

// portalValues are the platform's chart values: the fragment mounted as an
// extra app-config file. No list: the portal's environment is the
// customer-portal definition's.
func (in *Input) portalValues() render.Map {
	return render.Map{e("backstage", render.Map{e("extraAppConfig", []render.Map{{e("filename", portalAppConfigFile), e("configMapRef", portalAppConfigMap)}})})}
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
