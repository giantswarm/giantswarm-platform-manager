package e2e

// disable_capability over the invented registry: the dry run of an
// installation the platform rolled out on, the commit's pull requests on
// gitops-commit's in-process remote, the approval and the merge as for an
// enable, and the capability not enabled afterwards — every read and every
// commit as alice.

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render/clustermcpservers"
)

func disableCall(t *testing.T, c *client.Client, args map[string]any) (tools.DisableResult, string, bool) {
	t.Helper()
	text, isErr := call(t, c, tools.ToolDisableCapability, args)
	var out tools.DisableResult
	if !isErr {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decode: %v\n%s", err, text)
		}
	}
	return out, text, isErr
}

// The dry run of the platform on rowan names the marker and every file of
// the definition's directories as deletions — a hand-written one too, its
// Dex client out of the patch, or kept on request —, the extras' include as
// the one edit, and the objects the Kustomization leaves as the checklist; it
// writes nothing. The commit opens one pull request per repository as
// alice, the deletions removed from the branch's tree; once approved and
// merged, the next read finds the marker gone and the action disabled, and
// list_installations and verify_capability read the platform not enabled.
func TestDisableCommitsTheRemovalAndReadsNotEnabled(t *testing.T) {
	st := newStack(t)
	enabled, aliceC := rolledOut(t, st)
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	if !st.ghs.has(acmeConfigs, marker) {
		t.Fatal("the platform is not on record after its merge")
	}
	prsBefore := len(st.remote.PullRequests())

	// The MCP servers stay under cluster-mcp-servers, whose Dex clients need
	// a dex-app that restarts Dex on a rotation: on the platform's own pin
	// nothing of the removal can be named.
	if _, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgDryRun: true}); !isErr || !strings.Contains(text, "nothing is removed") {
		t.Fatalf("a dry run on dex-app %s: %v %s", platformDexApp, isErr, text)
	}
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(rowan), collectionsKustomization(clustermcpservers.DexAppRotation))

	// The capability's kustomization pulls the platform from the fleet's
	// remote base: while it cannot be read, the dry run refuses naming it,
	// and the checklist is never silently short of its objects.
	if _, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgDryRun: true}); !isErr || !strings.Contains(text, platformBase) || !strings.Contains(text, "nothing is removed blind") {
		t.Fatalf("a dry run without the remote base: %v %s", isErr, text)
	}
	st.ghs.addRepo(fleetBasesRepo, platformBaseFiles)

	// What a person added under the capability's directory: an MCP client's
	// ConfigMap and its Dex client's Secret in a hand-written kustomization,
	// the client in the installation's Dex patch.
	ap := "management-clusters/" + rowan + "/extras/agent-platform/"
	dexPatch := "installations/" + rowan + "/apps/dex-app/configmap-values.yaml.patch"
	addHandWritten(t, st, ap, dexPatch)
	if _, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgDryRun: true, tools.ArgKeep: []any{ap + "mcpclients/absent.yaml"}}); !isErr || !strings.Contains(text, "nothing is removed") {
		t.Fatalf("a dry run keeping a file not on record: %v %s", isErr, text)
	}
	kept, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgDryRun: true, tools.ArgKeep: []any{ap + "mcpclients/gateway.yaml"}})
	if isErr || len(kept.Plan.Kept) != 1 || kept.Plan.Kept[0].Path != ap+"mcpclients/gateway.yaml" || kept.Plan.Removes(acmeMCs, ap+"mcpclients/gateway.yaml") || kept.Plan.Removes(acmeMCs, ap+"mcpclients/kustomization.yaml") {
		t.Fatalf("a dry run keeping the ConfigMap: %s", text)
	}

	dry, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgDryRun: true})
	if isErr || !dry.DryRun || dry.State != installations.StateEnabled || dry.CommitRefused != "" || len(dry.References) != 0 {
		t.Fatalf("dry run: %s", text)
	}
	if len(st.remote.PullRequests()) != prsBefore {
		t.Fatal("the dry run opened a pull request")
	}
	deleted := map[string]bool{}
	var edited []string
	for _, f := range dry.Plan.Files {
		switch f.Change {
		case plan.ChangeDelete:
			deleted[f.Repository+":"+f.Path] = true
		case plan.ChangeUpdate:
			edited = append(edited, f.Repository+":"+f.Path)
		default:
			t.Fatalf("file %+v", f)
		}
	}
	if !deleted[acmeConfigs+":"+marker] || !deleted[acmeMCs+":management-clusters/"+rowan+"/extras/agent-platform/kustomization.yaml"] {
		t.Fatalf("deletions: %v", deleted)
	}
	if !slices.Contains(edited, acmeMCs+":"+extrasKustomizationPath(rowan)) {
		t.Fatalf("edits: %v", edited)
	}
	for _, p := range []string{"mcpclients/kustomization.yaml", "mcpclients/gateway.yaml", "mcpclients/dex-client-gateway.yaml"} {
		if !deleted[acmeMCs+":"+ap+p] {
			t.Errorf("the hand-written %s stays: deletions %v", p, deleted)
		}
	}
	if i := slices.IndexFunc(dry.Plan.Files, func(f plan.Removal) bool { return f.Path == dexPatch }); i < 0 || strings.Contains(dry.Plan.Files[i].Content, "dex-client-gateway") || !slices.ContainsFunc(dry.Plan.Files[i].Drops, func(d string) bool { return strings.HasPrefix(d, "extraStaticClients[gateway]") }) {
		t.Fatalf("the hand-registered Dex client stays in the patch: %+v", dry.Plan.Files)
	}
	if len(dry.Plan.PullRequests) != 2 {
		t.Fatalf("pull requests %+v", dry.Plan.PullRequests)
	}
	assertBaseChecklist(t, dry.Plan.Checklist)

	if _, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgMode: string(tools.ModeCommit)}); !isErr || !strings.Contains(text, tools.ArgReason) {
		t.Fatalf("a commit without a reason: %v %s", isErr, text)
	}
	out, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgMode: string(tools.ModeCommit), tools.ArgReason: "the platform moves to another hub"})
	if isErr || out.DryRun || out.Action == nil || out.Action.Spec.Kind != actions.KindDisable || out.Action.Status.State != actions.StatePendingApproval || len(out.PullRequests) != 2 || len(out.Action.Status.Orphans) != len(dry.Plan.Checklist) {
		t.Fatalf("commit: %s", text)
	}
	name := out.Action.Name
	branch := branchPrefix + name + "/" + rowan
	for _, pr := range out.PullRequests {
		tree := st.remote.Files(repoOf(t, pr.Repository), branch)
		for key := range deleted {
			if repo, p, _ := strings.Cut(key, ":"); repo == pr.Repository {
				if _, ok := tree[p]; ok {
					t.Errorf("%s is still on the branch", key)
				}
			}
		}
	}
	for _, pr := range st.remote.PullRequests()[prsBefore:] {
		if pr.Title != "feat("+rowan+"): disable agent-platform ("+name+")" || !strings.Contains(pr.Body, "does not prune") {
			t.Fatalf("pull request %q:\n%s", pr.Title, pr.Body)
		}
	}

	if _, text, isErr := decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: name}); isErr {
		t.Fatalf("approve: %s", text)
	}
	for _, pr := range st.remote.PullRequests()[prsBefore:] {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	if m, text, isErr := mergeCall(t, aliceC, name); isErr || m.Action == nil || len(m.Merged) != 2 {
		t.Fatalf("merge: %v %s", isErr, text)
	}
	if st.ghs.has(acmeConfigs, marker) {
		t.Fatal("the marker survived the merge")
	}

	got := getAction(t, aliceC, name)
	if got.Status.State != actions.StateDisabled || stageOf(&got, rowan).State != actions.StateDisabled {
		t.Fatalf("after the merge: %+v", got.Status)
	}
	if len(got.Status.Orphans) != len(dry.Plan.Checklist) {
		t.Fatalf("orphans %+v, checklist %+v", got.Status.Orphans, dry.Plan.Checklist)
	}
	for i, o := range got.Status.Orphans {
		if c := dry.Plan.Checklist[i]; o != (actions.Orphan{Installation: rowan, Kind: c.Kind, Namespace: c.Namespace, Name: c.Name, Takes: c.Takes}) {
			t.Fatalf("orphan %d: %+v, the checklist has %+v", i, o, c)
		}
	}
	li, _, _ := listInstallations(t, aliceC, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StateNotEnabled {
		t.Fatalf("list_installations: %+v", r.Capabilities[0])
	}
	if text, isErr := call(t, aliceC, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform}); isErr || !strings.Contains(text, string(installations.StateNotEnabled)) {
		t.Fatalf("verify_capability: %v %s", isErr, text)
	}
	if _, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgDryRun: true}); isErr || !strings.Contains(text, "nothing to disable") {
		t.Fatalf("a second dry run: %v %s", isErr, text)
	}
	_ = enabled
	assertNoLeak(t, "the server's log", st.logs.String())
}

