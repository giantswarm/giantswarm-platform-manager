package e2e

// A confidential MCP client registered on an installation the platform rolled
// out on — a hosted MCP gateway configured with a Dex client id and secret —
// over the invented registry: the reconcile's dry run renders the Dex
// client, its Secret and muster's audience from the registration ConfigMap,
// adopts a client registered by hand without drawing its secret anew, and
// orders the Secret's pull request before the patch that references it; the
// disable takes all three out.

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render/clustermcpservers"
)

// The confidential client the fixtures register on rowan.
const (
	gatewayID       = "gateway"
	gatewayCallback = "https://gateway.runtime.test/oauth2/callback"
	gatewaySecret   = "dex-client-" + gatewayID
)

// registerConfidentialOnRowan lists a confidential registration ConfigMap
// under rowan's extras/agent-platform/mcpclients/. byHand registers the
// client the way an installation carries one before the manager renders it:
// its Secret listed beside the ConfigMap, encrypted as the platform's own
// Dex client Secrets are, the client in the Dex patch with its secretRef and
// its id among muster's trusted audiences.
func registerConfidentialOnRowan(t *testing.T, st *stack, byHand bool) {
	t.Helper()
	ap := "management-clusters/" + rowan + "/extras/agent-platform/"
	k, ok := st.ghs.file(acmeMCs, ap+"kustomization.yaml")
	if !ok {
		t.Fatalf("%s is not on record", ap+"kustomization.yaml")
	}
	resources := "  - gateway.yaml\n"
	files := map[string]string{
		ap + "kustomization.yaml":      k + "  - ./mcpclients\n",
		ap + "mcpclients/gateway.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + gatewayID + "\n  namespace: agent-platform\ndata:\n  clientId: " + gatewayID + "\n  redirectURIs: |\n    " + gatewayCallback + "\n",
	}
	if byHand {
		muster, ok := st.ghs.file(acmeMCs, ap+"secrets/dex-client-muster-secret.yaml")
		if !ok {
			t.Fatal("muster's Dex client Secret is not on record")
		}
		files[ap+"mcpclients/"+gatewaySecret+".yaml"] = strings.ReplaceAll(muster, "dex-client-muster", gatewaySecret)
		resources += "  - " + gatewaySecret + ".yaml\n"
		dexPatch := "installations/" + rowan + "/apps/dex-app/configmap-values.yaml.patch"
		dex, _ := st.ghs.file(acmeConfigs, dexPatch)
		if !strings.Contains(dex, "  extraStaticClients:\n") {
			t.Fatalf("rowan's Dex patch lists no extraStaticClients:\n%s", dex)
		}
		st.ghs.addFile(acmeConfigs, dexPatch, strings.Replace(dex, "  extraStaticClients:\n", "  extraStaticClients:\n    - id: "+gatewayID+"\n      name: MCP gateway\n      redirectURIs:\n        - "+gatewayCallback+"\n      secretRef:\n        name: "+gatewaySecret+"\n        key: secret\n", 1))
		platformPatch := "installations/" + rowan + "/apps/agent-platform/configmap-values.yaml.patch"
		patch, _ := st.ghs.file(acmeConfigs, platformPatch)
		st.ghs.addFile(acmeConfigs, platformPatch, withListEntry(t, patch, "trustedAudiences:", gatewayID))
	}
	files[ap+"mcpclients/kustomization.yaml"] = "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n" + resources
	st.ghs.addFiles(acmeMCs, files)
}

// withListEntry is content with item appended to the block list under the
// line key ends, at the list's indentation.
func withListEntry(t *testing.T, content, key, item string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if !strings.HasSuffix(l, key) || i+1 >= len(lines) {
			continue
		}
		indent := lines[i+1][:len(lines[i+1])-len(strings.TrimLeft(lines[i+1], " "))]
		end := i + 1
		for end < len(lines) && strings.HasPrefix(lines[end], indent+"- ") {
			end++
		}
		return strings.Join(slices.Insert(lines, end, indent+"- "+item), "\n")
	}
	t.Fatalf("no %q in\n%s", key, content)
	return ""
}

// reconcileRowanDryRun is rowan's reconcile dry run: the result and rowan's plan.
func reconcileRowanDryRun(t *testing.T, st *stack) (tools.CapabilityResult, plan.Installation) {
	t.Helper()
	out, text, isErr := dryRun(t, st.mcpClient(t, aliceToken), tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" {
		t.Fatalf("refused: %s", p.Refused)
	}
	return out, p
}

func planFile(p plan.Installation, suffix string) (plan.File, bool) {
	for _, f := range p.Files {
		if strings.HasSuffix(f.Path, suffix) {
			return f, true
		}
	}
	return plan.File{}, false
}

