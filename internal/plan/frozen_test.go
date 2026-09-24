package plan

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The files of one server, as "<repository>:<path>", and the names held.
const (
	credentials  = "acme/mcs:extras/server/oauth-credentials.enc.yaml"  // #nosec G101 -- a file path, not a value
	valkey       = "acme/mcs:extras/server/valkey-credentials.enc.yaml" // #nosec G101 -- a file path, not a value
	dexClient    = "acme/mcs:extras/server/dex-client-server.yaml"
	revisionFile = "acme/mcs:extras/server/credentials-revision.enc.yaml" // #nosec G101 -- a file path, not a value
	patch        = "acme/configs:installations/x/apps/dex-app/configmap-values.yaml.patch"
	client       = "client"
	key          = "key"
	password     = "valkey"
	revision     = "revision"
	pair         = "pair"
)

func generatedOf(names ...string) map[string]*GeneratedSecret {
	out := map[string]*GeneratedSecret{}
	for _, n := range names {
		out[n] = &GeneratedSecret{Name: n}
	}
	return out
}

func rotating(t *testing.T, g map[string]*GeneratedSecret, name, forcedBy string, frozenIn ...string) {
	t.Helper()
	if gs := g[name]; !gs.Rotates || gs.Kept || gs.Refusal != "" || gs.ForcedBy != forcedBy || !slices.Equal(gs.FrozenIn, frozenIn) {
		t.Fatalf("%s: %+v, want rotating, forced by %s, frozen in %v", name, gs, forcedBy, frozenIn)
	}
}

func kept(t *testing.T, g map[string]*GeneratedSecret, name string, frozenIn ...string) {
	t.Helper()
	if gs := g[name]; gs.Rotates || !gs.Kept || gs.Refusal != "" || gs.ForcedBy != "" || !slices.Equal(gs.FrozenIn, frozenIn) {
		t.Fatalf("%s: %+v, want kept, frozen in %v", name, gs, frozenIn)
	}
}

// The server's credentials file and its Valkey Secret are on record as the
// render has them and the Dex client Secret is new: the client secret
// rotates into both, forced by the new file; the credentials file being
// rewritten, the encryption key it alone holds rotates too, and so does the
// valkey password it shares with the Valkey Secret — that file is rewritten
// as well, both forced by the credentials file. Nothing is refused.
func TestFrozenRotatesThroughTheRewrittenFiles(t *testing.T) {
	g := generatedOf(client, key, password)
	rewrite := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, password}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}},
	}, nil)
	rotating(t, g, client, dexClient, credentials)
	rotating(t, g, key, credentials, credentials)
	rotating(t, g, password, credentials, credentials, valkey)
	if len(rewrite) != 2 || !rewrite[credentials] || !rewrite[valkey] {
		t.Fatalf("rewritten files %v", rewrite)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[key], *g[password]}}
	if rotated := p.Rotated(); len(rotated) != 2 || !rotated[credentials] || !rotated[valkey] {
		t.Fatalf("rotated files %v", rotated)
	}
	if got := p.Rotating(); !slices.Equal(got, []string{client, key, password}) || p.FrozenRefusal() != "" {
		t.Fatalf("rotating %v, refusal %q", got, p.FrozenRefusal())
	}
}

// Every file is on record as the render has it outside the values: no file
// is written, every name is kept — listed as frozen so the reader knows the
// values on record stand. An unreadable file takes no part.
func TestFrozenKeepsTheValuesWhenNoFileIsWritten(t *testing.T) {
	g := generatedOf(client, password, "other")
	rewrite := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, password}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password}},
		{file: "acme/mcs:extras/other.yaml", change: ChangeUnknown, secret: []string{"other"}},
	}, nil)
	kept(t, g, client, dexClient, credentials)
	kept(t, g, password, credentials, valkey)
	if gs := g["other"]; gs.Rotates || gs.Kept || len(gs.FrozenIn) != 0 {
		t.Fatalf("other: %+v", gs)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[password]}}
	if len(rewrite) != 0 || len(p.Rotated()) != 0 || len(p.Rotating()) != 0 {
		t.Fatalf("rotation without a file to write: %v %v %v", rewrite, p.Rotated(), p.Rotating())
	}
}

