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
)

func generatedOf(names ...string) map[string]*GeneratedSecret {
	out := map[string]*GeneratedSecret{}
	for _, n := range names {
		out[n] = &GeneratedSecret{Name: n}
	}
	return out
}

// The server's credentials file exists and the Dex client Secret is new: the
// client secret rotates into both; the credentials file being rewritten, the
// encryption key it alone holds rotates too, and so does the valkey password
// it shares with the Valkey Secret on record — that file is rewritten as
// well. Nothing is refused.
func TestFrozenRotatesThroughTheRewrittenFiles(t *testing.T) {
	g := generatedOf(client, key, password)
	frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, key, password}},
		{file: valkey, change: ChangeUpdate, secret: []string{password}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}},
	})
	for name, want := range map[string][]string{client: {credentials}, key: {credentials}, password: {credentials, valkey}} {
		if gs := g[name]; !gs.Rotates || gs.Refusal != "" || !slices.Equal(gs.FrozenIn, want) {
			t.Fatalf("%s: %+v", name, gs)
		}
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[key], *g[password]}}
	if rotated := p.Rotated(); len(rotated) != 2 || !rotated[credentials] || !rotated[valkey] {
		t.Fatalf("rotated files %v", rotated)
	}
	if got := p.Rotating(); !slices.Equal(got, []string{client, key, password}) || p.FrozenRefusal() != "" {
		t.Fatalf("rotating %v, refusal %q", got, p.FrozenRefusal())
	}
}

// Both files exist: no file needs the frozen values, nothing rotates; the
// names are listed as frozen so the reader knows the values on record stand.
// An unchanged or unreadable file takes no part.
func TestFrozenWithoutANewFileKeepsTheValues(t *testing.T) {
	g := generatedOf(client, password, "other")
	frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, password}},
		{file: dexClient, change: ChangeUpdate, secret: []string{client}},
		{file: valkey, change: ChangeUnchanged, secret: []string{password}},
		{file: "acme/mcs:extras/other.yaml", change: ChangeUnknown, secret: []string{"other"}},
	})
	if gs := g[client]; gs.Rotates || !slices.Equal(gs.FrozenIn, []string{dexClient, credentials}) {
		t.Fatalf("client: %+v", gs)
	}
	if gs := g[password]; gs.Rotates || !slices.Equal(gs.FrozenIn, []string{credentials}) {
		t.Fatalf("valkey: %+v", gs)
	}
	if gs := g["other"]; gs.Rotates || len(gs.FrozenIn) != 0 {
		t.Fatalf("other: %+v", gs)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*g[client], *g[password]}}
	if len(p.Rotated()) != 0 || len(p.Rotating()) != 0 {
		t.Fatalf("rotation without a new file: %v %v", p.Rotated(), p.Rotating())
	}
}

// A key pair's public half in a plain file to write needs the pair; the
// private half frozen in the secret file on record makes the pair rotate.
func TestFrozenPublicHalfNeedsThePair(t *testing.T) {
	g := generatedOf("pair")
	frozen(g, []holder{
		{file: "acme/mcs:portal/plugin-keys-secret.enc.yaml", change: ChangeUpdate, secret: []string{"pair"}},
		{file: "acme/mcs:portal/configmap.yaml", change: ChangeUpdate, public: []string{"pair"}},
	})
	if gs := g["pair"]; !gs.Rotates || len(gs.FrozenIn) != 1 {
		t.Fatalf("pair: %+v", gs)
	}
}

// A name frozen in a file with several owners cannot rotate: rewriting it
// would write over their values. The refusal names the name and the file.
func TestFrozenInASharedFileIsRefused(t *testing.T) {
	g := generatedOf(client)
	frozen(g, []holder{
		{file: patch, change: ChangeUpdate, shared: true, secret: []string{client}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}},
	})
	gs := g[client]
	if gs.Rotates || !strings.Contains(gs.Refusal, client) || !strings.Contains(gs.Refusal, patch) {
		t.Fatalf("client: %+v", gs)
	}
	p := Installation{GeneratedSecrets: []GeneratedSecret{*gs}}
	if p.FrozenRefusal() != gs.Refusal || len(p.Rotated()) != 0 {
		t.Fatalf("refusal %q, rotated %v", p.FrozenRefusal(), p.Rotated())
	}
}