// A confidential registration ConfigMap renders the Dex client with its
// secretRef, the client's Secret in Dex's namespace with a value the commit
// draws, and the client's id among muster's audiences, and no public
// registration; the pull request with the Secret merges before the one with
// the patch that references it.
func TestReconcileRendersAConfidentialClient(t *testing.T) {
	st := newStack(t)
	rolledOut(t, st)
	registerConfidentialOnRowan(t, st, false)
	out, p := reconcileRowanDryRun(t, st)

	dex, ok := planFile(p, "/apps/dex-app/configmap-values.yaml.patch")
	if !ok || strings.Count(dex.Content, "- id: "+gatewayID+"\n") != 1 || !strings.Contains(dex.Content, "name: "+gatewaySecret+"\n") || !strings.Contains(dex.Content, gatewayCallback) {
		t.Fatalf("the Dex patch:\n%s", dex.Content)
	}
	secret, ok := planFile(p, "/extras/agent-platform/secrets/"+gatewaySecret+"-secret.yaml")
	if !ok || secret.Change != plan.ChangeCreate || !strings.Contains(secret.Content, "namespace: giantswarm\n") {
		t.Fatalf("the client's Secret: %+v", secret)
	}
	g := generatedIn(t, p, rowan+"-"+gatewayID+"-dex-client-secret")
	if len(g.FrozenIn) != 0 || g.Rotates || g.Refusal != "" {
		t.Errorf("the client's secret: %+v", g)
	}
	patch, _ := planFile(p, "/apps/agent-platform/configmap-values.yaml.patch")
	if !strings.Contains(patch.Content, "- "+gatewayID+"\n") || strings.Contains(patch.Content, gatewayCallback) {
		t.Errorf("the platform patch:\n%s", patch.Content)
	}
	var configs *plan.PullRequest
	for i := range out.PullRequests {
		if out.PullRequests[i].Repository == acmeConfigs {
			configs = &out.PullRequests[i]
		}
	}
	if configs == nil || !slices.ContainsFunc(configs.After, func(d plan.Dependency) bool {
		return d.Repository == acmeMCs && slices.Contains(d.Objects, "Secret/"+gatewaySecret)
	}) {
		t.Errorf("the configs pull request does not merge after the Secret: %+v", out.PullRequests)
	}
}

// An installation that carries the client by hand — its Secret under
// mcpclients/, its Dex entry and its audience kept — is adopted by its
// registration ConfigMap: the Secret is the render's in its file, its value
// on record kept and nothing drawn anew, the Dex entry and the audience are
// the rendered ones, once each, nothing of them kept beside the render.
func TestReconcileAdoptsAHandRegisteredConfidentialClient(t *testing.T) {
	st := newStack(t)
	rolledOut(t, st)
	registerConfidentialOnRowan(t, st, true)
	_, p := reconcileRowanDryRun(t, st)

	ap := "management-clusters/" + rowan + "/extras/agent-platform/"
	g := generatedIn(t, p, rowan+"-"+gatewayID+"-dex-client-secret")
	if g.Rotates || g.Refusal != "" || !g.Kept || !slices.Equal(g.FrozenIn, []string{acmeMCs + ":" + ap + "mcpclients/" + gatewaySecret + ".yaml"}) || len(g.Carries) != 0 {
		t.Fatalf("the adopted secret: %+v", g)
	}
	if slices.Contains(p.Rotating(), g.Name) || strings.Contains(p.FrozenRefusal(), gatewayID) || strings.Contains(p.CarryRefusal(), gatewayID) {
		t.Errorf("rotating %v, frozen refusal %q, carry refusal %q", p.Rotating(), p.FrozenRefusal(), p.CarryRefusal())
	}
	if f, ok := planFile(p, "/extras/agent-platform/secrets/"+gatewaySecret+"-secret.yaml"); ok {
		t.Errorf("a second Secret for the adopted client: %+v", f)
	}
	if f, ok := planFile(p, "/mcpclients/"+gatewaySecret+".yaml"); ok && (f.Change != plan.ChangeUnchanged || f.Rewritten) {
		t.Errorf("the adopted Secret's file: %s rewritten %v", f.Change, f.Rewritten)
	}
	dex, _ := planFile(p, "/apps/dex-app/configmap-values.yaml.patch")
	if strings.Count(dex.Content, "- id: "+gatewayID+"\n") != 1 || strings.Contains(dex.Content, "MCP gateway") || slices.ContainsFunc(dex.Kept, func(k plan.Kept) bool { return k.Entry == gatewayID }) {
		t.Errorf("the Dex patch, kept %v:\n%s", dex.Kept, dex.Content)
	}
	patch, _ := planFile(p, "/apps/agent-platform/configmap-values.yaml.patch")
	if strings.Count(patch.Content, "- "+gatewayID+"\n") != 1 || slices.ContainsFunc(patch.Kept, func(k plan.Kept) bool { return k.Entry == gatewayID }) {
		t.Errorf("the platform patch, kept %v:\n%s", patch.Kept, patch.Content)
	}
}

// The disable of the platform takes the confidential client out with it: the
// Secret's file and the registration are deleted, the Dex patch loses the
// client, and the platform patch with muster's audience goes whole.
func TestDisableRemovesAConfidentialClient(t *testing.T) {
	st := newStack(t)
	_, aliceC := rolledOut(t, st)
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(rowan), collectionsKustomization(clustermcpservers.DexAppRotation))
	st.ghs.addRepo(fleetBasesRepo, platformBaseFiles)
	registerConfidentialOnRowan(t, st, true)

	dry, text, isErr := disableCall(t, aliceC, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgDryRun: true})
	if isErr {
		t.Fatalf("dry run: %s", text)
	}
	ap := "management-clusters/" + rowan + "/extras/agent-platform/"
	for _, p := range []string{ap + "mcpclients/" + gatewaySecret + ".yaml", ap + "mcpclients/gateway.yaml"} {
		if !dry.Plan.Removes(acmeMCs, p) {
			t.Errorf("%s stays", p)
		}
	}
	if !dry.Plan.Removes(acmeConfigs, "installations/"+rowan+"/apps/agent-platform/configmap-values.yaml.patch") {
		t.Error("the platform patch with muster's audience stays")
	}
	dexPatch := "installations/" + rowan + "/apps/dex-app/configmap-values.yaml.patch"
	if i := slices.IndexFunc(dry.Plan.Files, func(f plan.Removal) bool { return f.Path == dexPatch }); i < 0 || strings.Contains(dry.Plan.Files[i].Content, gatewaySecret) {
		t.Fatalf("the confidential client stays in the Dex patch: %+v", dry.Plan.Files)
	}
}
