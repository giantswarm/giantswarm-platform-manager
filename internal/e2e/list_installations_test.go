package e2e

// The list_installations scenarios against the fake GitHub: an invented
// registry of invented installations (tree names, customers acme, umbrella,
// sealed and the hub's own example), each in one of the states readable from
// the repositories alone. Every read runs as the person whose bearer the call
// carries; nothing is cached between calls.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	registryRepo = "example/registry"
	registryPath = "catalog/installations.yaml"
	hub          = "hazel"
	hubMCs       = "example/example-management-clusters"
	hubConfigs   = "example/example-configs"
	acmeMCs      = "example/acme-management-clusters"
	acmeConfigs  = "example/acme-configs"
	umbrellaMCs  = "example/umbrella-management-clusters"
	// maple is umbrella's installation enabled by its owners before the
	// manager existed: both markers on record, no declaration.
	maple = "maple"
	// basesRepo is the fleet's shared collection base: the dex-app every
	// installation runs unless its collections kustomization pins its own.
	basesRepo = "example/management-cluster-bases"
)

// The fleet's dex-app: the base's pin, before the referenced Dex client
// secrets, and the version the installations that run the platform pin.
const (
	fleetDexApp    = "2.2.3"
	platformDexApp = "3.2.2"
)

// collectionsKustomization renders an installation's collections
// kustomization as the management-clusters repositories keep it: the fleet's
// base as a remote resource at main and, with pin, the installation's own
// patch on the App dex-app's version.
func collectionsKustomization(pin string) string {
	s := "resources:\n  - https://github.com/" + basesRepo + "//bases/collections/capa/stages/stable?ref=main\n"
	if pin != "" {
		s += "patches:\n  # the installation runs dex-app " + pin + " ahead of the fleet's shared pin\n  - target:\n      kind: App\n      name: dex-app\n      namespace: giantswarm\n    patch: |\n      - op: replace\n        path: /spec/version\n        value: " + pin + "\n"
	}
	return s
}

// dexAppApp renders the base's App dex-app at version.
func dexAppApp(version string) string {
	return "apiVersion: application.giantswarm.io/v1alpha1\nkind: App\nmetadata:\n  name: dex-app\n  namespace: giantswarm\nspec:\n  catalog: control-plane-catalog\n  kubeConfig:\n    inCluster: true\n  name: dex-app\n  namespace: giantswarm\n  version: " + version + "\n"
}

var registrySources = installations.Sources{Catalog: installations.Location{Repository: registryRepo, Path: registryPath}, Hub: hub}

// resource renders one catalog entry of an invented installation.
func resource(name, customer, provider, base string) string {
	return `---
apiVersion: backstage.io/v1alpha1
kind: Resource
metadata:
    name: ` + name + `
    labels:
        giantswarm.io/customer: ` + customer + `
        giantswarm.io/pipeline: stable
        giantswarm.io/provider: ` + provider + `
        giantswarm.io/region: eu-central-1
    annotations:
        giantswarm.io/account-engineer: Ada Example
        giantswarm.io/base: ` + base + `
    links:
        - url: https://github.com/example/` + customer + `-management-clusters
          type: CMC
        - url: https://github.com/example/` + customer + `-configs
          type: CCR
spec:
    owner: ` + customer + `
    type: installation
`
}

// portalConfig renders the hub's app-config ConfigMap with gs.installations.
// privateFixture is the installation the hub's portal reaches through the
// tunnel on the hub (its Kubernetes cluster entry names the tunnel's Service):
// the record's private flag.
const privateFixture = birch

