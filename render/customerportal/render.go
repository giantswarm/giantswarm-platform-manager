// Package customerportal is the customer-portal capability definition over the
// render library: an installation's inputs (definitions/customer-portal/schema.json)
// in, the files of the portal in its management-clusters repository out, with
// the probes of the running portal as data.
//
// It renders an installation's own developer portal: the extras/backstage tree
// over the fleet's backstage bases — the portal's app-config, the chart values
// with the portal's environment, the Secrets the chart reads its credentials
// from, the plugin signing keys, the tunnel's SPIFFE bundle reference — and
// the portal's Dex client. The agent-platform section of the portal is the
// agent-platform definition's: a kustomize Component this definition lists
// when installation.agentPlatform says the platform is enabled, never files
// of its own; the portal's environment (backstage.extraEnvVars) stays this
// definition's whole, the platform's avatars host included.
//
// The installation's dex-app configmap patch is one file with one owner. On
// an installation without the platform this definition renders it with the
// portal's client; with the platform enabled the agent-platform definition
// owns the file and carries the same entry (its portal.domain input), built by
// render.PortalDexClient for both.
package customerportal

import (
	"strconv"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	// fluxNamespace is where the portal's HelmRelease and its values sources live.
	fluxNamespace = "flux-giantswarm"
	// backstageNamespace is the portal's release namespace.
	backstageNamespace = "backstage"
	// dexNamespace is where the installation's Dex runs and reads client Secrets.
	dexNamespace = "giantswarm"
	// basesRepository is the fleet base the extras reference.
	basesRepository = "https://github.com/giantswarm/management-cluster-bases/extras/backstage/"
	// releaseName is the portal's HelmRelease and OCIRepository.
	releaseName = "backstage"
	// portalDir is the portal's directory under extras/.
	portalDir = "backstage"
	// The ConfigMaps and Secrets the HelmRelease takes its values from, in
	// the fleet's order; shared-config is the base's.
	appConfigMap        = "app-config-backstage"
	sharedConfigMap     = "shared-config-backstage"
	userValuesMap       = "user-values-backstage"
	userSecretsName     = "user-secrets-backstage"           // #nosec G101 -- a Secret name, not a value
	githubAppSecretName = "github-app-credentials-backstage" // #nosec G101 -- a Secret name, not a value
	pluginKeysName      = "plugin-keys-backstage"            // #nosec G101 -- a Secret name, not a value
	// The files of the portal's directory.
	appConfigFile   = "app-config.yaml"
	userValuesFile  = "user-values.yaml"
	userSecretsFile = "user-secrets.enc.yaml"           // #nosec G101 -- a file name, not a value
	githubAppFile   = "github-app-credentials.enc.yaml" // #nosec G101 -- a file name, not a value
	pluginKeysFile  = "plugin-keys-secret.enc.yaml"     // #nosec G101 -- a file name, not a value
	tunnelFile      = "tunnelport-spiffe-bundle.yaml"
	// tunnelBundleName is the Secret the tunnel bundle lands in and the
	// volume the chart mounts it from; tunnelBundleMount is where the
	// portal's Node runtime reads it.
	tunnelBundleName  = "tunnelport-spiffe-bundle"
	tunnelBundleMount = "/app/tunnelport-spiffe-bundle"
	// tunnelBundleFile is the bundle in the mount, the public half External
	// Secrets copies; tunnelCAEnv is the one variable Node reads extra CA
	// certificates through, so without it the mount is inert.
	tunnelBundleFile = "svid_bundle.pem"
	tunnelCAEnv      = "NODE_EXTRA_CA_CERTS"
	// avatarsEnv is the CSP image source slot of the shared base's config,
	// set to the installation's avatars host where the platform runs.
	avatarsEnv = "BACKSTAGE_AVATARS_IMG_SRC"
	// dexClientFile is the portal's Dex client Secret; the name matches the
	// fleet's .sops.yaml rules (.*(secret|credential).*) and the directory's
	// .enc.yaml convention.
	dexClientFile = "dex-client-backstage-secret.enc.yaml" // #nosec G101 -- a file name, not a value
	// The generated values, named so the Dex client Secret and the portal's
	// own Secret receive the same client secret.
	generatedSessionSecret   = "backstage-session-secret"    // #nosec G101 -- a placeholder name, not a value
	generatedDexClientSecret = "backstage-dex-client-secret" // #nosec G101 -- a placeholder name, not a value; prefixed with the installation: never shared between installations
	generatedTelemetrySalt   = "backstage-telemetrydeck-salt"
	// generatedPluginKeys is the plugin-to-plugin signing key pair, one
	// name for both halves.
	generatedPluginKeys = "backstage-plugin-keys"
	// platformAppConfigMap is the platform's app-config fragment the
	// agent-platform Component mounts into the portal.
	platformAppConfigMap = "agent-platform-app-config-backstage"
	// fileHeader opens every rendered YAML file.
	fileHeader = "# Rendered by giantswarm-platform-manager, customer-portal definition. Do not edit by hand:\n# the next reconcile writes it again from the installation's inputs.\n"
)

