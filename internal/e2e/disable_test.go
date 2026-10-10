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
// the definition's directories as deletions, the extras' include as the
// one edit, and the objects the Kustomization leaves as the checklist; it
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
	if len(dry.Plan.Checklist) == 0 || len(dry.Plan.PullRequests) != 2 {
		t.Fatalf("checklist %v, pull requests %+v", dry.Plan.Checklist, dry.Plan.PullRequests)
	}

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
	if got.Status.State != actions.StateDisabled || stageOf(&got, rowan).State != actions.StateDisabled || len(got.Status.Orphans) == 0 {
		t.Fatalf("after the merge: %+v", got.Status)
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