// hubPortalClientID is the opaque id of the hub portal's Dex client on record;
// birchPortalClientID is a client id birch's own patch trusts as an audience
// of muster, on record nowhere else. The ids in one list alone are the
// installations' own: the plan keeps each in its list, nowhere else.
const (
	hubPortalClientID   = "Yx7hub0portal0client0id0on0record0"
	birchPortalClientID = "Yx7portal0client0id0in0the0patch0only"
	// hubExtraAudienceID is a client id the hub's kagent UI accepts (oidc-extra-audience), on record nowhere else.
	hubExtraAudienceID = "Yx7client0id0in0oidc0extra0audience0only"
	// hubPeerClientID is a client the hub's Dex trusts as a peer of the authenticator, on record nowhere else.
	hubPeerClientID = "Yx7client0id0in0the0hubs0trusted0peers0only"
	// hubPortalKustomization is the hub portal's directory kustomization on record: the chart line patched over the fleet base's tag.
	hubPortalKustomization = "resources:\n  - https://github.com/giantswarm/management-cluster-bases/extras/backstage/main?ref=main\n  - app-config.yaml\npatches:\n  - patch: |\n      - op: remove\n        path: /spec/ref/tag\n      - op: add\n        path: /spec/ref/semver\n        value: '>=2.1.0 <3.0.0'\n    target: {kind: OCIRepository, name: backstage, namespace: flux-giantswarm}\n"
	// birchPeerClientID is a portal client birch's Dex patch trusts as a peer of the authenticator, on record nowhere else.
	birchPeerClientID = "Yx7portal0client0id0in0trusted0peers0only"
)

func portalConfig(names ...string) string {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n    backstage:\n      appConfig: |\n        app:\n          baseUrl: https://portal." + hub + ".example.test\n        organization:\n          name: Example\n        gs:\n          installations:\n")
	for _, n := range names {
		b.WriteString("            " + n + ":\n              authProvider: oidc\n              baseDomain: " + n + ".example.test\n              providers:\n                - capa\n")
	}
	b.WriteString("        kubernetes:\n          clusterLocatorMethods:\n            - type: config\n              clusters:\n")
	for _, n := range names {
		url := "https://happaapi." + n + ".example.test"
		if n == privateFixture {
			url = "https://kubernetes-" + n + ".agent-platform.svc.cluster.local:8443"
		}
		b.WriteString("                - name: " + n + "\n                  url: " + url + "\n")
	}
	return b.String()
}

// fixtures loads the registry and the installations' repositories into the
// fake: hazel (the hub, enabled, chart line 4), alder (not enabled, the
// fleet's dex-app), birch (enabled, private: the hub's portal reaches
// it through the tunnel), rowan (not enabled, a complete record), willow
// (not enabled, a bare record), maple (both markers on record: enabled by
// hand before the manager existed), oak
// (repositories the person may not read) and larch (portal only, no
// repositories on record). The fleet's base
// pins dex-app before the referenced Dex client secrets; hazel, birch and
// rowan pin the version that takes them, alder runs the fleet's.
// The kustomizations other owners write, which the includes land in: an
// installation's extras, and the portal's tree on rowan.
const (
	extrasKustomization         = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./monitoring/\n"
	extrasListingEverything     = extrasKustomization + "  - ./agent-platform/\n  - ./mcp-capi/\n  - ./mcp-kubernetes/\n  - ./mcp-prometheus/\n"
	rowanBackstageKustomization = "management-clusters/rowan/extras/backstage/kustomization.yaml"
)

func extrasKustomizationPath(installation string) string {
	return "management-clusters/" + installation + "/extras/kustomization.yaml"
}

