package e2e

// A generated value frozen in an encrypted file on record: the plan finds it
// before any write, the dry run names it, the commit rotates it into every
// file of the name — and a commit that fails after opening pull requests
// closes them itself, deny_action closing what it could not.

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// credentialsFile is the server's OAuth credentials Secret of the fleet's
// shape: the file an installation set up by hand carries encrypted already.
const credentialsFile = "oauth-credentials.enc.yaml" // #nosec G101 -- a file name, not a value

// generatedIn finds the generated secret of the plan called name.
func generatedIn(t *testing.T, p plan.Installation, name string) plan.GeneratedSecret {
	t.Helper()
	for _, g := range p.GeneratedSecrets {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("%s is not a generated secret of the plan: %+v", name, p.GeneratedSecrets)
	return plan.GeneratedSecret{}
}

// A server's credentials file exists on record and its Dex client Secret is
// new, both holding the client secret: the dry run lists the name as frozen
// in the credentials file and rotating, a commit would not be refused; the
// commit draws a new value into both — the credentials file rewritten,
// encrypted, the client Secret created — records the rotation on the Action
// by name and says so in the pull request; the other values the rewritten
// file holds rotate with it. No secret value leaves the encrypted files.
func TestCommitRotatesAGeneratedValueFrozenOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	c := st.mcpClient(t, aliceToken)
	inputs := minimalInputs(nil)
	bare, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr {
		t.Fatal(text)
	}
	var shared plan.GeneratedSecret
	var frozenFile string
	for _, g := range bare.Installations[0].GeneratedSecrets {
		if len(g.FrozenIn) != 0 || g.Rotates || g.Refusal != "" {
			t.Fatalf("nothing is on record and %+v", g)
		}
		for _, f := range g.Files {
			if shared.Name == "" && strings.HasSuffix(g.Name, "-dex-client-secret") && len(g.Files) == 2 && strings.HasSuffix(f, "/"+credentialsFile) {
				shared, frozenFile = g, f
			}
		}
	}
	if shared.Name == "" {
		t.Fatalf("no Dex client secret is shared by a %s and a second file: %+v", credentialsFile, bare.Installations[0].GeneratedSecrets)
	}
	repo, path, _ := strings.Cut(frozenFile, ":")
	onRecord := "sops:\n    age: []\n    version: 3.9.0\n"
	st.ghs.addFile(repo, path, onRecord)
	seedRemote(t, st)

	dry, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr {
		t.Fatal(text)
	}
	p := dry.Installations[0].Installation
	g := generatedIn(t, p, shared.Name)
	if !g.Rotates || g.Refusal != "" || !slices.Equal(g.FrozenIn, []string{frozenFile}) || !slices.Equal(g.Files, shared.Files) || p.CommitRefused != "" {
		t.Fatalf("the frozen value in the dry run: %+v (commitRefused %q)", g, p.CommitRefused)
	}
	for _, other := range p.GeneratedSecrets {
		if slices.Contains(other.Files, frozenFile) && !other.Rotates {
			t.Fatalf("%s is held by the rewritten file and does not rotate: %+v", other.Name, other)
		}
	}
	assertNoValue(t, "the dry run", text)

	out, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr {
		t.Fatal(text)
	}
	if out.Action == nil || out.Action.Status.State != actions.StatePendingApproval || len(out.PullRequests) != 2 || !slices.Contains(out.Action.Status.Rotated, shared.Name) ||
		!slices.Equal(out.Action.Status.Rotated, p.Rotating()) || !strings.Contains(out.Action.Spec.Change, "rotates "+strings.Join(p.Rotating(), ", ")) {
		t.Fatalf("answer: rotated %v, plan rotating %v, change %q", out.Action.Status.Rotated, p.Rotating(), out.Action.Spec.Change)
	}
	branch := "platform/" + out.Action.Name + "/" + rowan
	committed := st.remote.Files(repoOf(t, repo), branch)
	for _, f := range shared.Files {
		_, fpath, _ := strings.Cut(f, ":")
		content, ok := committed[fpath]
		if !ok || !strings.Contains(string(content), "ENC[") || string(content) == onRecord {
			t.Fatalf("%s on the branch: %v\n%s", f, ok, content)
		}
		assertNoLeak(t, f, string(content))
	}
	var body string
	for _, pr := range st.remote.PullRequests() {
		if pr.Repository.String() == repo {
			body = pr.Body
		}
	}
	if !strings.Contains(body, shared.Name+" (") || !strings.Contains(body, "rotated: a new value replaces the one on record in "+frozenFile) {
		t.Fatalf("the pull request body:\n%s", body)
	}
	assertNoLeak(t, "the pull request body", body)
	if got := getAction(t, c, out.Action.Name); !slices.Equal(got.Status.Rotated, out.Action.Status.Rotated) {
		t.Fatalf("get_action: %+v", got.Status)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// seedRemoteExcept seeds the remote with every fixture repository but the
// ones named: their pull requests cannot be opened — the failure after the
// first pull request.
func seedRemoteExcept(t *testing.T, st *stack, except ...string) {
	t.Helper()
	for repo, files := range st.ghs.repos() {
		if slices.Contains(except, repo) {
			continue
		}
		tree := map[string][]byte{}
		for p, content := range files {
			tree[p] = []byte(content)
		}
		st.remote.AddBranch(repoOf(t, repo), defaultBranch, tree)
	}
}

// The second repository refuses the branch after the first pull request is
// open: the commit closes that pull request itself, with its branch, records
// it closed and the reason on the failed Action, and the answer says so.
func TestCommitFailureClosesItsPullRequests(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	seedRemoteExcept(t, st, acmeConfigs)
	c := st.mcpClient(t, aliceToken)
	_, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if !isErr || !strings.Contains(text, acmeConfigs) || !strings.Contains(text, "has no branch") || !strings.Contains(text, "its 1 pull request(s) closed ("+acmeMCs+"#1)") {
		t.Fatalf("the failure: %v %s", isErr, text)
	}
	if calls := st.calls(); !slices.Equal(calls, []string{alice + " " + commit.OpClose + " " + acmeMCs + "#1"}) {
		t.Fatalf("remote calls %v", calls)
	}
	prs := st.remote.PullRequests()
	if len(prs) != 1 || !prs[0].Closed || prs[0].Merged {
		t.Fatalf("the remote's pull requests: %+v", prs)
	}
	if files := st.remote.Files(repoOf(t, acmeConfigs), prs[0].Head); len(files) != 0 {
		t.Fatalf("the branch %s stands after the close", prs[0].Head)
	}
	got := listActionsOf(t, c, rowan)
	if len(got) != 1 || got[0].Status.State != actions.StateFailed || len(got[0].Status.PullRequests) != 1 || got[0].Status.PullRequests[0].State != actions.PullRequestClosed ||
		got[0].Status.Result == nil || !strings.Contains(got[0].Status.Result.Message, "has no branch") || !strings.Contains(got[0].Status.Result.Message, "closed") {
		t.Fatalf("the failed action: %+v", got)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// The remote refuses the close as well: the pull request stays open on the
// record, the answer names deny_action — which takes a failed action, closes
// what is open as the member, records the reason and leaves the action
// failed; a second denial is refused, approve and merge stay refused.
func TestDenyClosesAFailedActionsOpenPullRequests(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	seedRemoteExcept(t, st, acmeConfigs)
	st.remote.Fail[commit.OpClose] = errors.New("the close was refused")
	c := st.mcpClient(t, aliceToken)
	_, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if !isErr || !strings.Contains(text, "1 could not be closed and stay open") || !strings.Contains(text, "the close was refused") || !strings.Contains(text, tools.ToolDenyAction) {
		t.Fatalf("the failure: %v %s", isErr, text)
	}
	failed := listActionsOf(t, c, rowan)[0]
	if failed.Status.State != actions.StateFailed || failed.Status.PullRequests[0].State != actions.PullRequestOpen || !strings.Contains(failed.Status.Result.Message, "stay open") {
		t.Fatalf("the failed action: %+v", failed.Status)
	}
	if prs := st.remote.PullRequests(); len(prs) != 1 || prs[0].Closed {
		t.Fatalf("the remote's pull requests: %+v", prs)
	}
	for _, tool := range []string{tools.ToolApproveAction, tools.ToolMergeAction} {
		if text, isErr := call(t, c, tool, map[string]any{tools.ArgAction: failed.Name}); !isErr || !strings.Contains(text, actions.StateFailed) {
			t.Fatalf("%s on a failed action: %v %s", tool, isErr, text)
		}
	}

	delete(st.remote.Fail, commit.OpClose)
	d, text, isErr := decide(t, c, tools.ToolDenyAction, map[string]any{tools.ArgAction: failed.Name, tools.ArgReason: "retry once the repository is reachable"})
	if isErr || d.Action.Status.State != actions.StateFailed || d.Action.Status.PullRequests[0].State != actions.PullRequestClosed || !strings.Contains(d.Message, "1 pull request(s) closed") {
		t.Fatalf("deny: %v %s", isErr, text)
	}
	if prs := st.remote.PullRequests(); len(prs) != 1 || !prs[0].Closed {
		t.Fatalf("the remote's pull requests after the denial: %+v", prs)
	}
	got := getAction(t, c, failed.Name)
	if got.Status.State != actions.StateFailed || got.Status.Approval == nil || got.Status.Approval.Decision != actions.DecisionDenied || got.Status.Approval.DecidedBy != alice ||
		got.Status.Approval.Reason != "retry once the repository is reachable" || !strings.Contains(got.Status.Result.Message, "retry once the repository is reachable") || got.Status.Result.State != actions.StateFailed {
		t.Fatalf("after the denial: %+v", got.Status)
	}
	if _, text, isErr := decide(t, c, tools.ToolDenyAction, map[string]any{tools.ArgAction: failed.Name, tools.ArgReason: "again"}); !isErr || !strings.Contains(text, "already denied by "+alice) {
		t.Fatalf("a second denial: %v %s", isErr, text)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}
