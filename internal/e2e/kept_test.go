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

	"regexp"

	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/mcpservers"
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

func reconcileDryRun(t *testing.T, c *client.Client, rotate ...string) plan.Installation {
	t.Helper()
	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, reconcileArgs(rotate))
	if isErr {
		t.Fatal(text)
	}
	assertNoValue(t, "the dry run", text)
	return findPlan(t, out, rowan)
}

// reconcileArgs are the arguments of rowan's reconcile, rotating the names
// asked for.
func reconcileArgs(rotate []string) map[string]any {
	args := map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)}
	if len(rotate) > 0 {
		args[tools.ArgRotate] = rotate
	}
	return args
}

// assertUnrequestedRefused holds a reconcile's dry run to the refusal of a
// rotation nobody asked for: every name of forcedBy refused, naming the
// file that forces it, none rotating, the commit refused before any write.
func assertUnrequestedRefused(t *testing.T, st *stack, c *client.Client, p plan.Installation, forcedBy map[string]string) {
	t.Helper()
	for _, g := range p.GeneratedSecrets {
		by, forced := forcedBy[g.Name]
		switch {
		case g.Rotates:
			t.Errorf("%s rotates without a request: %+v", g.Name, g)
		case forced && !strings.Contains(g.Refusal, g.Name+" would rotate, forced by "+by):
			t.Errorf("%s: %+v, want refused, forced by %s", g.Name, g, by)
		case !forced && g.Refusal != "":
			t.Errorf("%s is refused: %q", g.Name, g.Refusal)
		}
	}
	if !strings.Contains(p.CommitRefused, "a reconcile rotates a value only on request") {
		t.Fatalf("commitRefused %q", p.CommitRefused)
	}
	before := len(st.remote.PullRequests())
	if _, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, reconcileArgs(nil)); !isErr || !strings.Contains(text, "would rotate") {
		t.Fatalf("the commit without a request: %v %s", isErr, text)
	}
	if opened := len(st.remote.PullRequests()) - before; opened != 0 {
		t.Fatalf("a refused commit opened %d pull request(s)", opened)
	}
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
func commitReconcile(t *testing.T, st *stack, c *client.Client, p plan.Installation, repository string, rotate ...string) (tools.CommitResult, []string) {
	t.Helper()
	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, reconcileArgs(rotate))
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

// A hold's reason on record — the comment a person wrote above a held
// versionRange in the platform patch — is the comment the reconcile renders,
// line by line, and a hold without one gets the generic comment naming its
// input: the first dry run adds that comment, and the rendered file is the
// fixed point of the next, the reason kept as written.
func TestReconcileKeepsAHoldsReason(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	repo, path, content := onRecord(t, st, "/"+rowan+"/apps/agent-platform/configmap-values.yaml.patch")
	const reason = "    # Held on the running release until the next line lands here as one\n    # window (acme/platform#12); back to the range in that PR.\n"
	held := strings.Replace(content, "  agent-manager:\n    enabled: true\n", "  agent-manager:\n    enabled: true\n"+reason+"    versionRange: \"1.9.2\"\n", 1)
	held = strings.Replace(held, "  kagent:\n    enabled: true\n", "  kagent:\n    enabled: true\n    versionRange: \"1.2.3\"\n", 1)
	if strings.Count(held, "versionRange") != 2 {
		t.Fatalf("the patch on record lacks the agent-manager or the kagent toggle:\n%s", content)
	}
	st.ghs.addFile(repo, path, held)

	p := reconcileDryRun(t, c)
	rendered := fileOf(t, p, repo+":"+path)
	if rendered.Change != plan.ChangeUpdate || !strings.Contains(rendered.Content, reason+"    versionRange: \"1.9.2\"\n") {
		t.Errorf("the reconcile drops the hold's reason (%s):\n%s", rendered.Change, rendered.Content)
	}
	if !strings.Contains(rendered.Content, "    # Held by the input versions.components.kagent: a reconcile keeps it; --input versions.components.kagent=<version> moves it, an empty value lifts it.\n    versionRange: \"1.2.3\"\n") {
		t.Errorf("the hold without a reason lacks the generic comment:\n%s", rendered.Content)
	}
	versions, _ := p.Inputs["versions"].(map[string]any)
	reasons, _ := versions["reasons"].(map[string]any)
	components, _ := reasons["components"].(map[string]any)
	if components["agent-manager"] != "Held on the running release until the next line lands here as one\nwindow (acme/platform#12); back to the range in that PR." || components["kagent"] != "" {
		t.Errorf("the reasons among the inputs: %v", reasons)
	}

	st.ghs.addFile(repo, path, rendered.Content)
	p = reconcileDryRun(t, c)
	if again := fileOf(t, p, repo+":"+path); again.Change != plan.ChangeUnchanged || again.Content != rendered.Content {
		t.Errorf("the rendered file is not the fixed point (%s):\n%s", again.Change, again.Content)
	}
}

