package plan

import (
	"slices"
	"strings"
	"testing"
)

// The files of one server, as "<repository>:<path>", and the names held.
const (
	credentials = "acme/mcs:extras/server/oauth-credentials.enc.yaml"  // #nosec G101 -- a file path, not a value
	valkey      = "acme/mcs:extras/server/valkey-credentials.enc.yaml" // #nosec G101 -- a file path, not a value
	dexClient   = "acme/mcs:extras/server/dex-client-server.yaml"
	patch       = "acme/configs:installations/x/apps/dex-app/configmap-values.yaml.patch"
	client      = "client"
	key         = "key"
	password    = "valkey"
	pair        = "pair"
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
	})
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
	})
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
	})
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
	})
	rotating(t, g, pair, configmap, keys)
	if len(rewrite) != 2 || !rewrite[keys] || !rewrite[configmap] {
		t.Fatalf("rewritten files %v", rewrite)
	}
	g = generatedOf(pair)
	rewrite = frozen(g, []holder{
		{file: keys, change: ChangeUpdate, secret: []string{pair}},
		{file: configmap, change: ChangeUnchanged, public: []string{pair}},
	})
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
	const revisionFile, revision = "acme/mcs:extras/server/credentials-revision.enc.yaml", "revision"
	g := generatedOf(client, key, password, revision)
	rewrite := frozen(g, []holder{
		{file: credentials, change: ChangeUnchanged, secret: []string{client, key, revision}},
		{file: valkey, change: ChangeUpdate, secret: []string{password, revision}},
		{file: revisionFile, change: ChangeUnchanged, secret: []string{revision}},
		{file: dexClient, change: ChangeUnchanged, secret: []string{client}},
	})
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
	})
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
	})
	gs := g[client]
	if gs.Rotates || gs.Kept || gs.ForcedBy != "" || !strings.Contains(gs.Refusal, client) || !strings.Contains(gs.Refusal, patch) {
		t.Fatalf("client: %+v", gs)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*gs}}
	if p.FrozenRefusal() != gs.Refusal || len(p.Rotated()) != 0 {
		t.Fatalf("refusal %q, rotated %v", p.FrozenRefusal(), p.Rotated())
	}
}