// addHandWritten puts what a person adds beside the agent platform into
// rowan's trees: under the definition's directory ap a kustomization of an
// MCP client's ConfigMap and its Dex client's Secret, listed in the
// directory's kustomization, and the client in the Dex patch dexPatch.
func addHandWritten(t *testing.T, st *stack, ap, dexPatch string) {
	t.Helper()
	k, ok := st.ghs.file(acmeMCs, ap+"kustomization.yaml")
	if !ok {
		t.Fatalf("%s is not on record", ap+"kustomization.yaml")
	}
	st.ghs.addFile(acmeMCs, ap+"kustomization.yaml", k+"  - ./mcpclients\n")
	st.ghs.addFile(acmeMCs, ap+"mcpclients/kustomization.yaml", "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - gateway.yaml\n  - dex-client-gateway.yaml\n")
	st.ghs.addFile(acmeMCs, ap+"mcpclients/gateway.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: gateway-callbacks\n  namespace: giantswarm\n")
	st.ghs.addFile(acmeMCs, ap+"mcpclients/dex-client-gateway.yaml", "apiVersion: v1\nkind: Secret\nmetadata:\n  name: dex-client-gateway\n  namespace: giantswarm\n")
	dex, ok := st.ghs.file(acmeConfigs, dexPatch)
	if !ok || !strings.Contains(dex, "  extraStaticClients:\n") {
		t.Fatalf("rowan's Dex patch lists no extraStaticClients:\n%s", dex)
	}
	st.ghs.addFile(acmeConfigs, dexPatch, strings.Replace(dex, "  extraStaticClients:\n", "  extraStaticClients:\n    - id: gateway\n      name: MCP gateway\n      secretRef:\n        name: dex-client-gateway\n        key: secret\n", 1))
}

