package e2e

// A rotation on request: reconcile_capability's rotate names generated values
// as the plan lists them; the dry run lists each rotating on request with its
// credentials revision, the commit rewrites every file that freezes them and
// the review names them. An unknown name is refused before anything is
// written, for one installation and for a wave, where a name applies to the
// installation whose plan lists it.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// chartLineFact is the installation fact that selects the meta chart's line.
const chartLineFact = "chartLine"

// fourLineInputs type rowan onto the 4 line, where muster's credentials carry
// their revision: the record's gates with it.
func fourLineInputs() map[string]any {
	return map[string]any{argInstallation: map[string]any{chartLineFact: "4", "podCertificateRequest": true}}
}

// enabledOnTheFourLine enables rowan on the 4 line through a commit and puts
// the pull requests' files on the record, the way a merge would.
func enabledOnTheFourLine(t *testing.T, st *stack) *client.Client {
	t.Helper()
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: fourLineInputs()})
	if isErr || out.Action == nil {
		t.Fatalf("enable on the 4 line: %s", text)
	}
	branch := branchPrefix + out.Action.Name + "/" + rowan
	for _, pr := range out.PullRequests {
		for path, content := range st.remote.Files(repoOf(t, pr.Repository), branch) {
			st.ghs.addFile(pr.Repository, path, string(content))
		}
	}
	return c
}

// rotateArgs are rowan's reconcile on the 4 line asking to rotate names.
func rotateArgs(names ...string) map[string]any {
	return map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: fourLineInputs(), tools.ArgRotate: names}
}

// The platform's secrets directory on record, and muster's files in it.
const (
	musterSecrets     = "management-clusters/" + rowan + "/extras/agent-platform/secrets/"
	musterValkeyFile  = musterSecrets + "muster-valkey-credentials.yaml"   // #nosec G101 -- a file name, not a value
	musterOAuthFile   = musterSecrets + "muster-oauth-credentials.yaml"    // #nosec G101 -- a file name, not a value
	musterRevisionYml = musterSecrets + "muster-credentials-revision.yaml" // #nosec G101 -- a file name, not a value
	musterDexClient   = musterSecrets + "dex-client-muster-secret.yaml"    // #nosec G101 -- a file name, not a value
)

// rowan enabled on the 4 line, every file on record: a reconcile asking to
// rotate muster's Valkey password lists it rotating on request, frozen in
// the Valkey Secret, and muster's credentials revision with it; the OAuth
// credentials file holds the revision, so it is rewritten and its values
// rotate forced by it, down to the Dex client Secret; nothing else rotates,
// and exactly those four files update. The commit writes them, encrypted,
// and nothing else; the Action records the name asked for and every value
// rotated; the review and the change name the rotation on request.
func TestReconcileRotatesMustersValkeyPasswordOnRequest(t *testing.T) {
	st := newStack(t)
	c := enabledOnTheFourLine(t, st)
	valkeyPassword, revision := rowan+"-muster-valkey-password", rowan+"-muster-credentials-revision" // #nosec G101 -- generated value names, not values

	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, rotateArgs())
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, dry, rowan); len(p.Rotating()) != 0 || p.Diff[plan.ChangeUnchanged] != len(p.Files) {
		t.Fatalf("the enabled installation without a request: rotating %v, diff %v", p.Rotating(), p.Diff)
	}
	dry, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, rotateArgs(valkeyPassword))
	if isErr {
		t.Fatal(text)
	}
	assertNoValue(t, "the dry run", text)
	p := findPlan(t, dry, rowan)
	mcs := fileOf(t, p, acmeMCs+":"+musterValkeyFile).Repository
	id := func(path string) string { return mcs + ":" + path }
	if g := generatedIn(t, p, valkeyPassword); !g.Rotates || g.ForcedBy != plan.ForcedByRequest || !slices.Equal(g.FrozenIn, []string{id(musterValkeyFile)}) {
		t.Fatalf("the value asked for: %+v", g)
	}
	if g := generatedIn(t, p, revision); !g.Rotates || g.ForcedBy != plan.ForcedByRequest || len(g.FrozenIn) != 3 {
		t.Fatalf("its revision: %+v", g)
	}
	for _, name := range []string{"-muster-dex-client-secret", "-muster-registration-token", "-muster-oauth-encryption-key"} {
		if g := generatedIn(t, p, rowan+name); !g.Rotates || g.ForcedBy != id(musterOAuthFile) {
			t.Errorf("%s: %+v, want rotating forced by %s", g.Name, g, musterOAuthFile)
		}
	}
	for _, g := range p.GeneratedSecrets {
		if !strings.Contains(g.Name, "-muster-") && (g.Rotates || !g.Kept) {
			t.Errorf("%s rotates with muster's Valkey password: %+v", g.Name, g)
		}
	}
	written := []string{id(musterDexClient), id(musterRevisionYml), id(musterOAuthFile), id(musterValkeyFile)}
	slices.Sort(written)
	var got []string
	for _, f := range p.Files {
		if f.Change != plan.ChangeUnchanged {
			if f.Change != plan.ChangeUpdate {
				t.Errorf("%s:%s is %s", f.Repository, f.Path, f.Change)
			}
			got = append(got, f.Repository+":"+f.Path)
		}
	}
	if !slices.Equal(got, written) || p.CommitRefused != "" {
		t.Fatalf("files written %v, want %v (commitRefused %q)", got, written, p.CommitRefused)
	}

	args := rotateArgs(valkeyPassword)
	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args)
	if isErr || out.Action == nil || len(out.PullRequests) != 1 || out.PullRequests[0].Repository != mcs {
		t.Fatalf("the commit: %s", text)
	}
	a := out.Action
	if !slices.Equal(a.Spec.Rotate, []string{valkeyPassword}) || !slices.Equal(a.Status.Rotated, p.Rotating()) || !slices.Contains(a.Status.Rotated, revision) ||
		!strings.Contains(a.Spec.Change, "rotates on request "+revision+", "+valkeyPassword+" (") {
		t.Fatalf("the action: rotate %v, rotated %v, change %q", a.Spec.Rotate, a.Status.Rotated, a.Spec.Change)
	}
	branch := branchPrefix + a.Name + "/" + rowan
	changed := changedFiles(t, st, mcs, branch)
	want := []string{musterDexClient, musterRevisionYml, musterOAuthFile, musterValkeyFile}
	slices.Sort(want)
	if !slices.Equal(changed, want) {
		t.Fatalf("the pull request writes %v, want %v", changed, want)
	}
	for path, content := range st.remote.Files(repoOf(t, mcs), branch) {
		if slices.Contains(changed, path) && !strings.Contains(string(content), "ENC[") {
			t.Fatalf("%s on the branch is not encrypted:\n%s", path, content)
		}
		assertNoLeak(t, path, string(content))
	}
	for _, pr := range st.remote.PullRequests() {
		if pr.Repository.String() == mcs && strings.Contains(pr.Body, a.Name) {
			if !strings.Contains(pr.Body, valkeyPassword+" (alphanumeric, 32) — rotated on request: a new value replaces the one on record in "+id(musterValkeyFile)) {
				t.Errorf("the pull request body:\n%s", pr.Body)
			}
			assertNoLeak(t, "the pull request body", pr.Body)
		}
	}
	if review := fmt.Sprint(st.gateway.posted()); !strings.Contains(review, "rotates on request: "+valkeyPassword+".") {
		t.Errorf("the review does not name the rotation on request: %s", review)
	}
	assertNoLeak(t, "the server's log", st.logs.String())
}