// Render turns an installation's inputs into its fileset. raw is the decoded
// input document (map[string]any at the top, as a YAML or JSON decoder returns
// it); secrets carries the values the person supplies, by field name — the
// GitHub App's credentials, the Sentry DSNs. Everything else the portal needs
// is a placeholder the commit step generates. mode says what the render is
// for: a commit refuses a required person input the document lacks, a
// comparison renders its Missing marker.
func Render(raw any, secrets map[string]string, mode render.Mode) (*render.Result, error) {
	in, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := in.check(secrets, mode); err != nil {
		return nil, err
	}

	name := in.Installation.Name
	clusters := render.Repository("giantswarm/" + in.Installation.Customer + "-management-clusters")
	extras := "management-clusters/" + name + "/extras/"
	dir := extras + portalDir + "/"

	r := &render.Result{}
	r.Add(clusters, dir+"kustomization.yaml", in.extrasKustomization())
	in.portalFiles(r, clusters, dir+portalDir+"/", secrets)
	r.Include(clusters, extras+"kustomization.yaml", "./"+portalDir+"/")
	if !in.Installation.AgentPlatform {
		configs := render.Repository("giantswarm/" + in.Installation.Customer + "-configs")
		r.Add(configs, "installations/"+name+"/apps/dex-app/configmap-values.yaml.patch", yamlFile(in.dexPatch()))
	}
	r.Probes = in.probes()
	return r, nil
}

func yamlFile(v any) render.File {
	return render.File{Content: append([]byte(fileHeader), render.MustYAML(v)...)}
}

func e(key string, value any) render.Entry { return render.Entry{Key: key, Value: value} }

// host is a hostname on the installation's base domain.
func (in *Input) host(component string) string {
	return hostOn(component, in.Installation.BaseDomain)
}

// hostOn is a hostname on an installation's base domain.
func hostOn(component, baseDomain string) string { return component + "." + baseDomain }

// portalURL is the portal's origin.
func (in *Input) portalURL() string { return "https://" + in.Portal.Domain }

// authProvider is the portal's sign-in provider: on the Dex of the
// installation that signs people in.
func (in *Input) authProvider() string { return render.PortalAuthProvider(in.signInInstallation()) }

// extrasKustomization is extras/backstage/kustomization.yaml: the fleet's
// backstage base (the namespace), the portal's directory and, with the
// platform enabled, the agent-platform definition's Component. As the fleet
// writes its portal kustomizations, without apiVersion and kind.
func (in *Input) extrasKustomization() render.File {
	k := render.Map{e("resources", []string{basesRepository + "base?ref=main", "./" + portalDir + "/"})}
	if in.Installation.AgentPlatform {
		k = append(k, e("components", []string{render.PortalPlatformComponent()}))
	}
	return yamlFile(k)
}

// valuesSource is one entry of the HelmRelease's spec.valuesFrom.
func valuesSource(kind, name string) render.Map {
	return render.Map{e("kind", kind), e("name", name), e("valuesKey", "values")}
}