// clusterAppManifest renders an installation's cluster App manifest as the
// management-clusters repositories keep it: the ConfigMap with the chart's
// values and the App of the provider's cluster chart. With gates, the values
// carry the three feature gates Agent Substrate needs on every kubeadm
// component — the shape the installations that run the platform's 4 line
// carry; without, the record runs a chart before the gates' default and says
// the cluster does not serve PodCertificateRequest.
func clusterAppManifest(installation, chart, version string, gates bool) string {
	values := "global:\n  metadata:\n    name: " + installation + "\n"
	if gates {
		values += "cluster:\n  internal:\n    advancedConfiguration:\n      controlPlane:\n        apiServer:\n" + substrateGates("          ") +
			"        controllerManager:\n" + substrateGates("          ") + "      kubelet:\n" + substrateGates("        ")
	}
	return "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + installation + "-userconfig\n  namespace: org-giantswarm\ndata:\n  values: |\n" + indentLines(values, "    ") +
		"---\napiVersion: application.giantswarm.io/v1alpha1\nkind: App\nmetadata:\n  name: " + installation + "\n  namespace: org-giantswarm\nspec:\n  catalog: cluster\n  name: " + chart + "\n  version: " + version + "\n  userConfig:\n    configMap:\n      name: " + installation + "-userconfig\n      namespace: org-giantswarm\n"
}

// substrateGates is a component's featureGates list with the three gates
// enabled, at indent.
func substrateGates(indent string) string {
	var b strings.Builder
	b.WriteString(indent + "featureGates:\n")
	for _, g := range []string{"PodCertificateRequest", "ClusterTrustBundle", "ClusterTrustBundleProjection"} {
		b.WriteString(indent + "  - name: " + g + "\n" + indent + "    enabled: true\n")
	}
	return b.String()
}

func indentLines(s, prefix string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(s, "\n") {
		if line != "" {
			b.WriteString(prefix + line)
		}
	}
	return b.String()
}