// The render changes the credentials file's plaintext skeleton (a field
// added): the file is written anew, so every name it holds rotates, forced
// by the file itself; the Dex client Secret kept on record shares the client
// secret and is rewritten with it. The Valkey Secret shares nothing with it
// and keeps its password.
func TestFrozenARewrittenSkeletonForcesItsNames(t *testing.T) {
	g := generatedOf(client, key, password)
	rewrite := frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, key}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password}},
	}, nil)
	rotating(t, g, client, credentials, dexClient, credentials)
	rotating(t, g, key, credentials, credentials)
	kept(t, g, password, valkey)
	if len(rewrite) != 2 || !rewrite[credentials] || !rewrite[dexClient] {
		t.Fatalf("rewritten files %v", rewrite)
	}
}

// A key pair's public half in a plain file to write needs the pair; the
// private half frozen in the secret file on record makes the pair rotate,
// forced by the plain file. The other way round, a rotation of the pair
// rewrites the plain file kept on record with the public half.
func TestFrozenPublicHalfNeedsThePair(t *testing.T) {
	const keys, configmap = "acme/mcs:portal/plugin-keys-secret.enc.yaml", "acme/mcs:portal/configmap.yaml"
	g := generatedOf(pair)
	rewrite := frozen(g, []holder{
		{file: keys, change: ChangeUnchanged, secret: []string{pair}},
		{file: configmap, change: ChangeUpdate, public: []string{pair}},
	}, nil)
	rotating(t, g, pair, configmap, keys)
	if len(rewrite) != 2 || !rewrite[keys] || !rewrite[configmap] {
		t.Fatalf("rewritten files %v", rewrite)
	}
	g = generatedOf(pair)
	rewrite = frozen(g, []holder{
		{file: keys, change: ChangeUpdate, secret: []string{pair}},
		{file: configmap, change: ChangeUnchanged, public: []string{pair}},
	}, nil)
	rotating(t, g, pair, keys, keys)
	if len(rewrite) != 2 || !rewrite[configmap] {
		t.Fatalf("the plain file kept with the public half is not rewritten: %v", rewrite)
	}
}

// The server's credentials revision is held by its credentials file, its
// Valkey Secret and the revision Secret beside the HelmReleases. A rewrite of
// the Valkey Secret alone (its skeleton changed; a chart reading the
// password from that Secret shares nothing else with the credentials file)
// rotates the password and the revision, and the revision carries the
// rotation on: the credentials file and the revision Secret are rewritten,
// the credentials file's other names rotate with it, down to the Dex client
// Secret — so the HelmReleases read a new revision and both pods roll.
func TestFrozenRevisionCouplesTheFilesOfAServer(t *testing.T) {
	g := generatedOf(client, key, password, revision)
	rewrite := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, revision}},
		{file: valkey, change: ChangeUpdate, secret: []string{password, revision}},
		{file: revisionFile, change: ChangeUnchanged, secret: []string{revision}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
	}, nil)
	rotating(t, g, password, valkey, valkey)
	rotating(t, g, revision, valkey, revisionFile, credentials, valkey)
	rotating(t, g, client, credentials, dexClient, credentials)
	rotating(t, g, key, credentials, credentials)
	if len(rewrite) != 4 || !rewrite[valkey] || !rewrite[credentials] || !rewrite[revisionFile] || !rewrite[dexClient] {
		t.Fatalf("rewritten files %v", rewrite)
	}
	// Nothing written: every name kept, the revision with them, so the
	// HelmReleases read the same value and no pod template changes.
	g = generatedOf(client, key, password, revision)
	rewrite = frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, revision}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password, revision}},
		{file: revisionFile, change: ChangeUnchanged, secret: []string{revision}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
	}, nil)
	kept(t, g, revision, revisionFile, credentials, valkey)
	kept(t, g, password, valkey)
	if len(rewrite) != 0 {
		t.Fatalf("rewritten files %v", rewrite)
	}
}

