package customerportal

import "github.com/giantswarm/giantswarm-platform-manager/render"

// The features of definitions/customer-portal/features.yaml a probe belongs to.
const (
	featurePortal          = "portal"
	featureIdentity        = "identity"
	featureSecrets         = "secrets"
	featurePlatformSection = "agent-platform-section"
)

// podSelector is the label the backstage chart puts on the portal's pods:
// app.kubernetes.io/instance carries the chart's name on every pod of the
// portal's Deployment. TestRenderConsumption holds it to the rendered chart.
const podSelector = "app.kubernetes.io/instance=backstage"

// probes are the checks of the running portal, one or more per live dimension
// of features.yaml, in the order the verify slice runs them: the release, the
// pods, the home page, the sign-in chain over HTTP, Dex holding the client's
// secret, the Secrets and, with the platform enabled, the platform's
// fragment. Nothing here runs anything: a probe is data.
func (in *Input) probes() []render.Probe {
	start := in.portalURL() + "/api/auth/" + in.authProvider() + "/start?env=production"
	dexAuth := "https://" + in.host("dex") + "/auth"
	p := []render.Probe{
		resourceProbe("live-helmrelease-ready", featurePortal, render.HelmReleaseReady, fluxNamespace, "HelmRelease", releaseName),
		resourceProbe("live-pods-running", featurePortal, render.PodsRunning, backstageNamespace, "Pod", podSelector),
		httpProbe("live-portal-root", featurePortal, in.portalURL()+"/", render.Expectation{Status: 200}),
		httpProbe("live-oidc-start", featureIdentity, start, render.Expectation{Status: 302, LocationContains: dexAuth,
			Note: "the sign-in redirects to the installation's Dex"}),
		httpProbe("live-oidc-start", featureIdentity, start, render.Expectation{Status: 302, LocationContains: "client_id=" + render.PortalDexClientID,
			Note: "the sign-in redirects as the portal's client"}),
		render.DexAuthProbe("live-dex-auth-portal-client", featureIdentity, in.host("dex"), render.PortalDexClientID, render.PortalRedirectURI(in.Portal.Domain, in.Installation.Name)),
		render.DexSecretLoadedProbe("live-dex-portal-secret-loaded", featureIdentity, dexNamespace, render.DexClientSecretName(render.PortalDexClientID), render.PortalDexClientID),
	}
	values := []string{userSecretsName, pluginKeysName}
	if in.Plugins.GitHub.Enabled {
		values = append(values, githubAppSecretName)
	}
	for _, name := range values {
		p = append(p, secretProbe("live-values-secrets", fluxNamespace, name, "values"))
	}
	p = append(p, secretProbe("live-dex-client-secret", dexNamespace, render.DexClientSecretName(render.PortalDexClientID), render.DexSecretKey))
	if in.Installation.AgentPlatform {
		p = append(p, resourceProbe("live-platform-fragment", featurePlatformSection, render.ResourcePresent, backstageNamespace, "ConfigMap", platformAppConfigMap))
	}
	return p
}

// resourceProbe is a probe of one object, by kind, namespace, resource and name.
func resourceProbe(id, feature string, kind render.ProbeKind, namespace, resource, name string) render.Probe {
	return render.Probe{ID: id, Feature: feature, Kind: kind, Namespace: namespace, Resource: resource, Name: name}
}

// secretProbe is a Secret that exists and carries key.
func secretProbe(id, namespace, name, key string) render.Probe {
	p := resourceProbe(id, featureSecrets, render.ResourcePresent, namespace, "Secret", name)
	p.Expect.Keys = []string{key}
	return p
}

// httpProbe is a GET of url answered as expect says.
func httpProbe(id, feature, url string, expect render.Expectation) render.Probe {
	return render.Probe{ID: id, Feature: feature, Kind: render.HTTP, URL: url, Expect: expect}
}