func fixtures(g *fakeGitHub) {
	g.addRepo(registryRepo, map[string]string{registryPath: "---\napiVersion: backstage.io/v1alpha1\nkind: Group\nmetadata:\n    name: acme\nspec:\n    type: customer\n" +
		resource(hub, "example", "capa", "example.test") + resource(alder, "acme", "capa", "acme.test") + resource(birch, "acme", "capa", "acme.test") +
		resource("rowan", "acme", "capa", "acme.test") + resource("willow", "umbrella", "capz", "umbrella.test") + resource(maple, "umbrella", "capz", "umbrella.test") + resource("oak", "sealed", "capa", "sealed.test")})
	g.addRepo(hubMCs, map[string]string{
		installations.PortalConfigPath(hub):             portalConfig(hub, alder, birch, "rowan", "willow", maple, "oak", "larch"),
		installations.ClusterAppManifestPath(hub):       clusterAppManifest(hub, "cluster-aws", "10.2.0", true),
		installations.CollectionsKustomizationPath(hub): collectionsKustomization(platformDexApp),
		extrasKustomizationPath(hub):                    extrasListingEverything,
		// The hub's portal lists the hub itself: the platform's fragment joins the hub's portal tree as a Component.
		"management-clusters/" + hub + "/extras/backstage/kustomization.yaml":           "# The portal's tree; the platform's fragment joins it as a Component.\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./backstage/\n",
		"management-clusters/" + hub + "/extras/backstage/backstage/kustomization.yaml": hubPortalKustomization,
	})
	g.addRepo(hubConfigs, map[string]string{
		installations.ConfigPatchPath(hub): "codename: hazel\nbase: example.test\ncustomer: example\nmanagementCluster:\n  private: false\nagentPlatform:\n  kagentApiV2: true\nservices:\n  muster:\n    clientId: muster-hazel\n",
		// The hub trusts its portal today: the id is also in its own patch. Its kagent UI accepts one more id, in that list alone.
		installations.Capabilities()[0].EnabledMarker(hub): "muster:\n  muster:\n    oauth:\n      server:\n        trustedAudiences:\n          - dex-k8s-authenticator\n          - " + hubPortalClientID + "\n" +
			"kagent:\n  oauth2-proxy:\n    extraArgs:\n      oidc-extra-audience: dex-k8s-authenticator,kagent," + hubPortalClientID + ",backstage," + hubExtraAudienceID + "\n",
		// The hub portal's Dex client on record: its id is the audience the platform trusts for the portal. The authenticator trusts one more peer, in that list alone.
		installations.DexPatchPath(hub): "oidc:\n  staticClients:\n    dexK8SAuthenticator:\n      trustedPeers:\n        - " + hubPortalClientID + "\n        - backstage\n        - " + hubPeerClientID + "\n" +
			"  extraStaticClients:\n    - id: " + hubPortalClientID + "\n      name: Dev Portal\n      redirectURIs:\n        - " + render.PortalRedirectURI("portal."+hub+".example.test", hub) + "\n      secretRef: {name: dex-client-backstage, key: secret}\n",
	})
	g.addRepo(acmeMCs, map[string]string{
		installations.CollectionsKustomizationPath(alder):   collectionsKustomization(""),
		installations.CollectionsKustomizationPath(birch):   collectionsKustomization(platformDexApp),
		extrasKustomizationPath(birch):                      extrasListingEverything,
		installations.ClusterAppManifestPath("rowan"):       clusterAppManifest("rowan", "cluster-aws", "10.2.0", false),
		installations.CollectionsKustomizationPath("rowan"): collectionsKustomization(platformDexApp),
		extrasKustomizationPath("rowan"):                    extrasKustomization,
		rowanBackstageKustomization:                         "# The portal's tree; the platform's fragment joins it as a Component.\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./backstage/\n",
	})
	g.addRepo(acmeConfigs, map[string]string{
		installations.ConfigPatchPath(alder):   "codename: alder\nbase: acme.test\n",
		installations.ConfigPatchPath(birch):   "codename: birch\nbase: acme.test\n",
		installations.ConfigPatchPath("rowan"): "codename: rowan\nbase: acme.test\nservices:\n  muster:\n    clientId: muster-rowan\n",
		// birch's muster trusts a client on record in that list only; its authenticator trusts a peer on record in that list only.
		installations.Capabilities()[0].EnabledMarker(birch): "muster:\n  muster:\n    oauth:\n      server:\n        trustedAudiences:\n          - dex-k8s-authenticator\n          - " + birchPortalClientID + "\n",
		installations.DexPatchPath(birch):                    "oidc:\n  staticClients:\n    dexK8SAuthenticator:\n      trustedPeers:\n        - dex-k8s-authenticator\n        - " + birchPeerClientID + "\n",
	})
	g.addRepo(umbrellaMCs, map[string]string{
		// maple's portal on record, put there by hand: the customer-portal marker.
		installations.PortalConfigPath(maple): portalConfig(maple),
	})
	g.addRepo("example/umbrella-configs", map[string]string{
		installations.ConfigPatchPath("willow"): "codename: willow\n",
		installations.ConfigPatchPath(maple):    "codename: maple\nbase: umbrella.test\n",
		// maple's platform values on record, put there by hand: the agent-platform marker.
		installations.Capabilities()[0].EnabledMarker(maple): "muster:\n  muster:\n    oauth:\n      server:\n        trustedAudiences:\n          - dex-k8s-authenticator\n",
	})
	g.addRepo("example/shared-configs", map[string]string{"default/config.yaml": "services:\n  muster:\n    clientId: muster-shared\n"})
	g.addRepo(basesRepo, map[string]string{installations.DexAppBasePath: dexAppApp(fleetDexApp)})
	g.forbid("example/sealed-management-clusters")
	g.forbid("example/sealed-configs")
}

func listInstallations(t *testing.T, c *client.Client, args map[string]any) (tools.ListInstallationsResult, string, bool) {
	t.Helper()
	text, isErr := call(t, c, tools.ToolListInstallations, args)
	var out tools.ListInstallationsResult
	if !isErr {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decode: %v\n%s", err, text)
		}
	}
	return out, text, isErr
}