// The secrets kustomization on record lists Secrets added by hand beside the
// rendered ones, and lacks one rendered entry: the dry run names the hand
// entries as kept, and the committed reconcile writes the rendered entry back
// with the hand entries after it, so a hand-kept Secret survives every
// reconcile.
func TestReconcileKeepsTheHandAddedSecretsOfTheSecretsKustomization(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	repo, path, content := onRecord(t, st, "/extras/agent-platform/secrets/kustomization.yaml")
	rendered, write := kustomizationResources(t, content)
	hand := []string{"github-oauth-client.yaml", "slack-oauth-client.yaml"}
	st.ghs.addFile(repo, path, write(append(slices.Clone(rendered[1:]), hand...)))

	p := reconcileDryRun(t, c)
	var f plan.File
	for _, pf := range p.Files {
		if pf.Path == path {
			f = pf
		}
	}
	want := []plan.Kept{{List: plan.ListResources, Entry: hand[0]}, {List: plan.ListResources, Entry: hand[1]}}
	if f.Change != plan.ChangeUpdate || !slices.Equal(f.Kept, want) {
		t.Fatalf("the secrets kustomization in the dry run: %s, kept %+v, want %+v", f.Change, f.Kept, want)
	}
	out, written := commitReconcile(t, st, c, p, repo)
	if !slices.Equal(written, []string{path}) {
		t.Fatalf("the pull request writes %v, want %s alone", written, path)
	}
	got, _ := kustomizationResources(t, string(st.remote.Files(repoOf(t, repo), branchPrefix+out.Action.Name+"/"+rowan)[path]))
	if !slices.Equal(got, append(slices.Clone(rendered), hand...)) {
		t.Fatalf("the committed secrets kustomization lists %v, want the render's %v and then %v", got, rendered, hand)
	}
	for _, pr := range st.remote.PullRequests() {
		if pr.Repository.String() == repo && strings.Contains(pr.Body, out.Action.Name) && !strings.Contains(pr.Body, path+": resources["+hand[0]+"], resources["+hand[1]+"]") {
			t.Fatalf("the pull request body does not name the kept entries:\n%s", pr.Body)
		}
	}
}