// portalFiles renders extras/backstage/backstage/: the kustomization over the
// fleet's main base with the release range and the values sources patched in,
// the ConfigMaps, the Secrets and the tunnel bundle.
func (in *Input) portalFiles(r *render.Result, repo render.Repository, dir string, secrets map[string]string) {
	r.Add(repo, dir+appConfigFile, yamlFile(configMap(appConfigMap, "values", render.Map{e("backstage", render.Map{e("appConfig", string(render.MustYAML(in.appConfig())))})})))
	r.Add(repo, dir+userValuesFile, yamlFile(configMap(userValuesMap, "values", in.userValues())))
	r.Add(repo, dir+userSecretsFile, in.userSecrets(secrets))
	resources := []string{basesRepository + "main?ref=main", appConfigFile, userValuesFile, userSecretsFile}
	sources := []render.Map{valuesSource("ConfigMap", appConfigMap), valuesSource("ConfigMap", sharedConfigMap),
		valuesSource("ConfigMap", userValuesMap), valuesSource("Secret", userSecretsName)}
	if in.Plugins.GitHub.Enabled {
		r.Add(repo, dir+githubAppFile, in.githubAppCredentials(secrets))
		resources = append(resources, githubAppFile)
		sources = append(sources, valuesSource("Secret", githubAppSecretName))
	}
	r.Add(repo, dir+pluginKeysFile, in.pluginKeys())
	resources = append(resources, pluginKeysFile)
	sources = append(sources, valuesSource("Secret", pluginKeysName))
	if in.Tunnel.Enabled {
		r.Add(repo, dir+tunnelFile, render.File{Content: []byte(fileHeader + tunnelBundle)})
		resources = append(resources, tunnelFile)
	}
	r.Add(repo, dir+dexClientFile, render.Secret(render.DexClientSecretName(render.PortalDexClientID), dexNamespace, nil,
		render.GeneratedKey(render.DexSecretKey, in.Installation.Name+"-"+generatedDexClientSecret, render.Base64, 32)))
	resources = append(resources, dexClientFile)

	ociPatch := []render.Map{{e("op", "remove"), e("path", "/spec/ref/tag")}, {e("op", "add"), e("path", "/spec/ref/semver"), e("value", in.Chart.Line)}}
	releasePatch := render.Map{e("apiVersion", "helm.toolkit.fluxcd.io/v2"), e("kind", "HelmRelease"),
		e("metadata", render.Map{e("name", releaseName), e("namespace", fluxNamespace)}),
		e("spec", render.Map{e("valuesFrom", sources)})}
	k := render.Map{e("resources", resources),
		e("patches", []render.Map{
			{e("patch", string(render.MustYAML(ociPatch))), e("target", render.Map{e("kind", "OCIRepository"), e("name", releaseName), e("namespace", fluxNamespace)})},
			{e("patch", string(render.MustYAML(releasePatch))), e("target", render.Map{e("kind", "HelmRelease"), e("name", releaseName), e("namespace", fluxNamespace)})},
		})}
	r.Add(repo, dir+"kustomization.yaml", yamlFile(k))
}

// configMap renders a ConfigMap in the flux namespace with one key of YAML text.
func configMap(name, key string, value any) render.Map {
	return render.Map{e("apiVersion", "v1"), e("kind", "ConfigMap"),
		e("metadata", render.Map{e("name", name), e("namespace", fluxNamespace)}),
		e("data", render.Map{e(key, string(render.MustYAML(value)))})}
}

// valuesSecret renders a Secret whose single key values carries chart values
// as YAML text, with the generated placeholders in it listed for the commit
// step. No labels: the fleet's portal Secrets carry none, and a Secret file
// on record is never generated again.
func valuesSecret(name string, values render.Map, generated ...render.Generated) render.File {
	f := render.Secret(name, fluxNamespace, nil, render.ValueKey("values", string(render.MustYAML(values))))
	f.Generated = append(f.Generated, generated...)
	return f
}

// generated is a placeholder for a value the commit step creates.
func generated(name string, kind render.GeneratedKind, length int) render.Generated {
	return render.Generated{Name: name, Placeholder: render.Placeholder(name), Kind: kind, Length: length}
}