func find(t *testing.T, out tools.ListInstallationsResult, name string) installations.Report {
	t.Helper()
	for _, r := range out.Installations {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("%s is not in the answer: %+v", name, out.Installations)
	return installations.Report{}
}

// Every installation of the registry is answered with the state readable
// from its repositories now, as alice.
func TestListInstallationsStates(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	out, text, isErr := listInstallations(t, st.mcpClient(t, aliceToken), nil)
	if isErr {
		t.Fatal(text)
	}
	if out.Caller != alice || out.Hub != hub || len(out.Installations) != 8 || len(out.Capabilities) != 2 || out.Capabilities[0] != installations.AgentPlatform || out.Capabilities[1] != installations.CustomerPortal ||
		out.Registry.Catalog.Repository != registryRepo || out.Registry.Portal.Repository != hubMCs || out.Registry.Portal.Path != installations.PortalConfigPath(hub) {
		t.Fatalf("answer: %s", text)
	}

	hazel := find(t, out, hub)
	if !hazel.Hub || !hazel.Readable || hazel.Capabilities[0].State != installations.StateEnabled || !hazel.Capabilities[0].Enabled ||
		hazel.Record == nil || hazel.Record.ChartLine != "4" || hazel.Record.Private || hazel.Record.MusterClientID != "muster-hazel" || hazel.Record.BaseDomain != "hazel.example.test" ||
		hazel.AuthProvider != "oidc" || len(hazel.Sources) != 2 || hazel.Capabilities[0].LastAction != nil {
		t.Fatalf("hazel: %+v", hazel)
	}
	// The hub's portal app-config is the portal's marker, read in the management-clusters repository.
	if portal := hazel.Capabilities[1]; portal.Name != installations.CustomerPortal || portal.State != installations.StateEnabled || !portal.Enabled ||
		portal.MarkerRepository != installations.ManagementClustersRepository || portal.EnabledMarker != installations.PortalConfigPath(hub) {
		t.Fatalf("hazel portal: %+v", portal)
	}
	if in, ok := hazel.Capabilities[0].Inputs["installation"].(map[string]any); !ok || in["chartLine"] != "4" {
		t.Fatalf("hazel inputs on record: %+v", hazel.Capabilities[0].Inputs)
	}

	alder := find(t, out, alder)
	if alder.Hub || alder.Capabilities[0].State != installations.StateNotEnabled || alder.Capabilities[0].Enabled || alder.Capabilities[1].State != installations.StateNotEnabled || !alder.Readable ||
		alder.Customer != "acme" || alder.AccountEngineer != "Ada Example" || alder.Record == nil || alder.Record.ChartLine != "3" {
		t.Fatalf("alder: %+v", alder)
	}

	birch := find(t, out, birch)
	if birch.Capabilities[0].State != installations.StateEnabled || !birch.Capabilities[0].Enabled || !birch.Record.Private ||
		birch.Capabilities[0].EnabledMarker != "installations/birch/apps/agent-platform/configmap-values.yaml.patch" ||
		birch.Capabilities[1].State != installations.StateNotEnabled || birch.Capabilities[1].Enabled || birch.Capabilities[1].MarkerRepository != installations.ManagementClustersRepository {
		t.Fatalf("birch: %+v", birch)
	}

	rowan := find(t, out, "rowan")
	if rowan.Capabilities[0].State != installations.StateNotEnabled || rowan.Capabilities[0].Enabled {
		t.Fatalf("rowan: %+v", rowan)
	}

	willow := find(t, out, "willow")
	if willow.Capabilities[0].State != installations.StateNotEnabled || willow.Capabilities[0].Enabled || willow.Provider != "capz" {
		t.Fatalf("willow: %+v", willow)
	}

	// maple's people enabled both capabilities by hand: the filesets on
	// record are the fact, whoever put them there.
	mapleR := find(t, out, maple)
	if !mapleR.Readable || mapleR.Record == nil || mapleR.Record.MusterClientID != "muster-shared" ||
		mapleR.Capabilities[0].State != installations.StateEnabled || !mapleR.Capabilities[0].Enabled ||
		mapleR.Capabilities[1].State != installations.StateEnabled || !mapleR.Capabilities[1].Enabled || len(mapleR.Errors) != 0 {
		t.Fatalf("maple: %+v", mapleR)
	}

	oak := find(t, out, "oak")
	if oak.Readable || oak.Capabilities[0].State != installations.StateUnknown || oak.Capabilities[1].State != installations.StateUnknown || len(oak.Errors) == 0 ||
		!strings.Contains(strings.Join(oak.Errors, " "), "forbidden") {
		t.Fatalf("oak: %+v", oak)
	}

	larch := find(t, out, "larch")
	if larch.Readable || larch.Repositories.Known() || len(larch.Capabilities) != 2 || larch.Capabilities[1].State != installations.StateUnknown || len(larch.Errors) != 1 ||
		len(larch.Sources) != 1 || larch.Sources[0] != installations.SourcePortal || larch.BaseDomain != "larch.example.test" {
		t.Fatalf("larch: %+v", larch)
	}

	if len(out.Unreadable) != 2 || out.Unreadable[0] != "larch" || out.Unreadable[1] != "oak" {
		t.Fatalf("unreadable: %v", out.Unreadable)
	}
	if len(out.States.FromRepositories) != 3 || len(out.States.FromActions) != 5 {
		t.Fatalf("states: %+v", out.States)
	}
}

// The markers are read at call time, never cached: a fileset that lands
// between two calls changes the answer.
func TestListInstallationsReadsTheMarkersEveryCall(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	marker := installations.Capabilities()[0].EnabledMarker(alder)
	out, _, _ := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{alder}})
	if find(t, out, alder).Capabilities[0].State != installations.StateNotEnabled {
		t.Fatal("alder enabled before its fileset landed")
	}
	st.ghs.addFile(acmeConfigs, marker, "muster: {}\n")
	out, _, _ = listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{alder}})
	if a := find(t, out, alder).Capabilities[0]; a.State != installations.StateEnabled || !a.Enabled {
		t.Fatal("the fileset that landed was not read")
	}
	if n := st.ghs.reads(acmeConfigs, marker); n != 2 {
		t.Fatalf("the marker was read %d times for two calls", n)
	}
}

