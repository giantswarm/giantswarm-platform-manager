package e2e

// The dex-app prerequisite over the invented registry: every Dex client the
// definition renders is a referenced Secret, which dex-app takes next to a
// hand-made inline one from 3.2.2; the record says which dex-app an
// installation runs, and a commit onto an older one is held while the
// comparison still runs.

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// modelServingKey is the one choice's key in the inputs.
const modelServingKey = "modelServing"

// dexAppRefusal is the plan's refusal for an installation whose record says
// it runs version, read from source.
func dexAppRefusal(name, version, source string) string {
	return "dex-app " + version + " on record (" + source + "): the referenced Dex client secrets need dex-app " + plan.DexAppReferencedSecrets + " or later; pin it in " + installations.CollectionsKustomizationPath(name) + " first"
}

// The record reads the dex-app from the installation's own pin where its
// collections kustomization patches the App, else from the fleet's base at
// the ref the kustomization names, and the fact reaches the definition's
// inputs; an installation without the kustomization has none.
func TestListInstallationsReadsTheDexAppOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{rowan, alder, willow}})
	if isErr {
		t.Fatal(text)
	}
	if r := find(t, out, rowan); r.Record == nil || r.Record.DexAppVersion != platformDexApp || r.Record.DexAppSource != acmeMCs+":"+installations.CollectionsKustomizationPath(rowan) || len(r.Errors) != 0 {
		t.Fatalf("rowan pins its own: %+v %v", r.Record, r.Errors)
	}
	if r := find(t, out, alder); r.Record == nil || r.Record.DexAppVersion != fleetDexApp || r.Record.DexAppSource != basesRepo+":"+installations.DexAppBasePath || len(r.Errors) != 0 {
		t.Fatalf("alder runs the fleet's base: %+v %v", r.Record, r.Errors)
	}
	if r := find(t, out, willow); r.Record == nil || r.Record.DexAppVersion != "" || r.Record.DexAppSource != "" {
		t.Fatalf("willow has no collections kustomization on record: %+v", r.Record)
	}
	res := verifyRowan(t, c, rowan)
	if inst, _ := res.Inputs.Values[argInstallation].(map[string]any); inst["dexAppVersion"] != platformDexApp {
		t.Fatalf("the fact reaches the inputs: %v", res.Inputs.Values[argInstallation])
	}
}

// An installation enabled by hand on a dex-app before the referenced
// secrets: the comparison runs and shows what a commit would change, and
// says the commit would be refused, naming the version, the file it was read
// from and the kustomization to pin dex-app in; the dry run says the same;
// commit mode refuses with it before any write — no pull request, no
// action. Pinned to the version that takes them, the same commit goes ahead.
func TestCommitHeldByTheDexAppOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	c := st.mcpClient(t, aliceToken)
	birchByHand(t, st, c, false)
	seedRemote(t, st)
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(birch), collectionsKustomization(fleetDexApp))
	serving := map[string]any{modelServingKey: map[string]any{enabledKey: true}}
	want := dexAppRefusal(birch, fleetDexApp, acmeMCs+":"+installations.CollectionsKustomizationPath(birch))

	res := verifyWith(t, c, birch, serving)
	if res.Refused != "" || res.CommitRefused != want {
		t.Fatalf("refused %q, commitRefused %q, want %q", res.Refused, res.CommitRefused, want)
	}
	if len(inputDifferences(res, modelServingKey+"."+enabledKey)) == 0 || res.Summary[verify.DiffersByInput] == 0 || res.Diff[plan.ChangeUpdate] == 0 || len(res.DexClients) == 0 {
		t.Fatalf("the comparison still runs: summary %v diff %v clients %d", res.Summary, res.Diff, len(res.DexClients))
	}
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: birch, tools.ArgInputs: serving})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, out, birch); p.Refused != "" || p.CommitRefused != want || p.Diff[plan.ChangeUpdate] == 0 {
		t.Fatalf("the dry run: refused %q, commitRefused %q, diff %v", p.Refused, p.CommitRefused, p.Diff)
	}

	_, text, isErr = commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: birch, tools.ArgInputs: serving})
	if !isErr || !strings.Contains(text, want) || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("commit mode: %v %s", isErr, text)
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("the remote saw %d pull request(s)", len(prs))
	}
	if got := listActionsOf(t, c, birch); len(got) != 0 {
		t.Fatalf("the refusal recorded %d action(s)", len(got))
	}
	_, text, isErr = commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{birch}, tools.ArgInputs: serving})
	if !isErr || !strings.Contains(text, want) || len(st.remote.PullRequests()) != 0 {
		t.Fatalf("a wave: %v %s", isErr, text)
	}

	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(birch), collectionsKustomization(platformDexApp))
	if res := verifyWith(t, c, birch, serving); res.CommitRefused != "" {
		t.Fatalf("pinned %s: %q", platformDexApp, res.CommitRefused)
	}
	committed, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: birch, tools.ArgInputs: serving})
	if isErr || committed.Action == nil || len(committed.PullRequests) == 0 || len(st.remote.PullRequests()) != len(committed.PullRequests) {
		t.Fatalf("pinned %s, the commit goes ahead: %v %s", platformDexApp, isErr, text)
	}
}
