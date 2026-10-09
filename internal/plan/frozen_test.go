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
	dexClient    = "acme/mcs:extras/server/dex-client-server-secret.yaml"
	revisionFile = "acme/mcs:extras/server/credentials-revision.enc.yaml" // #nosec G101 -- a file path, not a value
	patch        = "acme/configs:installations/x/apps/dex-app/configmap-values.yaml.patch"
	client       = "client"
	key          = "key"
	password     = "valkey"
	revision     = "revision"
	pair         = "pair"
	// The key paths of a client secret in a credentials Secret and in a Dex client Secret.
	clientKey = "stringData.client-secret"
	secretKey = "stringData.secret" // #nosec G101 -- a key path, not a value
	// The key paths of the Valkey password and the revision in a Valkey Secret.
	defaultKey  = "stringData.default"
	revisionKey = "stringData.revision"
)

// revisions names the server's credentials revision as one.
var revisions = map[string]bool{revision: true}

func generatedOf(names ...string) map[string]*GeneratedSecret {
	out := map[string]*GeneratedSecret{}
	for _, n := range names {
		out[n] = &GeneratedSecret{Name: n}
	}
	return out
}

func rotating(t *testing.T, g map[string]*GeneratedSecret, name, forcedBy string, frozenIn ...string) {
	t.Helper()
	if gs := g[name]; !gs.Rotates || gs.Kept || gs.Refusal != "" || gs.ForcedBy != forcedBy || !slices.Equal(gs.FrozenIn, frozenIn) || len(gs.Carries) != 0 {
		t.Fatalf("%s: %+v, want rotating, forced by %s, frozen in %v", name, gs, forcedBy, frozenIn)
	}
}

func kept(t *testing.T, g map[string]*GeneratedSecret, name string, frozenIn ...string) {
	t.Helper()
	if gs := g[name]; gs.Rotates || !gs.Kept || gs.Refusal != "" || gs.ForcedBy != "" || !slices.Equal(gs.FrozenIn, frozenIn) || len(gs.Carries) != 0 {
		t.Fatalf("%s: %+v, want kept, frozen in %v", name, gs, frozenIn)
	}
}

// carried holds name to kept on record in frozenIn and carried as carries.
func carried(t *testing.T, g map[string]*GeneratedSecret, name string, frozenIn []string, carries ...Carry) {
	t.Helper()
	if gs := g[name]; gs.Rotates || !gs.Kept || gs.Refusal != "" || gs.ForcedBy != "" || !slices.Equal(gs.FrozenIn, frozenIn) || !slices.Equal(gs.Carries, carries) {
		t.Fatalf("%s: %+v, want kept, frozen in %v, carried %v", name, gs, frozenIn, carries)
	}
}

// fresh holds name to a value no file on record holds: drawn by the vault
// as carries, or by the commit without any; pending in pendingIn.
func fresh(t *testing.T, g map[string]*GeneratedSecret, name string, pendingIn []string, carries ...Carry) {
	t.Helper()
	if gs := g[name]; gs.Rotates || gs.Kept || gs.Refusal != "" || gs.ForcedBy != "" || len(gs.FrozenIn) != 0 || !slices.Equal(gs.PendingIn, pendingIn) || !slices.Equal(gs.Carries, carries) {
		t.Fatalf("%s: %+v, want fresh, pending in %v, carried %v", name, gs, pendingIn, carries)
	}
}

// keyPath is the key path of a server Secret's entry.
func keyPath(file, k string) string { return file + "#stringData." + k }