// summary answers the states and the last actions alone: the record, the
// cluster App, the portals and the federation facts are neither read nor
// answered, and the answer says so.
func TestListInstallationsSummary(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	out, text, isErr := listInstallations(t, st.mcpClient(t, aliceToken), map[string]any{tools.ArgSummary: true})
	if isErr || !out.Summary || len(out.Installations) != 8 || len(out.Capabilities) != 2 {
		t.Fatalf("answer: %s", text)
	}
	hazelR, birchR, alderR, mapleR, oakR := find(t, out, hub), find(t, out, birch), find(t, out, alder), find(t, out, maple), find(t, out, "oak")
	if !hazelR.Readable || hazelR.Capabilities[0].State != installations.StateEnabled || hazelR.Capabilities[1].State != installations.StateEnabled ||
		hazelR.Record != nil || hazelR.Portals != nil || hazelR.Federation != nil || hazelR.Capabilities[0].Inputs != nil {
		t.Fatalf("hazel: %+v", hazelR)
	}
	if birchR.Capabilities[0].State != installations.StateEnabled || birchR.Capabilities[1].State != installations.StateNotEnabled || birchR.Record != nil ||
		alderR.Capabilities[0].State != installations.StateNotEnabled || alderR.Record != nil ||
		mapleR.Capabilities[0].State != installations.StateEnabled || mapleR.Capabilities[1].State != installations.StateEnabled || !mapleR.Capabilities[1].Enabled || mapleR.Record != nil ||
		oakR.Readable || oakR.Capabilities[0].State != installations.StateUnknown {
		t.Fatalf("birch %+v alder %+v maple %+v oak %+v", birchR, alderR, mapleR, oakR)
	}
	for _, read := range []struct{ repo, path string }{
		{hubConfigs, installations.ConfigPatchPath(hub)},
		{acmeConfigs, installations.ConfigPatchPath(alder)},
		{hubMCs, installations.ClusterAppManifestPath(hub)},
		{"example/shared-configs", "default/config.yaml"},
	} {
		if n := st.ghs.reads(read.repo, read.path); n != 0 {
			t.Errorf("summary read %s:%s %d times", read.repo, read.path, n)
		}
	}
	if n := st.ghs.reads(acmeConfigs, installations.Capabilities()[0].EnabledMarker(birch)); n != 1 {
		t.Errorf("the marker was read %d times", n)
	}
	// The default answer is the full one.
	out, text, isErr = listInstallations(t, st.mcpClient(t, aliceToken), nil)
	if isErr || out.Summary || find(t, out, hub).Record == nil {
		t.Fatalf("default: %s", text)
	}
}

