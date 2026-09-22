package e2e

// mode commit of enable_capability over the invented registry: the gate,
// the Action record, the encrypted files and the pull requests on
// gitops-commit's in-process remote — every GitHub read and every commit as
// alice, no secret value anywhere but inside the encrypted files.

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/giantswarm/gitops-commit/provenance"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

const (
	// modelKeyField is a field no installation asks for — the model key is
	// never supplied at commit, its Secret is the installation's own — so a
	// value for it is refused by field; modelKeyValue is the invented value
	// the scenario hunts for everywhere it must not appear.
	modelKeyField = "kagent.modelKey"
	modelKeyValue = "sk-fixture-model-key-4f9c1e"
	willow        = "willow"
	// suppliedFields are the secret values an installation without a Slack app
	// (rowan in the fixtures) asks for: none — the model key is never
	// supplied and no gateway runs there. A value supplied anyway is refused
	// by field.
	suppliedFields = ""
)

// leakMarkers must appear in no log, pull request, commit or committed file:
// the value, and the placeholders that stood for values. An answer carries
// the plan, whose file contents show the placeholders by design — there the
// value alone is the leak.
var leakMarkers = []string{modelKeyValue, "GENERATED(", "SUPPLIED("}

var actionOf = regexp.MustCompile(`recorded on action (\S+)`)

func commitCall(t *testing.T, c *client.Client, tool string, args map[string]any) (tools.CommitResult, string, bool) {
	t.Helper()
	args[tools.ArgMode] = string(tools.ModeCommit)
	text, isErr := call(t, c, tool, args)
	var out tools.CommitResult
	if !isErr {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decode: %v\n%s", err, text)
		}
	}
	return out, text, isErr
}

// sopsFixtures gives the acme repositories a .sops.yaml with a fresh age
// recipient: what the commit encrypts the secret files for.
func sopsFixtures(t *testing.T, g *fakeGitHub) {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{acmeConfigs, acmeMCs} {
		g.addFile(repo, tools.SopsConfig, "creation_rules:\n  - path_regex: "+sopsPathRegex+"\n    age: "+id.Recipient().String()+"\n")
	}
}

// sopsPathRegex is the fixture repositories' .sops.yaml rule: the fleet's
// shape, a secret file by its path.
const sopsPathRegex = `.*(secret|credential).*`

// isSecretFile is the fixture rule as sopsenc decides it: the path matches,
// and a kustomize entry point is never a secret file.
func isSecretFile(path string) bool {
	return filepath.Base(path) != "kustomization.yaml" && regexp.MustCompile(sopsPathRegex).MatchString(path)
}

// seedRemote gives the in-process remote every fixture repository on its
// default branch, as GitHub would have them.
func seedRemote(t *testing.T, st *stack) {
	t.Helper()
	for repo, files := range st.ghs.repos() {
		tree := map[string][]byte{}
		for p, content := range files {
			tree[p] = []byte(content)
		}
		st.remote.AddBranch(repoOf(t, repo), defaultBranch, tree)
	}
}

func repoOf(t *testing.T, full string) provenance.Repository {
	t.Helper()
	owner, name, ok := strings.Cut(full, "/")
	if !ok {
		t.Fatalf("repository %q", full)
	}
	return provenance.Repository{Owner: owner, Name: name}
}

func assertNoLeak(t *testing.T, where, text string) {
	t.Helper()
	for _, m := range leakMarkers {
		if strings.Contains(text, m) {
			t.Fatalf("%s carries %q", where, m)
		}
	}
}

func assertNoValue(t *testing.T, where, text string) {
	t.Helper()
	if strings.Contains(text, modelKeyValue) {
		t.Fatalf("%s carries the supplied value", where)
	}
}

// listActionsOf answers the actions naming installation, newest first.
func listActionsOf(t *testing.T, c *client.Client, installation string) []actions.Action {
	t.Helper()
	var list tools.ListActionsResult
	if text, isErr := call(t, c, tools.ToolListActions, map[string]any{tools.ArgInstallation: installation}); isErr || json.Unmarshal([]byte(text), &list) != nil {
		t.Fatalf("list_actions: %s", text)
	}
	return list.Actions
}