// fleetBasesRepo is the fleet's bases repository the definition's
// kustomizations pull the platform from; platformBase the remote base the
// agent platform's kustomization lists, platformBaseFiles what it carries.
const (
	fleetBasesRepo = "giantswarm/management-cluster-bases"
	platformBase   = "https://github.com/" + fleetBasesRepo + "//extras/agent-platform?ref=main"
)

var platformBaseFiles = map[string]string{
	"extras/agent-platform/kustomization.yaml":  "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./namespace.yaml\n  - ./oci-repository.yaml\n  - ./helm-release.yaml\n  - ./konfiguration.yaml\n",
	"extras/agent-platform/namespace.yaml":      "apiVersion: v1\nkind: Namespace\nmetadata:\n  name: agent-platform\n---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: kagent\n",
	"extras/agent-platform/oci-repository.yaml": "apiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: agent-platform\n  namespace: flux-giantswarm\n",
	"extras/agent-platform/helm-release.yaml":   "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: agent-platform\n  namespace: flux-giantswarm\nspec:\n  targetNamespace: agent-platform\n",
	"extras/agent-platform/konfiguration.yaml":  "apiVersion: konfigure.giantswarm.io/v1alpha1\nkind: Konfiguration\nmetadata:\n  name: agent-platform-konfiguration\n  namespace: flux-giantswarm\n",
}

// assertBaseChecklist holds a checklist to the remote base's objects in
// deletion order ahead of the Secrets and ConfigMaps: the umbrella
// HelmRelease, its source and the Konfiguration, the namespaces, each line
// saying what its deletion takes with it.
func assertBaseChecklist(t *testing.T, checklist []plan.Object) {
	t.Helper()
	want := []string{"HelmRelease flux-giantswarm/agent-platform", "OCIRepository flux-giantswarm/agent-platform", "Konfiguration flux-giantswarm/agent-platform-konfiguration", "Namespace agent-platform", "Namespace kagent"}
	if len(checklist) <= len(want) {
		t.Fatalf("checklist %v", checklist)
	}
	for i, w := range want {
		if checklist[i].String() != w || checklist[i].Takes == "" {
			t.Fatalf("checklist line %d: %q, want %q with what it takes", i+1, checklist[i].Line(), w)
		}
	}
	ahead := map[string]bool{}
	for _, w := range want {
		ahead[strings.Fields(w)[0]] = true
	}
	for _, o := range checklist[len(want):] {
		if ahead[o.Kind] {
			t.Fatalf("after the namespaces: %s", o)
		}
	}
}
