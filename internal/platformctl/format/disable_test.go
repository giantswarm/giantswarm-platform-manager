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
// own entries, the remote base's objects read for the checklist. The hub
// brokers into the installation, so the commit is refused on the pairing.
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
	base := plan.ObjectsIn([]byte("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: agent-platform\n---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: kagent\n---\nkind: OCIRepository\nmetadata:\n  name: agent-platform\n  namespace: flux-giantswarm\n---\nkind: HelmRelease\nmetadata:\n  name: agent-platform\n  namespace: flux-giantswarm\n"))
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
