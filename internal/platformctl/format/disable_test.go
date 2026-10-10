package format

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

var update = flag.Bool("update", false, "rewrite the golden dry runs from the current output")

// The disable's dry run as platformctl prints it, held to a golden: the
// agent-platform definition's public-customer shape on record as rendered,
// beside it a client of the installation's own in the shared Dex patch, a
// file another owner put into the definition's directory and the extras'
// own entries, the remote base's objects read at its ref for the checklist,
// each line saying what its deletion takes with it. The hub
// brokers into the installation, so the commit is refused on the pairing.
// baseFiles are the remote base of one extra as the fleet's bases
// repository carries it, by path: its namespaces, its chart sources and
// HelmReleases — an MCP server's with its valkey's — and its Konfiguration.
func baseFiles(extra string) map[string]string {
	namespaces, releases := []string{extra}, []string{extra}
	if extra == installations.AgentPlatform {
		namespaces = append(namespaces, "kagent")
	} else {
		releases = append(releases, extra+"-valkey")
	}
	files := map[string]string{"konfiguration.yaml": "apiVersion: konfigure.giantswarm.io/v1alpha1\nkind: Konfiguration\nmetadata:\n  name: " + extra + "-konfiguration\n  namespace: flux-giantswarm\n"}
	resources := []string{"namespace.yaml"}
	var ns []string
	for _, n := range namespaces {
		ns = append(ns, "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: "+n+"\n")
	}
	files["namespace.yaml"] = strings.Join(ns, "---\n")
	for _, r := range releases {
		files[r+"-oci-repository.yaml"] = "apiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: " + r + "\n  namespace: flux-giantswarm\n"
		files[r+"-helm-release.yaml"] = "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: " + r + "\n  namespace: flux-giantswarm\nspec:\n  targetNamespace: " + extra + "\n"
		resources = append(resources, "./"+r+"-oci-repository.yaml", "./"+r+"-helm-release.yaml")
	}
	files["kustomization.yaml"] = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - " + strings.Join(append(resources, "./konfiguration.yaml"), "\n  - ") + "\n"
	return files
}

func TestDisableDryRunGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "render", "agentplatform", "testdata", "public-customer", "input.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input map[string]any `yaml:"input"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	in, err := agentplatform.Parse(doc.Input)
	if err != nil {
		t.Fatal(err)
	}
	res, err := agentplatform.Render(doc.Input, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		t.Fatal(err)
	}
	const name = "kestrel"
	inst := installations.Installation{Name: name, Customer: "oakridge", Repositories: installations.Repositories{Configs: "giantswarm/oakridge-configs", ManagementClusters: "giantswarm/oakridge-management-clusters"}}
	hub := installations.Installation{Name: "gopher", Customer: "giantswarm", Hub: true, Repositories: installations.Repositories{Configs: "giantswarm/giantswarm-configs", ManagementClusters: "giantswarm/giantswarm-management-clusters"}}
	files := map[string]string{}
	for repo, fs := range res.Files {
		for p, f := range fs {
			files[string(repo)+":"+p] = string(f.Content)
		}
	}
	mc := "giantswarm/oakridge-management-clusters:management-clusters/" + name + "/extras/"
	dex := "giantswarm/oakridge-configs:installations/" + name + "/apps/dex-app/configmap-values.yaml.patch"
	files[dex] = "ingress:\n  largeHeaderBuffers: true\n" + strings.Replace(files[dex], "  extraStaticClients:\n", "  extraStaticClients:\n    - id: own-tool\n      name: own-tool\n      secretRef:\n        name: dex-client-own-tool\n        key: secret\n", 1)
	files[mc+"kustomization.yaml"] = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./zot/\n  - ./agent-platform/\n  - ./mcp-kubernetes/\n  - ./mcp-prometheus/\n  - ./mcp-capi/\n"
	files[mc+"backstage/kustomization.yaml"] = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./backstage/\ncomponents:\n  - ./agent-platform/\n"
	files[mc+"agent-platform/mcpclients/gateway.yaml"] = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: gateway-callbacks\n  namespace: agent-platform\n"
	for _, extra := range []string{installations.AgentPlatform, "mcp-kubernetes", "mcp-prometheus", "mcp-capi"} {
		for p, content := range baseFiles(extra) {
			files["giantswarm/management-cluster-bases:extras/"+extra+"/"+p] = content
		}
	}
	read := func(_ context.Context, repository, p string) (string, error) {
		if c, ok := files[repository+":"+p]; ok {
			return c, nil
		}
		return "", fmt.Errorf("%s: %w", p, gh.ErrNotFound)
	}
	list := func(_ context.Context, repository, dir string) ([]string, error) {
		var out []string
		for k := range files {
			if p, ok := strings.CutPrefix(k, repository+":"); ok && strings.HasPrefix(p, dir+"/") {
				out = append(out, p)
			}
		}
		sort.Strings(out)
		return out, nil
	}
	def, _ := installations.FindCapability(installations.AgentPlatform)
	d := plan.Disable(context.Background(), plan.DisableOptions{Definition: def, Installation: inst, Hub: hub, Result: res, Read: read, List: list})
	if len(d.Bases) != 4 {
		t.Fatalf("bases %v", d.Bases)
	}
	base, err := plan.BaseObjects(context.Background(), func(ref string) plan.Reader {
		if ref != "main" {
			t.Errorf("the base is read at %q, its kustomization names main", ref)
		}
		return read
	}, d.Bases)
	if err != nil {
		t.Fatal(err)
	}
	d.Checklist = plan.Checklist(append(base, d.Checklist...))
	out := tools.DisableResult{Caller: "jane", Tool: tools.ToolDisableCapability, Capability: def.Name, Hub: hub.Name, Installation: name, DryRun: true, State: installations.StateEnabled, Plan: d, Remaining: []string{}}
	for _, p := range d.Pairings() {
		peer, file, _ := strings.Cut(p, ":")
		out.References = append(out.References, name+" shares a value with "+peer+", which holds the other side in "+file)
	}
	out.CommitRefused = fmt.Sprintf("%d reference(s) still depend on %s on %s", len(out.References), def.Name, name)

	var buf bytes.Buffer
	if err := Disable(&buf, out, true); err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/disable-dry-run.golden"
	if *update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if buf.String() != string(want) {
		t.Fatalf("the dry run differs from %s (run with -update to accept):\n%s", golden, buf.String())
	}
}