// A name frozen in a file with several owners cannot rotate: rewriting it
// would write over their values. The refusal names the name and the file.
func TestFrozenInASharedFileIsRefused(t *testing.T) {
	g := generatedOf(client)
	frozen(g, []holder{
		{file: patch, change: ChangeUnchanged, shared: true, secret: []string{client}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}},
	}, nil)
	gs := g[client]
	if gs.Rotates || gs.Kept || gs.ForcedBy != "" || !strings.Contains(gs.Refusal, client) || !strings.Contains(gs.Refusal, patch) {
		t.Fatalf("client: %+v", gs)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*gs}}
	if p.FrozenRefusal() != gs.Refusal || len(p.Rotated()) != 0 {
		t.Fatalf("refusal %q, rotated %v", p.FrozenRefusal(), p.Rotated())
	}
}

// The person asks to rotate the Valkey password (with its credentials
// revision, as requestedRotations adds it) while every file is on record as
// the render has it: the password and the revision rotate, forced by the
// request; the rewritten credentials file carries the client secret and the
// key on, forced by that file, down to the Dex client Secret. Another
// server's value shares no file and is kept.
func TestFrozenRotatesOnRequest(t *testing.T) {
	const other, otherFile = "other", "acme/mcs:extras/other/oauth-credentials.enc.yaml" // #nosec G101 -- a name and a file path, not values
	g := generatedOf(client, key, password, revision, other)
	rewrite := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, revision}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password, revision}},
		{file: revisionFile, change: ChangeUnchanged, secret: []string{revision}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
		{file: otherFile, change: ChangeUnchanged, secret: []string{other}},
	}, []string{password, revision})
	rotating(t, g, password, ForcedByRequest, valkey)
	rotating(t, g, revision, ForcedByRequest, revisionFile, credentials, valkey)
	rotating(t, g, client, credentials, dexClient, credentials)
	rotating(t, g, key, credentials, credentials)
	kept(t, g, other, otherFile)
	if len(rewrite) != 4 || !rewrite[valkey] || !rewrite[credentials] || !rewrite[revisionFile] || !rewrite[dexClient] {
		t.Fatalf("rewritten files %v", rewrite)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[key], *g[other], *g[revision], *g[password]}}
	requested, forced := p.Rotations()
	if !slices.Equal(requested, []string{revision, password}) || !slices.Equal(forced, []string{client, key}) || !slices.Equal(p.Rotating(), []string{client, key, revision, password}) || p.FrozenRefusal() != "" {
		t.Fatalf("on request %v, forced %v, rotating %v, refusal %q", requested, forced, p.Rotating(), p.FrozenRefusal())
	}
}