// The server's credentials file on record changes in its plaintext skeleton
// beyond the keys it lacks (its type): the commit would write it anew
// whole, so every name it holds would rotate, forced by that file with the
// cause. Nobody asked, so the reconcile refuses them and commits nothing.
// Asked for by name, they rotate: the Dex client Secret, the Valkey Secret
// and the revision Secret kept on record share three of them and are
// rewritten with the new values; every other file stays, every other name
// is kept, and the commit writes those four files and nothing else.
func TestReconcileRotatesTheNamesOfAFileWhoseSkeletonChanges(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	s := serverOf(t, reconcileDryRun(t, c))
	repository, path := splitID(t, s.credentials)
	current := st.ghs.repos()[repository][path]
	if !strings.Contains(current, "\ntype: Opaque\n") {
		t.Fatalf("%s on record is not an Opaque Secret:\n%s", s.credentials, current)
	}
	st.ghs.addFile(repository, path, strings.Replace(current, "\ntype: Opaque\n", "\ntype: kubernetes.io/basic-auth\n", 1))

	forcedBy := map[string]string{}
	for _, n := range s.names {
		forcedBy[n] = s.credentials
	}
	assertUnrequestedRefused(t, st, c, reconcileDryRun(t, c), forcedBy)

	p := reconcileDryRun(t, c, s.names...)
	for _, n := range s.names {
		forcedBy[n] = plan.ForcedByRequest
	}
	assertRotation(t, p, forcedBy, s.credentials, s.dexClient, s.valkey, s.revision)
	_, written := commitReconcile(t, st, c, p, repository, s.names...)
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
// is on record, so it is kept, and the new Secret takes it from the
// credentials file by the caller's vault, at the key paths; nothing
// rotates, every other name is kept, and the commit refuses until the carry
// is on record, opening nothing. Asked for the client secret, the rotation
// reaches the rest: the new Secret is written with the new value, the
// credentials file is rewritten with it, so the three other names it holds
// rotate too, forced by the credentials file, and the Valkey Secret sharing
// the password and the revision and the revision Secret sharing the
// revision are rewritten as well.
func TestReconcileRotatesANameANewFileShares(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	s := serverOf(t, reconcileDryRun(t, c))
	repository, path := splitID(t, s.dexClient)
	files := st.ghs.repos()[repository]
	delete(files, path)
	st.ghs.addRepo(repository, files)

	p := reconcileDryRun(t, c)
	clientSecrets := fileOf(t, p, s.dexClient).Generated
	if fileOf(t, p, s.dexClient).Change != plan.ChangeCreate {
		t.Fatalf("the Dex client Secret is on record: %+v", fileOf(t, p, s.dexClient))
	}
	for _, g := range p.GeneratedSecrets {
		switch {
		case g.Rotates || g.Refusal != "":
			t.Errorf("%s rotates without a request: %+v", g.Name, g)
		case slices.Contains(clientSecrets, g.Name):
			if !g.Kept || !slices.Equal(g.FrozenIn, []string{s.credentials}) || len(g.Carries) != 1 || !g.Carries[0].Create ||
				!strings.HasPrefix(g.Carries[0].From, s.credentials+"#stringData.") || g.Carries[0].To != s.dexClient+"#stringData."+render.DexSecretKey {
				t.Errorf("%s: %+v, want kept on record in %s and carried into %s", g.Name, g, s.credentials, s.dexClient)
			}
		case len(g.Carries) != 0:
			t.Errorf("%s is carried: %+v", g.Name, g)
		}
	}
	const carry = "carry the value there with your vault before the commit"
	if !strings.Contains(p.CommitRefused, carry) {
		t.Fatalf("commitRefused %q", p.CommitRefused)
	}
	before := len(st.remote.PullRequests())
	if _, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, reconcileArgs(nil)); !isErr || !strings.Contains(text, carry) {
		t.Fatalf("the commit without the carry: %v %s", isErr, text)
	}
	if opened := len(st.remote.PullRequests()) - before; opened != 0 {
		t.Fatalf("a refused commit opened %d pull request(s)", opened)
	}

	forcedBy := map[string]string{}
	for _, n := range s.names {
		forcedBy[n] = s.credentials
	}
	p = reconcileDryRun(t, c, clientSecrets...)
	for _, g := range clientSecrets {
		forcedBy[g] = plan.ForcedByRequest
	}
	for _, n := range s.names {
		if strings.HasSuffix(n, "-credentials-revision") {
			forcedBy[n] = plan.ForcedByRequest // the revision of a value asked for is drawn with it
		}
	}
	assertRotation(t, p, forcedBy, s.credentials, s.dexClient, s.valkey, s.revision)
	out, written := commitReconcile(t, st, c, p, repository, clientSecrets...)
	if len(written) != 4 || !slices.Contains(written, path) {
		t.Fatalf("the pull request writes %v", written)
	}
	if got := getAction(t, c, out.Action.Name); !slices.Equal(got.Status.Rotated, p.Rotating()) {
		t.Fatalf("get_action: %+v", got.Status)
	}
}