// A record from before a Dex client had a Secret of its own — kagent's
// oauth2-proxy credentials holding the client secret and the cookie secret,
// muster's OAuth credentials holding the client secret, the encryption key
// and the registration token, a server's credentials holding the client
// secret and the key beside the Valkey password its Valkey Secret shares —
// and the render adds the Dex client Secret. The client secret is on record,
// so it is kept and the new file takes it from the record by the caller's
// vault, at the key paths; nothing rotates: the credentials file is not
// rewritten, so the names it holds alone stay kept, the Valkey Secret alike.
// The commit refuses until the carry is on record, naming the value and the
// file, and rewrites no file.
func TestFrozenCarriesANameANewFileShares(t *testing.T) {
	g := generatedOf(client, key, password)
	paths := map[string]string{client: clientKey, key: "stringData.key", password: "stringData.password"}
	whole := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, password}, paths: paths},
		{file: valkey, change: ChangeUnchanged, secret: []string{password}, paths: map[string]string{password: defaultKey}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}, paths: map[string]string{client: secretKey}},
	}, nil, true, nil)
	carried(t, g, client, []string{credentials}, Carry{From: keyPath(credentials, "client-secret"), To: keyPath(dexClient, "secret"), Create: true})
	kept(t, g, key, credentials)
	kept(t, g, password, credentials, valkey)
	if len(whole) != 0 {
		t.Fatalf("files written whole %v", whole)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[key], *g[password]}}
	if len(p.Rotated()) != 0 || len(p.Rotating()) != 0 || p.FrozenRefusal() != "" {
		t.Fatalf("rotation: %v %v, refusal %q", p.Rotated(), p.Rotating(), p.FrozenRefusal())
	}
	if r := p.CarryRefusal(); !strings.Contains(r, client+" is on record in "+credentials+" and "+keyPath(dexClient, "secret")+" takes it") || strings.Contains(r, key) {
		t.Fatalf("carry refusal %q", r)
	}
}

// A new file whose values no record holds is written by the commit, not by
// the vault: the server's revision Secret on a record that predates the
// revision, its credentials file and Valkey Secret lacking the key and
// standing without it (pending), and the Dex client Secret new as well,
// taking the client secret from the record. The revision gets its verdict:
// no file on record holds it, drawn at commit, pending in the two files.
func TestFrozenDrawsANewFilesValuesAtCommit(t *testing.T) {
	g := generatedOf(client, key, password, revision)
	whole := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, revision}, lacks: []string{revision}, paths: map[string]string{client: clientKey}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password, revision}, lacks: []string{revision}},
		{file: revisionFile, change: ChangeCreate, secret: []string{revision}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}, paths: map[string]string{client: secretKey}},
	}, nil, true, revisions)
	fresh(t, g, revision, []string{credentials, valkey})
	carried(t, g, client, []string{credentials}, Carry{From: keyPath(credentials, "client-secret"), To: keyPath(dexClient, "secret"), Create: true})
	kept(t, g, key, credentials)
	kept(t, g, password, valkey)
	if len(whole) != 1 || !whole[revisionFile] {
		t.Fatalf("files written whole %v", whole)
	}
}

// mcp-prometheus's credentials Secret on a 3-line record holds the client
// secret and the key; the 4 line adds the Valkey password, on record in the
// Valkey Secret, and the revision. The file's only differences are the keys
// it lacks, so the vault fills it: the password is kept and carried from the
// Valkey Secret into the credentials file at its key path, the revision
// waits in both files, and the client secret and the key stand — no file is
// rewritten. The Dex client Secret new as well takes the client secret.
func TestFrozenCarriesAKeyAFileOnRecordLacks(t *testing.T) {
	g := generatedOf(client, key, password, revision)
	whole := frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, key, password, revision}, lacks: []string{password, revision}, fillable: true,
			paths: map[string]string{client: "stringData.DEX_CLIENT_SECRET", key: "stringData.MCP_OAUTH_ENCRYPTION_KEY", password: "stringData.VALKEY_PASSWORD", revision: "stringData.CREDENTIALS_REVISION"}}, // #nosec G101 -- key paths, not values
		{file: valkey, change: ChangeUnchanged, secret: []string{password, revision}, lacks: []string{revision}, paths: map[string]string{password: defaultKey, revision: revisionKey}},
		{file: revisionFile, change: ChangeCreate, secret: []string{revision}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}, paths: map[string]string{client: secretKey}},
	}, nil, true, revisions)
	carried(t, g, password, []string{valkey}, Carry{From: keyPath(valkey, "default"), To: keyPath(credentials, "VALKEY_PASSWORD")})
	carried(t, g, client, []string{credentials}, Carry{From: keyPath(credentials, "DEX_CLIENT_SECRET"), To: keyPath(dexClient, "secret"), Create: true})
	kept(t, g, key, credentials)
	fresh(t, g, revision, []string{credentials, valkey})
	if len(whole) != 1 || !whole[revisionFile] {
		t.Fatalf("files written whole %v", whole)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[key], *g[password], *g[revision]}}
	if r := p.CarryRefusal(); !strings.Contains(r, password+" is on record in "+valkey) || !strings.Contains(r, client+" is on record in "+credentials) || strings.Contains(r, revision+" is") {
		t.Fatalf("carry refusal %q", r)
	}
}