// A name no plan lists is refused before anything is written, naming it: for
// one installation (the dry run and the commit alike), and for a wave, where
// a name applies to the installation whose plan lists it and changes nothing
// of the others'. No action is recorded, no pull request opened.
func TestRotateRefusesAnUnknownName(t *testing.T) {
	st := newStack(t)
	c := enabledOnTheFourLine(t, st)
	const typo = rowan + "-muster-valkey-pasword"
	before := len(listActionsOf(t, c, rowan))
	prs := len(st.remote.PullRequests())
	for _, mode := range []string{"dry run", "commit"} {
		args := rotateArgs(rowan+"-muster-valkey-password", typo)
		var text string
		var isErr bool
		if mode == "dry run" {
			_, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, args)
		} else {
			_, text, isErr = commitCall(t, c, tools.ToolReconcileCapability, args)
		}
		if !isErr || !strings.Contains(text, tools.ArgRotate+" names "+typo+", which the plan of "+rowan+" does not list") || strings.Contains(text, rowan+"-muster-valkey-password,") {
			t.Fatalf("%s: %v %s", mode, isErr, text)
		}
	}

	// The wave: birch is on record, and its plan lists no name of rowan's.
	wave := func(extra map[string]any, names ...string) map[string]any {
		args := map[string]any{tools.ArgInstallations: []string{birch, rowan}, tools.ArgInputs: fourLineInputs(), tools.ArgRotate: names}
		for k, v := range extra {
			args[k] = v
		}
		return args
	}
	text, isErr := call(t, c, tools.ToolReconcileCapability, wave(map[string]any{tools.ArgMode: string(tools.ModeCommit)}, birch+"-nothing"))
	if !isErr || !strings.Contains(text, tools.ArgRotate+" names "+birch+"-nothing, which no plan of the set lists") {
		t.Fatalf("the wave: %v %s", isErr, text)
	}
	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, wave(nil, rowan+"-muster-valkey-password"))
	if isErr {
		t.Fatal(text)
	}
	if g := generatedIn(t, findPlan(t, dry, rowan), rowan+"-muster-valkey-password"); !g.Rotates || g.ForcedBy != plan.ForcedByRequest {
		t.Fatalf("rowan's value in the wave: %+v", g)
	}
	for _, g := range findPlan(t, dry, birch).GeneratedSecrets {
		if g.ForcedBy == plan.ForcedByRequest {
			t.Fatalf("birch's %s rotates on rowan's request: %+v", g.Name, g)
		}
	}
	if n := len(listActionsOf(t, c, rowan)); n != before {
		t.Fatalf("%d action(s) recorded by the refusals", n-before)
	}
	if n := len(st.remote.PullRequests()); n != prs {
		t.Fatalf("%d pull request(s) opened by the refusals", n-prs)
	}
}