// watchPatch matches the kustomization patch that has Flux watch a
// credentials revision Secret, as the render writes it.
var watchPatch = regexp.MustCompile(`  - patch: \|-\n      apiVersion: v1\n      kind: Secret\n      metadata:\n        name: \S+\n        labels:\n          ` + regexp.QuoteMeta(render.WatchLabel) + `: Enabled\n    target:\n      kind: Secret\n      name: \S+\n`)

// fluxNamespaceLine matches the namespace line of a Secret in the Flux
// namespace, with its indentation.
var fluxNamespaceLine = regexp.MustCompile(`\n( +)namespace: flux-giantswarm\n`)

// An installation enabled before the watch label moved to the
// kustomizations, as gazelle is: its kustomizations lack the watch patch,
// its revision Secrets the label — or, as on the installations reconciled
// while the label lived in the Secret's own file, carry it there. Either
// way the reconcile rotates nothing: every encrypted file stays as it is,
// every generated value is kept, and the commit writes the kustomizations
// alone, in plaintext.
func TestReconcileMovesTheWatchLabelWithoutARotation(t *testing.T) {
	for name, labelInFile := range map[string]bool{"label nowhere (gazelle)": false, "label in the encrypted file": true} {
		t.Run(name, func(t *testing.T) {
			st := newStack(t)
			c := enabledOnRecord(t, st)
			var kustomizations []string
			for repository, files := range st.ghs.repos() {
				for path, content := range files {
					switch {
					case strings.HasSuffix(path, "/kustomization.yaml") && watchPatch.MatchString(content):
						st.ghs.addFile(repository, path, watchPatch.ReplaceAllString(content, ""))
						kustomizations = append(kustomizations, repository+":"+path)
					case labelInFile && strings.HasSuffix(path, "/"+mcpservers.RevisionFile):
						m := fluxNamespaceLine.FindStringSubmatch(content)
						if m == nil {
							t.Fatalf("%s on record has no Flux namespace line:\n%s", path, content)
						}
						st.ghs.addFile(repository, path, strings.Replace(content, m[0], m[0]+m[1]+"labels:\n"+m[1]+m[1]+render.WatchLabel+": Enabled\n", 1))
					}
				}
			}
			if len(kustomizations) < len(mcpservers.Servers) {
				t.Fatalf("the watch patch is in %d kustomization(s): %v", len(kustomizations), kustomizations)
			}
			slices.Sort(kustomizations)

			p := reconcileDryRun(t, c)
			for _, g := range p.GeneratedSecrets {
				if g.Rotates || g.Refusal != "" || len(g.FrozenIn) > 0 && !g.Kept {
					t.Errorf("%s: %+v, want kept", g.Name, g)
				}
			}
			var written []string
			for _, f := range p.Files {
				if f.Change != plan.ChangeUnchanged {
					written = append(written, f.Repository+":"+f.Path)
				}
			}
			slices.Sort(written)
			if !slices.Equal(written, kustomizations) || p.CommitRefused != "" || len(p.Rotating()) != 0 {
				t.Fatalf("files written %v, want %v (commitRefused %q, rotating %v)", written, kustomizations, p.CommitRefused, p.Rotating())
			}
			repository, _ := splitID(t, kustomizations[0])
			out, got := commitReconcile(t, st, c, p, repository)
			var want []string
			for _, id := range kustomizations {
				_, path := splitID(t, id)
				want = append(want, path)
			}
			if !slices.Equal(got, want) || len(out.Action.Status.Rotated) != 0 {
				t.Fatalf("the pull request writes %v, want %v; rotated %v", got, want, out.Action.Status.Rotated)
			}
		})
	}
}