// userSecrets is user-secrets-backstage: the chart values the portal reads
// its own credentials from — the session secret, the Dex clients under the
// installations' names (the chart exposes them as AUTH_DEX_<NAME>_CLIENT_ID
// and _CLIENT_SECRET: the portal's own generated; another installation's the
// portal has a provider for, and the token broker's, supplied), the
// telemetry salt and, with sentry on, the DSNs.
func (in *Input) userSecrets(secrets map[string]string) render.File {
	session := generated(generatedSessionSecret, render.Base64, 32)
	client := generated(in.Installation.Name+"-"+generatedDexClientSecret, render.Base64, 32)
	salt := generated(generatedTelemetrySalt, render.Alphanumeric, 32)
	credentials := render.Map{e(in.Installation.Name, render.Map{e("clientID", render.PortalDexClientID), e("clientSecret", client.Placeholder)})}
	for _, inst := range in.providerInstallations() {
		if inst.Name != in.Installation.Name {
			credentials = append(credentials, e(inst.Name, render.Map{
				e("clientID", secrets[federationField(inst.Name, suffixClientID)]), e("clientSecret", secrets[federationField(inst.Name, suffixClientSecret)])}))
		}
	}
	if in.tokenBroker() != "" {
		credentials = append(credentials, e(brokerCredentials, render.Map{
			e("clientID", secrets[fieldTokenBroker+suffixClientID]), e("clientSecret", secrets[fieldTokenBroker+suffixClientSecret])}))
	}
	values := render.Map{
		e("authSessionSecret", session.Placeholder),
		e("dexAuthCredentials", credentials),
		e("telemetrydeck", render.Map{e("salt", salt.Placeholder)}),
	}
	if in.Plugins.Sentry.Enabled {
		values = append(values, e("sentry", render.Map{
			e("app", render.Map{e("dsn", secrets[fieldSentryAppDSN])}),
			e("backend", render.Map{e("dsn", secrets[fieldSentryBackendDSN])}),
			e("reportURI", secrets[fieldSentryReportURI]),
		}))
	}
	return valuesSecret(userSecretsName, values, session, client, salt)
}

// githubAppCredentials is github-app-credentials-backstage: the GitHub App
// as the chart writes it into github-app-credentials.yaml for the
// integrations.github include. The id is supplied at commit like the
// credentials: this file is the only place it lives.
func (in *Input) githubAppCredentials(secrets map[string]string) render.File {
	return valuesSecret(githubAppSecretName, render.Map{e("githubAppCredentials", render.Map{
		e("appId", appID(secrets[fieldGitHubAppID])),
		e("clientId", secrets[fieldGitHubClientID]),
		e("clientSecret", secrets[fieldGitHubClientSecret]),
		e("webhookSecret", secrets[fieldGitHubWebhookSecret]),
		e("privateKey", secrets[fieldGitHubPrivateKey]),
	})})
}

// appID is the GitHub App's id as the file carries it: the number the person
// supplied, or a dry run's marker.
func appID(value string) any {
	if n, err := strconv.Atoi(value); err == nil {
		return n
	}
	return value
}

// pluginKeys is plugin-keys-backstage: the plugin-to-plugin signing key pair
// (ES256) the chart mounts under /app/plugin-keys/<keyId>/, where
// backend.auth of the app-config reads it — each half a PEM as one scalar.
func (in *Input) pluginKeys() render.File {
	public := render.KeyPair(generatedPluginKeys, render.Public)
	private := render.KeyPair(generatedPluginKeys, render.Private)
	return valuesSecret(pluginKeysName, render.Map{e("pluginKeys", []render.Map{{
		e("keyId", in.PluginKeys.KeyID), e("publicKey", public.Placeholder), e("privateKey", private.Placeholder)}})},
		public, private)
}

// dexPatch is installations/<name>/apps/dex-app/configmap-values.yaml.patch on
// an installation without the platform: the portal's client, its secret a
// reference to the Secret in Dex's namespace.
func (in *Input) dexPatch() render.Map {
	return render.Map{e("oidc", render.Map{e("extraStaticClients", []render.Map{render.PortalDexClient(in.Portal.Domain, in.Installation.Name)})})}
}