// A file on record lacks a value no file holds (the render adds a new key):
// the vault draws it there, and a new file sharing it takes it from there
// rather than having the commit draw a second one. The file's other values
// stand. A credentials revision is not drawn this way: it waits.
func TestFrozenDrawsAFreshKeyAFileOnRecordLacks(t *testing.T) {
	const cookie, cookieFile = "cookie", "acme/mcs:extras/server/cookie-secret.enc.yaml" // #nosec G101 -- a name and a file path, not values
	g := generatedOf(client, cookie, revision)
	whole := frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, cookie, revision}, lacks: []string{cookie, revision}, fillable: true,
			paths: map[string]string{client: clientKey, cookie: "stringData.cookie-secret", revision: "stringData.credentials-revision"}},
		{file: cookieFile, change: ChangeCreate, secret: []string{cookie}, paths: map[string]string{cookie: "stringData.cookie"}},
	}, nil, true, revisions)
	fresh(t, g, cookie, nil, Carry{To: keyPath(credentials, "cookie-secret")}, Carry{From: keyPath(credentials, "cookie-secret"), To: keyPath(cookieFile, "cookie"), Create: true})
	kept(t, g, client, credentials)
	fresh(t, g, revision, []string{credentials})
	if len(whole) != 0 {
		t.Fatalf("files written whole %v", whole)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[cookie], *g[revision]}}
	if r := p.CarryRefusal(); !strings.Contains(r, cookie+" is drawn by your vault into "+keyPath(credentials, "cookie-secret")+", "+keyPath(cookieFile, "cookie")) {
		t.Fatalf("carry refusal %q", r)
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
	}, nil, false, nil)
	kept(t, g, client, dexClient, credentials)
	kept(t, g, password, credentials, valkey)
	if gs := g["other"]; gs.Rotates || gs.Kept || len(gs.FrozenIn) != 0 || len(gs.Carries) != 0 {
		t.Fatalf("other: %+v", gs)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[password]}}
	if len(rewrite) != 0 || len(p.Rotated()) != 0 || len(p.Rotating()) != 0 || p.CarryRefusal() != "" {
		t.Fatalf("rotation without a file to write: %v %v %v", rewrite, p.Rotated(), p.Rotating())
	}
}

