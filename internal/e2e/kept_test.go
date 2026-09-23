package e2e

// A reconcile of an installation the manager enabled: the encrypted files on
// record are kept with their values as long as the render changes nothing
// outside them, and a generated name rotates only when a file of the name has
// to be written — the dry run naming the file that forces it.

import (
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// enabledOnRecord enables rowan through a commit and puts the pull requests'
// files on the record, the way a merge would: an installation the manager
// enabled, the encrypted files as sopsenc wrote them.
func enabledOnRecord(t *testing.T, st *stack) *client.Client {
	t.Helper()
	out, c := commitRowan(t, st)
	branch := branchPrefix + out.Action.Name + "/" + rowan
	for _, pr := range out.PullRequests {
		for path, content := range st.remote.Files(repoOf(t, pr.Repository), branch) {
			st.ghs.addFile(pr.Repository, path, string(content))
		}
	}
	return c
}

// branchPrefix is where every action's pull requests live.
const branchPrefix = "platform/"

func reconcileDryRun(t *testing.T, c *client.Client) plan.Installation {
	t.Helper()
	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	assertNoValue(t, "the dry run", text)
	return findPlan(t, out, rowan)
}

// changedFiles are the paths a branch of the remote carries changed or added
// against the default branch: what the pull request writes.
func changedFiles(t *testing.T, st *stack, repository, branch string) []string {
	t.Helper()
	base := st.remote.Files(repoOf(t, repository), defaultBranch)
	var out []string
	for path, content := range st.remote.Files(repoOf(t, repository), branch) {
		if was, ok := base[path]; !ok || string(was) != string(content) {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return out
}

// server are one MCP server's files on record, as "<repository>:<path>":
// its credentials file holding four names, the Dex client Secret sharing
// the client secret with it, the Valkey Secret sharing the password and the
// credentials revision, the revision Secret sharing the revision.
type mcpServer struct {
	credentials, dexClient, valkey, revision string
	names                                    []string // held by the credentials file
}

func serverOf(t *testing.T, p plan.Installation) mcpServer {
	t.Helper()
	for _, g := range p.GeneratedSecrets {
		if !strings.HasSuffix(g.Name, "-dex-client-secret") || len(g.Files) != 2 {
			continue
		}
		var s mcpServer
		for i, f := range g.Files {
			if strings.HasSuffix(f, "/"+credentialsFile) {
				s.credentials, s.dexClient = f, g.Files[1-i]
			}
		}
		if s.credentials == "" {
			continue
		}
		s.names = fileOf(t, p, s.credentials).Generated
		for _, other := range p.GeneratedSecrets {
			if other.Name == g.Name || !slices.Contains(s.names, other.Name) {
				continue
			}
			switch len(other.Files) {
			case 2: // the password: the credentials file and the Valkey Secret
				s.valkey = other.Files[0]
				if s.valkey == s.credentials {
					s.valkey = other.Files[1]
				}
			case 3: // the revision: both of those and the revision Secret
				for _, f := range other.Files {
					if f != s.credentials && !strings.HasSuffix(f, "/valkey-credentials.enc.yaml") {
						s.revision = f
					}
				}
			}
		}
		if len(s.names) == 4 && s.valkey != "" && s.revision != "" {
			return s
		}
	}
	t.Fatalf("no server holds four names in a %s, two shared with its Valkey Secret and one with its revision Secret: %+v", credentialsFile, p.GeneratedSecrets)
	return mcpServer{}
}

func fileOf(t *testing.T, p plan.Installation, id string) plan.File {
	t.Helper()
	for _, f := range p.Files {
		if f.Repository+":"+f.Path == id {
			return f
		}
	}
	t.Fatalf("%s is not in the plan", id)
	return plan.File{}
}

func splitID(t *testing.T, id string) (repository, path string) {
	t.Helper()
	repository, path, ok := strings.Cut(id, ":")
	if !ok {
		t.Fatalf("file id %q", id)
	}
	return repository, path
}

// assertRotation holds the dry run to the rotation expected: the names
// rotating with the file that forced each, every other name on record kept,
// and exactly the files named written.
func assertRotation(t *testing.T, p plan.Installation, forcedBy map[string]string, written ...string) {
	t.Helper()
	for _, g := range p.GeneratedSecrets {
		by, rotates := forcedBy[g.Name]
		if g.Rotates != rotates || g.ForcedBy != by || g.Kept == rotates || g.Refusal != "" || len(g.FrozenIn) == 0 {
			t.Errorf("%s: %+v, want rotates %v forced by %q", g.Name, g, rotates, by)
		}
	}
	var got []string
	for _, f := range p.Files {
		if f.Change != plan.ChangeUnchanged {
			got = append(got, f.Repository+":"+f.Path)
		}
	}
	slices.Sort(written)
	if !slices.Equal(got, written) || p.CommitRefused != "" {
		t.Fatalf("files written %v, want %v (commitRefused %q)", got, written, p.CommitRefused)
	}
	if got := p.Rotating(); !slices.Equal(got, keysOf(forcedBy)) {
		t.Fatalf("rotating %v, want %v", got, keysOf(forcedBy))
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// commitReconcile commits the reconcile of rowan and answers the files its
// one pull request writes, all encrypted where the record is, none leaking.
func commitReconcile(t *testing.T, st *stack, c *client.Client, p plan.Installation, repository string) (tools.CommitResult, []string) {
	t.Helper()
	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || out.Action == nil || len(out.PullRequests) != 1 || out.PullRequests[0].Repository != repository || !slices.Equal(out.Action.Status.Rotated, p.Rotating()) {
		t.Fatalf("the reconcile's commit: %s", text)
	}
	branch := branchPrefix + out.Action.Name + "/" + rowan
	written := changedFiles(t, st, repository, branch)
	for path, content := range st.remote.Files(repoOf(t, repository), branch) {
		if slices.Contains(written, path) && isSecretFile(path) && !strings.Contains(string(content), "ENC[") {
			t.Fatalf("%s on the branch is not encrypted:\n%s", path, content)
		}
		assertNoLeak(t, path, string(content))
	}
	for _, pr := range st.remote.PullRequests() {
		if pr.Repository.String() == repository {
			assertNoLeak(t, "the pull request body", pr.Body)
		}
	}
	assertNoLeak(t, "the server's log", st.logs.String())
	return out, written
}

// Nothing changed since the enablement: every file is unchanged — the
// encrypted ones kept with their values, every generated name kept, none
// rotating — and a commit has nothing to do. A change to one plain file is
// the one file the pull request writes: no secret file is rewritten, no
// value rotates, and the pull request says the names are kept.
func TestReconcileKeepsTheEncryptedFilesOnRecord(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	p := reconcileDryRun(t, c)
	if len(p.Files) == 0 || p.Diff[plan.ChangeUnchanged] != len(p.Files) {
		t.Fatalf("an enabled installation's reconcile: %v\n%+v", p.Diff, p.Files)
	}
	secrets := 0
	for _, f := range p.Files {
		if isSecretFile(f.Path) {
			secrets++
		}
	}
	if secrets == 0 || len(p.GeneratedSecrets) == 0 {
		t.Fatalf("the fixture has %d secret files and %d generated names", secrets, len(p.GeneratedSecrets))
	}
	for _, g := range p.GeneratedSecrets {
		if !g.Kept || g.Rotates || g.ForcedBy != "" || g.Refusal != "" || len(g.FrozenIn) != len(g.Files) {
			t.Fatalf("%s after the enablement: %+v", g.Name, g)
		}
	}
	// A kept file names the literals the record holds encrypted, with the
	// render's value — the kagent UI's client id beside its generated secret;
	// a file of generated values alone names none.
	var unseen []plan.Unseen
	for _, f := range p.Files {
		switch {
		case strings.HasSuffix(f.Path, "/secrets/kagent-oauth2-proxy-credentials.yaml"):
			unseen = f.Unseen
		case strings.HasSuffix(f.Path, "/secrets/muster-valkey-credentials.yaml") && len(f.Unseen) != 0:
			t.Errorf("%s holds generated values only: %+v", f.Path, f.Unseen)
		}
	}
	if !slices.Equal(unseen, []plan.Unseen{{Path: "stringData.client-id", Value: "kagent"}}) {
		t.Fatalf("the kagent credentials' unseen literals: %+v", unseen)
	}
	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr || out.Action != nil || len(out.PullRequests) != 0 || !strings.Contains(out.Next, "nothing to commit") {
		t.Fatalf("a commit with nothing changed: %s", text)
	}

	// The plain file: the platform patch, whose audience lists carry nothing
	// of the installation's own here, so the plan writes the render's bytes.
	var drifted plan.File
	for _, f := range p.Files {
		if strings.HasSuffix(f.Path, "/apps/agent-platform/configmap-values.yaml.patch") {
			drifted = f
			break
		}
	}
	if drifted.Path == "" || drifted.Content == "" {
		t.Fatalf("no platform patch with content in %+v", p.Files)
	}
	st.ghs.addFile(drifted.Repository, drifted.Path, drifted.Content+"# edited by hand\n")
	p = reconcileDryRun(t, c)
	if p.Diff[plan.ChangeUpdate] != 1 || p.Diff[plan.ChangeUnchanged] != len(p.Files)-1 || len(p.Rotating()) != 0 {
		t.Fatalf("one plain file drifted: %v, rotating %v", p.Diff, p.Rotating())
	}
	out, written := commitReconcile(t, st, c, p, drifted.Repository)
	if !slices.Equal(written, []string{drifted.Path}) || len(out.Action.Status.Rotated) != 0 {
		t.Fatalf("the pull request writes %v, rotated %v", written, out.Action.Status.Rotated)
	}
	var body string
	for _, pr := range st.remote.PullRequests() {
		if pr.Repository.String() == drifted.Repository && strings.Contains(pr.Body, out.Action.Name) {
			body = pr.Body
		}
	}
	if !strings.Contains(body, "kept: the value on record in ") || strings.Contains(body, "rotated") {
		t.Fatalf("the pull request body:\n%s", body)
	}
}

// The render adds a field to an encrypted file's template — on record the
// server's credentials file lacks it: the file has to be written, so every
// name it holds rotates, forced by that file; the Dex client Secret, the
// Valkey Secret and the revision Secret kept on record share three of them
// and are rewritten with the new values. Every other file stays, every other
// name is kept. The commit writes those four files and nothing else.
func TestReconcileRotatesTheNamesOfAFileWhoseSkeletonChanges(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	s := serverOf(t, reconcileDryRun(t, c))
	repository, path := splitID(t, s.credentials)
	var without []string
	dropped := false
	for _, line := range strings.Split(st.ghs.repos()[repository][path], "\n") {
		if !dropped && strings.Contains(line, ": ENC[") {
			dropped = true
			continue
		}
		without = append(without, line)
	}
	if !dropped {
		t.Fatalf("%s on record holds no encrypted value", s.credentials)
	}
	st.ghs.addFile(repository, path, strings.Join(without, "\n"))

	p := reconcileDryRun(t, c)
	forcedBy := map[string]string{}
	for _, n := range s.names {
		forcedBy[n] = s.credentials
	}
	assertRotation(t, p, forcedBy, s.credentials, s.dexClient, s.valkey, s.revision)
	_, written := commitReconcile(t, st, c, p, repository)
	want := []string{path}
	for _, id := range []string{s.dexClient, s.valkey, s.revision} {
		_, p := splitID(t, id)
		want = append(want, p)
	}
	slices.Sort(want)
	if !slices.Equal(written, want) {
		t.Fatalf("the pull request writes %v, want %v", written, want)
	}
}

// A new file shares a generated name with a file on record — the server's
// Dex client Secret is absent, its credentials file kept: the client secret
// rotates, forced by the new file; the credentials file is rewritten with it,
// so the three other names it holds rotate too, forced by the credentials
// file, and the Valkey Secret sharing the password and the revision and the
// revision Secret sharing the revision are rewritten as well.
func TestReconcileRotatesANameANewFileShares(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	s := serverOf(t, reconcileDryRun(t, c))
	repository, path := splitID(t, s.dexClient)
	files := st.ghs.repos()[repository]
	delete(files, path)
	st.ghs.addRepo(repository, files)

	p := reconcileDryRun(t, c)
	forcedBy := map[string]string{}
	for _, n := range s.names {
		forcedBy[n] = s.credentials
	}
	for _, g := range fileOf(t, p, s.dexClient).Generated {
		forcedBy[g] = s.dexClient
	}
	if fileOf(t, p, s.dexClient).Change != plan.ChangeCreate {
		t.Fatalf("the Dex client Secret is on record: %+v", fileOf(t, p, s.dexClient))
	}
	assertRotation(t, p, forcedBy, s.credentials, s.dexClient, s.valkey, s.revision)
	out, written := commitReconcile(t, st, c, p, repository)
	if len(written) != 4 || !slices.Contains(written, path) {
		t.Fatalf("the pull request writes %v", written)
	}
	if got := getAction(t, c, out.Action.Name); !slices.Equal(got.Status.Rotated, p.Rotating()) {
		t.Fatalf("get_action: %+v", got.Status)
	}
}