// userValues is user-values-backstage: the portal's route on the
// installation's gateway, and under backstage the portal's environment and,
// with the tunnel on, the SPIFFE bundle's volume and mount — the chart mounts
// nothing of its own, so the portal's Node runtime finds the bundle only
// through them. backstage.extraEnvVars is one list Helm replaces wholesale
// across the HelmRelease's values sources (the shared base's default, this
// file, the agent-platform Component's values), so this file is its one
// owner and the Component sets none.
func (in *Input) userValues() render.Map {
	values := render.Map{e("route", render.Map{
		e("enabled", true),
		e("parentRefs", []render.Map{{e("name", "giantswarm-default"), e("namespace", "envoy-gateway-system")}}),
		e("hostnames", []string{in.Portal.Domain}),
		// The block the fleet's portals carry: Envoy Gateway's default route
		// timeout spans the whole response and would cut the agent platform's
		// server-sent turn stream, so no request or stream timeout, a 1 h idle
		// timeout and TCP keepalive keep the stream alive as long as the agent works.
		e("backendTrafficPolicy", render.Map{
			e("enabled", true),
			e("spec", render.Map{
				e("tcpKeepalive", render.Map{e("idleTime", "60s"), e("interval", "30s"), e("probes", 3)}),
				e("timeout", render.Map{
					e("http", render.Map{e("connectionIdleTimeout", "1h"), e("maxStreamDuration", "0s"), e("requestTimeout", "0s")}),
					e("tcp", render.Map{e("connectTimeout", "10s")}),
				}),
			}),
		}),
	})}
	backstage := render.Map{}
	if env := in.extraEnvVars(); len(env) > 0 {
		backstage = append(backstage, e("extraEnvVars", env))
	}
	if in.Tunnel.Enabled {
		// A directory mount, no subPath, so a refreshed bundle reaches the file.
		backstage = append(backstage,
			e("extraVolumes", []render.Map{{e("name", tunnelBundleName), e("secret", render.Map{e("secretName", tunnelBundleName)})}}),
			e("extraVolumeMounts", []render.Map{{e("name", tunnelBundleName), e("mountPath", tunnelBundleMount), e("readOnly", true)}}))
	}
	if len(backstage) > 0 {
		values = append(values, e("backstage", backstage))
	}
	return values
}

// extraEnvVars is the portal's environment: with the platform enabled, the
// installation's avatars host as the CSP image source the shared base's
// csp.imgSrc reads (its default is 'self'); with the tunnel on, the mounted
// SPIFFE bundle as Node's extra CA certificates, so the backend trusts the
// SVIDs the tunnel Services present. Nil without either: the list is not set
// and the shared base's default stands.
func (in *Input) extraEnvVars() []render.Map {
	var env []render.Map
	if in.Installation.AgentPlatform {
		env = append(env, render.Map{e("name", avatarsEnv), e("value", "https://"+in.host("avatars"))})
	}
	if in.Tunnel.Enabled {
		env = append(env, render.Map{e("name", tunnelCAEnv), e("value", tunnelBundleMount+"/"+tunnelBundleFile)})
	}
	return env
}

// tunnelBundle is tunnelport-spiffe-bundle.yaml: the tunnel's SPIFFE trust
// bundle copied from the platform namespace into the portal's, so the portal's
// Node runtime trusts the SVID the tunnel's ghostunnel serves. The bundle is
// written by tunnelport's trust-bundle bot into agent-platform/tunnelport-spiffe-bundle
// and a Pod cannot mount a Secret from another namespace, so External Secrets
// copies the public key (svid_bundle.pem) with the kubernetes provider; the
// reader ServiceAccount can get exactly that one Secret. Refresh follows the
// source, so a SPIFFE CA rotation propagates without a manual step.
var tunnelBundle = strings.TrimLeft(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: tunnelport-spiffe-bundle-reader
  namespace: backstage
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: tunnelport-spiffe-bundle-reader
  namespace: agent-platform
rules:
  - apiGroups: [""]
    resources: [secrets]
    resourceNames: [tunnelport-spiffe-bundle]
    verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: tunnelport-spiffe-bundle-reader
  namespace: agent-platform
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: tunnelport-spiffe-bundle-reader
subjects:
  - kind: ServiceAccount
    name: tunnelport-spiffe-bundle-reader
    namespace: backstage
---
# The store reconciler validates its access with a SelfSubjectRulesReview in
# remoteNamespace; system:basic-user grants that to every authenticated
# ServiceAccount, so no extra RBAC is needed for it.
apiVersion: external-secrets.io/v1
kind: SecretStore
metadata:
  name: tunnelport-spiffe-bundle
  namespace: backstage
spec:
  provider:
    kubernetes:
      remoteNamespace: agent-platform
      server:
        caProvider:
          type: ConfigMap
          name: kube-root-ca.crt
          key: ca.crt
      auth:
        serviceAccount:
          name: tunnelport-spiffe-bundle-reader
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: tunnelport-spiffe-bundle
  namespace: backstage
spec:
  refreshInterval: 15m
  secretStoreRef:
    kind: SecretStore
    name: tunnelport-spiffe-bundle
  target:
    name: tunnelport-spiffe-bundle
    creationPolicy: Owner
  data:
    - secretKey: svid_bundle.pem
      remoteRef:
        key: tunnelport-spiffe-bundle
        property: svid_bundle.pem
`, "\n")
