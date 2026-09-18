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
)

const (
	registryRepo = "example/registry"
	registryPath = "catalog/installations.yaml"
	hub          = "hazel"
	hubMCs       = "example/example-management-clusters"
	hubConfigs   = "example/example-configs"
	acmeMCs      = "example/acme-management-clusters"
	acmeConfigs  = "example/acme-configs"
)

var registrySources = installations.Sources{Catalog: installations.Location{Repository: registryRepo, Path: registryPath}, Hub: hub}

const optedIn = "# the owners' consent\noptIn: true\n"

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
func portalConfig(names ...string) string {
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n    backstage:\n      appConfig: |\n        gs:\n          installations:\n")
	for _, n := range names {
		b.WriteString("            " + n + ":\n              authProvider: oidc\n              baseDomain: " + n + ".example.test\n              providers:\n                - capa\n")
	}
	return b.String()
}

// fixtures loads the registry and the installations' repositories into the
// fake: hazel (the hub, opted in, enabled, chart line 4), alder (no
// declaration), birch (opted in, enabled, private), rowan (opted in, not
// enabled), willow (optIn: false), oak (repositories the person may not
// read) and larch (portal only, no repositories on record).
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

func fixtures(g *fakeGitHub) {
	g.addRepo(registryRepo, map[string]string{registryPath: "---\napiVersion: backstage.io/v1alpha1\nkind: Group\nmetadata:\n    name: acme\nspec:\n    type: customer\n" +
		resource(hub, "example", "capa", "example.test") + resource("alder", "acme", "capa", "acme.test") + resource("birch", "acme", "capa", "acme.test") +
		resource("rowan", "acme", "capa", "acme.test") + resource("willow", "umbrella", "capz", "umbrella.test") + resource("oak", "sealed", "capa", "sealed.test")})
	g.addRepo(hubMCs, map[string]string{
		installations.PortalConfigPath(hub): portalConfig(hub, "alder", "birch", "rowan", "willow", "oak", "larch"),
		installations.OptInPath(hub):        optedIn,
		extrasKustomizationPath(hub):        extrasListingEverything,
	})
	g.addRepo(hubConfigs, map[string]string{
		installations.ConfigPatchPath(hub):                 "codename: hazel\nbase: example.test\ncustomer: example\nmanagementCluster:\n  private: false\nagentPlatform:\n  kagentApiV2: true\nservices:\n  muster:\n    clientId: muster-hazel\n",
		installations.Capabilities()[0].EnabledMarker(hub): "configmap: {}\n",
	})
	g.addRepo(acmeMCs, map[string]string{
		installations.OptInPath("birch"): optedIn,
		extrasKustomizationPath("birch"): extrasListingEverything,
		installations.OptInPath("rowan"): optedIn,
		extrasKustomizationPath("rowan"): extrasKustomization,
		rowanBackstageKustomization:      "# The portal's tree; the platform's fragment joins it as a Component.\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./backstage/\n",
	})
	g.addRepo(acmeConfigs, map[string]string{
		installations.ConfigPatchPath("alder"):                 "codename: alder\nbase: acme.test\n",
		installations.ConfigPatchPath("birch"):                 "codename: birch\nbase: acme.test\nmanagementCluster:\n  private: true\n",
		installations.ConfigPatchPath("rowan"):                 "codename: rowan\nbase: acme.test\nservices:\n  muster:\n    clientId: muster-rowan\n",
		installations.Capabilities()[0].EnabledMarker("birch"): "configmap: {}\n",
	})
	g.addRepo("example/umbrella-management-clusters", map[string]string{installations.OptInPath("willow"): "optIn: false\n"})
	g.addRepo("example/umbrella-configs", map[string]string{installations.ConfigPatchPath("willow"): "codename: willow\n"})
	g.addRepo("example/shared-configs", map[string]string{"default/config.yaml": "services:\n  muster:\n    clientId: muster-shared\n"})
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
	if out.Caller != alice || out.Hub != hub || len(out.Installations) != 7 || len(out.Capabilities) != 2 || out.Capabilities[0] != installations.AgentPlatform || out.Capabilities[1] != installations.CustomerPortal ||
		out.Registry.Catalog.Repository != registryRepo || out.Registry.Portal.Repository != hubMCs || out.Registry.Portal.Path != installations.PortalConfigPath(hub) {
		t.Fatalf("answer: %s", text)
	}

	hazel := find(t, out, hub)
	if !hazel.Hub || !hazel.Readable || hazel.OptIn.State != installations.OptedIn || hazel.Capabilities[0].State != installations.StateEnabled ||
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

	alder := find(t, out, "alder")
	if alder.Hub || alder.OptIn.State != installations.NotOptedIn || alder.OptIn.Present || alder.OptIn.Path != installations.OptInPath("alder") || alder.OptIn.Repository != acmeMCs ||
		!strings.Contains(alder.OptIn.HowToOptIn, "optIn: true") || !strings.Contains(alder.OptIn.HowToOptIn, acmeMCs) || !strings.Contains(alder.OptIn.HowToOptIn, "never") ||
		alder.Capabilities[0].State != installations.StateNotOptedIn || alder.Capabilities[0].Enabled || alder.Capabilities[1].State != installations.StateNotOptedIn || !alder.Readable ||
		alder.Customer != "acme" || alder.AccountEngineer != "Ada Example" || alder.Record == nil || alder.Record.ChartLine != "3" {
		t.Fatalf("alder: %+v %+v", alder, alder.OptIn)
	}

	birch := find(t, out, "birch")
	if birch.OptIn.State != installations.OptedIn || birch.Capabilities[0].State != installations.StateEnabled || !birch.Capabilities[0].Enabled || !birch.Record.Private ||
		birch.Capabilities[0].EnabledMarker != "installations/birch/apps/agent-platform/configmap-values.yaml.patch" ||
		birch.Capabilities[1].State != installations.StateNotEnabled || birch.Capabilities[1].Enabled || birch.Capabilities[1].MarkerRepository != installations.ManagementClustersRepository {
		t.Fatalf("birch: %+v", birch)
	}

	rowan := find(t, out, "rowan")
	if rowan.OptIn.State != installations.OptedIn || rowan.Capabilities[0].State != installations.StateNotEnabled || rowan.Capabilities[0].Enabled {
		t.Fatalf("rowan: %+v", rowan)
	}

	willow := find(t, out, "willow")
	if willow.OptIn.State != installations.NotOptedIn || !willow.OptIn.Present || willow.OptIn.Value == nil || *willow.OptIn.Value || willow.OptIn.HowToOptIn == "" ||
		willow.Capabilities[0].State != installations.StateNotOptedIn || willow.Provider != "capz" {
		t.Fatalf("willow: %+v %+v", willow, willow.OptIn)
	}

	oak := find(t, out, "oak")
	if oak.Readable || oak.OptIn.State != installations.OptInUnreadable || oak.Capabilities[0].State != installations.StateUnknown || oak.Capabilities[1].State != installations.StateUnknown || len(oak.Errors) == 0 ||
		!strings.Contains(strings.Join(oak.Errors, " "), "forbidden") {
		t.Fatalf("oak: %+v %+v", oak, oak.OptIn)
	}

	larch := find(t, out, "larch")
	if larch.Readable || larch.Repositories.Known() || larch.OptIn != nil || len(larch.Capabilities) != 2 || larch.Capabilities[1].State != installations.StateUnknown || len(larch.Errors) != 1 ||
		len(larch.Sources) != 1 || larch.Sources[0] != installations.SourcePortal || larch.BaseDomain != "larch.example.test" {
		t.Fatalf("larch: %+v", larch)
	}

	if len(out.Unreadable) != 2 || out.Unreadable[0] != "larch" || out.Unreadable[1] != "oak" {
		t.Fatalf("unreadable: %v", out.Unreadable)
	}
	if len(out.States.FromRepositories) != 4 || len(out.States.FromActions) != 5 {
		t.Fatalf("states: %+v", out.States)
	}
}

// The opt-in is read at call time, never cached: a declaration that lands
// between two calls changes the answer.
func TestListInstallationsReadsTheOptInEveryCall(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, _, _ := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{"alder"}})
	if find(t, out, "alder").Capabilities[0].State != installations.StateNotOptedIn {
		t.Fatal("alder opted in before the declaration landed")
	}
	st.ghs.addRepo(acmeMCs, map[string]string{installations.OptInPath("alder"): optedIn})
	out, _, _ = listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{"alder"}})
	if find(t, out, "alder").Capabilities[0].State != installations.StateNotEnabled {
		t.Fatal("the declaration that landed was not read")
	}
	if n := st.ghs.reads(acmeMCs, installations.OptInPath("alder")); n != 2 {
		t.Fatalf("the declaration was read %d times for two calls", n)
	}
}

// installations and customer narrow the answer; a name the registry does not
// know is refused.
func TestListInstallationsFilters(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{"birch", "rowan"}})
	if isErr || len(out.Installations) != 2 || out.Installations[0].Name != "birch" || out.Installations[1].Name != "rowan" {
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