// The render changes the credentials file's plaintext skeleton beyond the
// keys it lacks (a field changed): the file is written anew, so every name
// it holds rotates, forced by the file itself with the cause; the Dex client
// Secret kept on record shares the client secret and is rewritten with it.
// The Valkey Secret shares nothing with it and keeps its password.
func TestFrozenARewrittenSkeletonForcesItsNames(t *testing.T) {
	g := generatedOf(client, key, password)
	rewrite := frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, key}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password}},
	}, nil, false, nil)
	rotating(t, g, client, credentials, dexClient, credentials)
	rotating(t, g, key, credentials, credentials)
	kept(t, g, password, valkey)
	if g[client].Cause != causeSkeleton || g[key].Cause != causeSkeleton {
		t.Fatalf("cause %q, %q", g[client].Cause, g[key].Cause)
	}
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
	}, nil, false, nil)
	rotating(t, g, pair, configmap, keys)
	if len(rewrite) != 2 || !rewrite[keys] || !rewrite[configmap] {
		t.Fatalf("rewritten files %v", rewrite)
	}
	g = generatedOf(pair)
	rewrite = frozen(g, []holder{
		{file: keys, change: ChangeUpdate, secret: []string{pair}},
		{file: configmap, change: ChangeUnchanged, public: []string{pair}},
	}, nil, false, nil)
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
	}, nil, false, revisions)
	rotating(t, g, password, valkey, valkey)
	rotating(t, g, revision, valkey, revisionFile, credentials, valkey)
	rotating(t, g, client, credentials, dexClient, credentials)
	rotating(t, g, key, credentials, credentials)
	if g[client].Cause != "rewritten with "+revision {
		t.Fatalf("cause %q", g[client].Cause)
	}
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
	}, nil, false, revisions)
	kept(t, g, revision, revisionFile, credentials, valkey)
	kept(t, g, password, valkey)
	if len(rewrite) != 0 {
		t.Fatalf("rewritten files %v", rewrite)
	}
}

// A name frozen in a file with several owners cannot rotate: rewriting it
// would write over their values. The refusal names the name and the file. A
// new file that shares the name takes it from that file by the vault, no
// rotation needed; a file rewritten whole that holds it forces one, refused.
func TestFrozenInASharedFileIsRefused(t *testing.T) {
	g := generatedOf(client)
	frozen(g, []holder{
		{file: patch, change: ChangeUnchanged, shared: true, secret: []string{client}, paths: map[string]string{client: "oidc.extraStaticClients.0.secret"}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}, paths: map[string]string{client: secretKey}},
	}, nil, false, nil)
	carried(t, g, client, []string{patch}, Carry{From: patch + "#oidc.extraStaticClients.0.secret", To: keyPath(dexClient, "secret"), Create: true})
	g = generatedOf(client)
	frozen(g, []holder{
		{file: patch, change: ChangeUnchanged, shared: true, secret: []string{client}},
		{file: dexClient, change: ChangeUpdate, secret: []string{client}},
	}, nil, false, nil)
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
	}, []string{password, revision}, false, revisions)
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

// A rotation asked for reaches a new file that shares the name: the commit
// writes the new file with the new value, no carry, and the credentials file
// on record is rewritten with it, its other values forced.
func TestFrozenARequestWritesTheNewFileItReaches(t *testing.T) {
	g := generatedOf(client, key)
	whole := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}},
	}, []string{client}, true, nil)
	rotating(t, g, client, ForcedByRequest, credentials)
	rotating(t, g, key, credentials, credentials)
	if len(whole) != 2 || !whole[credentials] || !whole[dexClient] {
		t.Fatalf("files written whole %v", whole)
	}
}

// A value asked for that is frozen in a file with several owners cannot
// rotate, the request alike: the refusal names the value and the file.
func TestFrozenOnRequestInASharedFileIsRefused(t *testing.T) {
	g := generatedOf(client)
	frozen(g, []holder{
		{file: patch, change: ChangeUnchanged, shared: true, secret: []string{client}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
	}, []string{client}, false, nil)
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
		if f.Change != want || f.Rewritten != (want == ChangeUpdate) {
			t.Errorf("%s: %s (rewritten %v), want %s", f.Path, f.Change, f.Rewritten, want)
		}
	}
	if p.Diff[ChangeUpdate] != 4 || p.Diff[ChangeUnchanged] != 1 || p.FrozenRefusal() != "" {
		t.Fatalf("diff %v, refusal %q", p.Diff, p.FrozenRefusal())
	}
}