// A value asked for that is frozen in a file with several owners cannot
// rotate, the request alike: the refusal names the value and the file.
func TestFrozenOnRequestInASharedFileIsRefused(t *testing.T) {
	g := generatedOf(client)
	frozen(g, []holder{
		{file: patch, change: ChangeUnchanged, shared: true, secret: []string{client}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
	}, []string{client})
	gs := g[client]
	if gs.Rotates || gs.Kept || gs.ForcedBy != "" || !strings.Contains(gs.Refusal, client) || !strings.Contains(gs.Refusal, patch) {
		t.Fatalf("client: %+v", gs)
	}
}

// A name asked for is the plan's when the plan lists it, followed by the
// credentials revision the definition names for it; a name the plan does not
// list — another installation's in a set — takes no part, and a value no
// revision covers draws none.
func TestRequestedRotations(t *testing.T) {
	g := generatedOf(password, key, revision)
	revisions := map[string]string{password: revision, key: revision, "elsewhere": revision}
	got := requestedRotations([]string{password, "elsewhere", pair}, g, revisions)
	if !slices.Equal(got, []string{password, revision}) {
		t.Fatalf("requested %v", got)
	}
	g = generatedOf(pair)
	if got := requestedRotations([]string{pair}, g, revisions); !slices.Equal(got, []string{pair}) {
		t.Fatalf("a value without a revision: %v", got)
	}
}

// secretsDefinition renders a server's credentials Secret, its Valkey
// Secret, its revision Secret and its Dex client Secret, with the revision
// named for every value of the first two, and another server's Secret: the
// definition's shape where the revision couples a server's files.
func secretsDefinition() installations.Capability {
	secret := func(names ...string) render.File {
		f := render.File{}
		for _, n := range names {
			f.Generated = append(f.Generated, render.Generated{Name: n, Placeholder: render.Placeholder(n), Kind: render.Alphanumeric, Length: 32})
			f.Content = append(f.Content, []byte(n+": "+render.Placeholder(n)+"\n")...)
		}
		return f
	}
	const rev, dexSecret, pw = "rowan-revision", "rowan-client", "rowan-password" // #nosec G101 -- generated value names, not values
	return installations.Capability{
		Name:  "secrets",
		Parse: func(any) (render.Input, error) { return fakeInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{
				Files: render.Fileset{renderedConfigs: {
					"server/oauth-credentials.enc.yaml":    secret(dexSecret, "rowan-key", rev),
					"server/valkey-credentials.enc.yaml":   secret(pw, rev),
					"server/credentials-revision.enc.yaml": secret(rev),
					"server/dex-client.enc.yaml":           secret(dexSecret),
					"other/oauth-credentials.enc.yaml":     secret("rowan-other"),
				}},
				Revisions: map[string]string{dexSecret: rev, "rowan-key": rev, pw: rev},
			}, nil
		},
	}
}

// A plan asked to rotate the Valkey password over files on record as the
// render has them: the password and its revision rotate on request, the
// credentials file's other values forced by it, the four files of the server
// update and nothing else — the other server's Secret stands, its value
// kept. A name the plan does not list changes nothing; without a request
// every value is kept.
func TestBuildRotatesOnRequest(t *testing.T) {
	def := secretsDefinition()
	res, err := def.Render(nil, nil, render.ModeCompare)
	if err != nil {
		t.Fatal(err)
	}
	read := func(_ context.Context, _, path string) (string, error) {
		return string(res.Files[renderedConfigs][path].Content), nil
	}
	inst := rowanInstallation()
	build := func(rotate ...string) Installation {
		return Build(context.Background(), Options{Definition: def, Installation: inst, Inputs: map[string]any{}, Read: read, Rotate: rotate})
	}
	if p := build("elsewhere-password"); len(p.Rotating()) != 0 || p.Diff[ChangeUnchanged] != len(p.Files) {
		t.Fatalf("a name the plan does not list: rotating %v, diff %v", p.Rotating(), p.Diff)
	}
	p := build("rowan-password")
	byName := map[string]GeneratedSecret{}
	for _, g := range p.GeneratedSecrets {
		byName[g.Name] = g
	}
	server := acmeConfigs + ":server/"
	for name, by := range map[string]string{"rowan-password": ForcedByRequest, "rowan-revision": ForcedByRequest, "rowan-client": server + "oauth-credentials.enc.yaml", "rowan-key": server + "oauth-credentials.enc.yaml"} {
		if g := byName[name]; !g.Rotates || g.ForcedBy != by {
			t.Errorf("%s: %+v, want rotating forced by %s", name, g, by)
		}
	}
	if g := byName["rowan-other"]; g.Rotates || !g.Kept {
		t.Errorf("the other server's value: %+v", g)
	}
	for _, f := range p.Files {
		want := ChangeUpdate
		if f.Path == "other/oauth-credentials.enc.yaml" {
			want = ChangeUnchanged
		}
		if f.Change != want {
			t.Errorf("%s: %s, want %s", f.Path, f.Change, want)
		}
	}
	if p.Diff[ChangeUpdate] != 4 || p.Diff[ChangeUnchanged] != 1 || p.FrozenRefusal() != "" {
		t.Fatalf("diff %v, refusal %q", p.Diff, p.FrozenRefusal())
	}
}