// The owner's shared default config, which every installation without its
// own client id falls back to, is read once per call.
func TestListInstallationsReadsTheSharedDefaultOnce(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	out, text, isErr := listInstallations(t, st.mcpClient(t, aliceToken), nil)
	if isErr {
		t.Fatal(text)
	}
	// alder, birch and willow have no client id of their own.
	for _, name := range []string{alder, birch, "willow"} {
		if rec := find(t, out, name).Record; rec == nil || rec.MusterClientID != "muster-shared" {
			t.Fatalf("%s: %+v", name, rec)
		}
	}
	if n := st.ghs.reads("example/shared-configs", "default/config.yaml"); n != 1 {
		t.Fatalf("the shared default was read %d times in one call", n)
	}
}

// installations and customer narrow the answer; a name the registry does not
// know is refused.
func TestListInstallationsFilters(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{birch, "rowan"}})
	if isErr || len(out.Installations) != 2 || out.Installations[0].Name != birch || out.Installations[1].Name != "rowan" {
		t.Fatalf("by name: %s", text)
	}
	out, text, isErr = listInstallations(t, c, map[string]any{tools.ArgCustomer: "acme"})
	if isErr || len(out.Installations) != 3 || len(out.Unreadable) != 0 {
		t.Fatalf("by customer: %s", text)
	}
	if _, text, isErr = listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{"spruce"}}); !isErr || !strings.Contains(text, `"spruce" is not in the registry`) {
		t.Fatalf("unknown name: %s", text)
	}
}

// A registry the person cannot read as themselves is refused with the
// requirement: the App installed on the repository, and the person able to
// read it. No token of the manager's own steps in.
func TestListInstallationsRegistryUnreadable(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	st.ghs.forbid(registryRepo)
	_, text, isErr := listInstallations(t, st.mcpClient(t, aliceToken), nil)
	if !isErr || !strings.Contains(text, registryRepo) || !strings.Contains(text, "the App must be installed on the repository") {
		t.Fatalf("registry forbidden: %s", text)
	}
}

// Without a caller there is nothing to read the registry as: the tool says so
// instead of reading with a token of its own.
func TestListInstallationsNeedsACaller(t *testing.T) {
	ts := tools.New(tools.Deps{Version: testVersion, Registry: registrySources})
	c, err := client.NewInProcessClient(ts.MCPServer())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(context.Background(), mcp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	text, isErr := call(t, c, tools.ToolListInstallations, nil)
	if !isErr || !strings.Contains(text, "needs a caller") {
		t.Fatalf("anonymous: %s", text)
	}
}

// get_info reports the registry configuration, and list_installations is no
// longer a planned tool.
func TestGetInfoReportsTheRegistry(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	text, isErr := call(t, c, tools.ToolGetInfo, nil)
	var info tools.Info
	if isErr || json.Unmarshal([]byte(text), &info) != nil || !info.Registry.Configured || info.Registry.Hub != hub || info.Registry.Catalog.Repository != registryRepo {
		t.Fatalf("get_info registry: %s", text)
	}
	for _, planned := range info.PlannedTools {
		if planned == tools.ToolListInstallations {
			t.Fatal("list_installations is still listed as planned")
		}
	}
}