// The gate: an installation whose repositories the person cannot read is
// refused before any write; the refusal is an Action in state refused;
// nothing reaches the remote.
func TestCommitRefusedUnreadable(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	_, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: "oak", tools.ArgInputs: minimalInputs(nil)})
	if !isErr || !strings.Contains(text, "commit refused") || !strings.Contains(text, tools.SkippedUnreadable) {
		t.Fatalf("refusal: %v %s", isErr, text)
	}
	m := actionOf.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no action in the refusal: %s", text)
	}
	got := listActionsOf(t, c, "oak")
	if len(got) != 1 || got[0].Name != m[1] || got[0].Status.State != actions.StateRefused || got[0].Spec.Actor.Login != alice || got[0].Spec.Kind != actions.KindEnable ||
		got[0].Status.Result == nil || !strings.Contains(got[0].Status.Result.Message, tools.SkippedUnreadable) || len(got[0].Status.PullRequests) != 0 {
		t.Fatalf("recorded action: %+v", got)
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("the remote saw %d pull request(s)", len(prs))
	}
}

// One opted-in installation: the supplied value is demanded by field, the
// Action is created in pending approval, the pull requests open as alice in
// dependency order on branch platform/<action>/<installation> with the
// action id in the body, the secret files land encrypted and the plain files
// as planned — and no secret value or placeholder appears in any answer, log,
// pull request or committed file.
func TestCommitOpensPullRequestsAndPendsApproval(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	inputs := minimalInputs(nil)

	if _, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallations: []any{rowan}, tools.ArgInputs: inputs}); !isErr || !strings.Contains(text, "one installation") {
		t.Fatalf("a set: %v %s", isErr, text)
	}
	if _, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs, tools.ArgSecrets: map[string]any{modelKeyField: modelKeyValue}}); !isErr || !strings.Contains(text, modelKeyField) {
		t.Fatalf("a supplied value nothing asks for: %v %s", isErr, text)
	}
	if got := listActionsOf(t, c, rowan); len(got) != 0 {
		t.Fatalf("a refused argument recorded %d action(s)", len(got))
	}
	dry, dryText, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr || len(dry.Installations) != 1 || strings.Join(dry.Installations[0].SuppliedSecrets, ",") != suppliedFields {
		t.Fatalf("dry run: %s", dryText)
	}
	// The portal's fragment is a kustomize Component: the dry run lists it
	// under components of the portal host's kustomization, everything else
	// under resources of the installation's extras; each is to be added.
	lists := map[string]string{}
	for _, inc := range dry.Installations[0].Includes {
		if inc.Repository != acmeMCs || inc.Change != plan.ChangeUpdate {
			t.Fatalf("include %+v", inc)
		}
		lists[inc.Path+" "+inc.Resource] = inc.List
	}
	if len(lists) < 2 || lists[extrasKustomizationPath(rowan)+" ./agent-platform/"] != plan.ListResources {
		t.Fatalf("includes: %v", lists)
	}

	out, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr {
		t.Fatal(text)
	}
	if out.Caller != alice || out.DryRun || out.Installation != rowan || out.Action == nil || out.Action.Status.State != actions.StatePendingApproval || out.Action.Spec.Actor.Login != alice ||
		out.Action.Spec.Kind != actions.KindEnable || len(out.Action.Status.PullRequests) != 2 || len(out.PullRequests) != 2 || len(out.UnchangedRepositories) != 0 {
		t.Fatalf("answer: %s", text)
	}
	if out.PullRequests[0].Repository != acmeConfigs || out.PullRequests[0].Number != 1 || out.PullRequests[1].Repository != acmeMCs || out.PullRequests[1].Number != 2 ||
		out.PullRequests[0].State != actions.PullRequestOpen || out.PullRequests[0].URL == "" {
		t.Fatalf("pull requests: %+v", out.PullRequests)
	}
	if _, ok := out.Action.Spec.Inputs["secrets"]; ok {
		t.Fatalf("the action's inputs carry %v", out.Action.Spec.Inputs["secrets"])
	}

	branch := "platform/" + out.Action.Name + "/" + rowan
	prs := st.remote.PullRequests()
	if len(prs) != 2 {
		t.Fatalf("the remote has %d pull request(s)", len(prs))
	}
	for i, pr := range prs {
		if pr.Repository.String() != out.PullRequests[i].Repository || pr.Head != branch || pr.Base != defaultBranch || pr.Merged || !strings.Contains(pr.Body, out.Action.Name) || pr.Title != "feat("+rowan+"): enable agent-platform ("+out.Action.Name+")" {
			t.Fatalf("remote pull request %d: %+v", i, pr)
		}
		assertNoLeak(t, "pull request body", pr.Body+pr.Title)
	}
	for _, f := range out.Plan.Files {
		committed := st.remote.Files(repoOf(t, f.Repository), branch)
		content, ok := committed[f.Path]
		if !ok {
			t.Fatalf("%s:%s is not on the branch", f.Repository, f.Path)
		}
		assertNoLeak(t, f.Repository+":"+f.Path, string(content))
		switch {
		case isSecretFile(f.Path):
			if !strings.Contains(string(content), "ENC[") || !strings.Contains(string(content), "sops:") {
				t.Fatalf("%s:%s is not encrypted:\n%s", f.Repository, f.Path, content)
			}
		case string(content) != f.Content:
			t.Fatalf("%s:%s differs from the plan", f.Repository, f.Path)
		}
	}
	// The kustomizations land edited on their nodes: the Component under
	// components, the comment and the owners' entries kept; the extras under
	// resources, never a components list.
	onBranch := st.remote.Files(repoOf(t, acmeMCs), branch)
	if got := string(onBranch[extrasKustomizationPath(rowan)]); !strings.HasPrefix(got, extrasKustomization+"  - ./agent-platform/\n  - ./mcp-") || strings.Contains(got, "components:") {
		t.Fatalf("the extras kustomization on the branch:\n%s", got)
	}
	for _, repo := range []string{acmeConfigs, acmeMCs} {
		commits := st.remote.Commits(repoOf(t, repo), branch)
		if len(commits) != 1 || !strings.Contains(commits[0].Message, out.Action.Name) || len(commits[0].Files) == 0 {
			t.Fatalf("%s: %d commit(s) on the branch: %+v", repo, len(commits), commits)
		}
		assertNoLeak(t, repo+" commit message", commits[0].Message)
	}
	assertNoValue(t, "the dry run", dryText)
	assertNoValue(t, "the commit's answer", text)
	assertNoLeak(t, "the server's log", st.logs.String())
	if !strings.Contains(st.logs.String(), out.Action.Name) {
		t.Fatalf("the log does not name the action:\n%s", st.logs.String())
	}

	var got actions.Action
	if text, isErr := call(t, c, tools.ToolGetAction, map[string]any{tools.ArgName: out.Action.Name}); isErr || json.Unmarshal([]byte(text), &got) != nil || got.Status.State != actions.StatePendingApproval || len(got.Status.PullRequests) != 2 {
		t.Fatalf("get_action: %s", text)
	}
	li, _, _ := listInstallations(t, c, map[string]any{tools.ArgInstallations: []any{rowan}})
	if r := find(t, li, rowan); r.Capabilities[0].State != installations.StatePendingApproval || r.Capabilities[0].LastAction == nil || r.Capabilities[0].LastAction.Name != out.Action.Name {
		t.Fatalf("after the commit: %+v", r.Capabilities[0])
	}
	if out.Plan.Diff[plan.ChangeUpdate] != 1 || out.Plan.Diff[plan.ChangeCreate] != len(out.Plan.Files)-1 {
		t.Fatalf("plan diff %v", out.Plan.Diff)
	}
}

// A repository without .sops.yaml: nothing is written blind — its rules
// decide which files are secret files, so the commit is refused naming the
// file before anything is recorded: no Action, no pull request.
func TestCommitFailsWithoutSopsConfig(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	_, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if !isErr || !strings.Contains(text, tools.SopsConfig) || !strings.Contains(text, acmeConfigs) {
		t.Fatalf("without .sops.yaml: %v %s", isErr, text)
	}
	if got := listActionsOf(t, c, rowan); len(got) != 0 {
		t.Fatalf("recorded action: %+v", got)
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("the remote saw %d pull request(s)", len(prs))
	}
}