// A server enabled as gazelle's are: its revision Secret's skeleton changes
// (a field changed) while its credentials file and Valkey Secret hold the
// revision beside their values. Where the capability is on record, nobody
// asked for a rotation, so none happens: every name the change would rotate
// is refused, naming the file that forces it and the cause, and the commit
// refuses.
func TestFrozenRefusesAnUnrequestedRotationOnRecord(t *testing.T) {
	g := generatedOf(client, key, password, revision)
	frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, revision}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password, revision}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
		{file: revisionFile, change: ChangeUpdate, secret: []string{revision}},
	}, nil, true, revisions)
	for _, n := range []string{client, key, password, revision} {
		if gs := g[n]; gs.Rotates || gs.Kept || !strings.Contains(gs.Refusal, n+" would rotate") || !strings.Contains(gs.Refusal, "rotate "+n) {
			t.Fatalf("%s: %+v, want refused", n, gs)
		}
	}
	if !strings.Contains(g[revision].Refusal, "forced by "+revisionFile+" ("+causeSkeleton+")") || !strings.Contains(g[client].Refusal, "forced by "+credentials+" (rewritten with "+revision+")") {
		t.Fatalf("the refusals do not name the file and the cause: %q, %q", g[revision].Refusal, g[client].Refusal)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[key], *g[password], *g[revision]}}
	if len(p.Rotating()) != 0 || p.FrozenRefusal() == "" {
		t.Fatalf("rotating %v, refusal %q", p.Rotating(), p.FrozenRefusal())
	}
}

// On record, a rotation asked for goes ahead with everything it reaches: the
// Valkey password and its revision asked for, the credentials file holding
// the revision rewritten, its client secret and key with it. A skeleton
// change elsewhere that no request reaches is still refused.
func TestFrozenRotatesWhatARequestReachesOnRecord(t *testing.T) {
	const keys = "acme/mcs:portal/plugin-keys-secret.enc.yaml"
	g := generatedOf(client, key, password, revision, pair)
	frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, revision}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password, revision}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
		{file: keys, change: ChangeUpdate, secret: []string{pair}},
	}, []string{password, revision}, true, revisions)
	rotating(t, g, password, ForcedByRequest, valkey)
	rotating(t, g, revision, ForcedByRequest, credentials, valkey)
	rotating(t, g, client, credentials, dexClient, credentials)
	rotating(t, g, key, credentials, credentials)
	if gs := g[pair]; gs.Rotates || !strings.Contains(gs.Refusal, "forced by "+keys) {
		t.Fatalf("the unrequested rotation: %+v", gs)
	}
}

// The server's credentials file on record lacks the Valkey password and the
// revision, the revision Secret and the Valkey Secret holding the revision:
// the password is carried into the credentials file, the revision is not —
// it is kept on record and the credentials file stands without it, pending,
// until a rotation asked for rewrites the file from the render.
func TestFrozenCarriesNoPendingRevision(t *testing.T) {
	g := generatedOf(client, key, password, revision)
	whole := frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, key, password, revision}, lacks: []string{password, revision}, fillable: true,
			paths: map[string]string{password: "stringData.VALKEY_PASSWORD", revision: "stringData.CREDENTIALS_REVISION"}}, // #nosec G101 -- key paths, not values
		{file: valkey, change: ChangeUnchanged, secret: []string{password, revision}, paths: map[string]string{password: defaultKey, revision: revisionKey}},
		{file: revisionFile, change: ChangeUnchanged, secret: []string{revision}, paths: map[string]string{revision: revisionKey}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
	}, nil, true, revisions)
	carried(t, g, password, []string{valkey}, Carry{From: keyPath(valkey, "default"), To: keyPath(credentials, "VALKEY_PASSWORD")})
	if gs := g[revision]; !gs.Kept || !slices.Equal(gs.FrozenIn, []string{revisionFile, valkey}) || len(gs.Carries) != 0 || !slices.Equal(gs.PendingIn, []string{credentials}) {
		t.Fatalf("the revision: %+v", gs)
	}
	kept(t, g, client, dexClient, credentials)
	kept(t, g, key, credentials)
	if len(whole) != 0 {
		t.Fatalf("files written whole %v", whole)
	}
}
