package plan

import (
	"context"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The names and files of kagent's oauth2-proxy credentials: its Dex client
// and cookie secrets, the revision that rolls the proxy with either, held by
// the proxy's Secret and a revision Secret of its own, and the Dex client
// Secret that holds the client secret too.
const (
	proxyDex      = "rowan-kagent-dex-client-secret"
	proxyCookie   = "rowan-kagent-cookie-secret"
	proxyRevision = "rowan-kagent-credentials-revision"
	proxyFile     = "secrets/kagent-oauth2-proxy-credentials.yaml"
	proxyRevFile  = "secrets/kagent-credentials-revision.yaml"
	proxyDexFile  = "secrets/dex-client-kagent-secret.yaml"
	proxyMarker   = "marker.yaml"
)

// kagentSecret is a Secret as the render writes it, one stringData entry
// per line of entries.
func kagentSecret(name string, entries ...string) string {
	return "apiVersion: v1\nkind: Secret\nmetadata:\n  name: " + name + "\ntype: Opaque\nstringData:\n  " + strings.Join(entries, "\n  ") + "\n"
}

// encryptedOnRecord is rendered as SOPS keeps it on record: every value
// under stringData encrypted, the sops block beneath.
func encryptedOnRecord(rendered string) string {
	enc := regexp.MustCompile(`(?m)^(  [a-z-]+): .*$`).ReplaceAllString(rendered, "$1: ENC[AES256_GCM,data:fixture,iv:fixture,tag:fixture,type:str]")
	return enc + "sops:\n  age:\n    - recipient: age1fixture\n  encrypted_regex: ^(data|stringData)$\n  version: 3.9.0\n"
}

// kagentDefinition renders kagent's proxy credentials the way the
// agent-platform definition does since the proxy has a revision, and a
// marker file that puts the capability on record.
func kagentDefinition() (installations.Capability, map[string]string) {
	g := func(name string) render.Generated {
		return render.Generated{Name: name, Placeholder: render.Placeholder(name), Kind: render.Alphanumeric, Length: 32}
	}
	files := map[string]render.File{
		proxyFile: {Content: []byte(kagentSecret("kagent-oauth2-proxy-credentials", "client-id: kagent", "client-secret: "+render.Placeholder(proxyDex),
			"cookie-secret: "+render.Placeholder(proxyCookie), "credentials-revision: "+render.Placeholder(proxyRevision))),
			Generated: []render.Generated{g(proxyDex), g(proxyCookie), g(proxyRevision)}},
		proxyRevFile: {Content: []byte(kagentSecret("kagent-credentials-revision", "revision: "+render.Placeholder(proxyRevision))), Generated: []render.Generated{g(proxyRevision)}},
		proxyDexFile: {Content: []byte(kagentSecret("dex-client-kagent", "secret: "+render.Placeholder(proxyDex))), Generated: []render.Generated{g(proxyDex)}},
		proxyMarker:  {Content: []byte("enabled: true\n")},
	}
	rendered := map[string]string{}
	for path, f := range files {
		rendered[path] = string(f.Content)
	}
	return installations.Capability{
		Name:          "kagent-proxy",
		Parse:         func(any) (render.Input, error) { return fakeInput{}, nil },
		EnabledMarker: func(string) string { return proxyMarker },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{Files: render.Fileset{renderedConfigs: files}, Revisions: map[string]string{proxyDex: proxyRevision, proxyCookie: proxyRevision}}, nil
		},
	}, rendered
}

// buildKagent plans the kagent definition against record (path → content,
// absent when missing), rotating the names asked for.
func buildKagent(t *testing.T, record map[string]string, rotate ...string) Installation {
	t.Helper()
	def, _ := kagentDefinition()
	read := func(_ context.Context, _, path string) (string, error) {
		if c, ok := record[path]; ok {
			return c, nil
		}
		return "", gh.ErrNotFound
	}
	return Build(context.Background(), Options{Definition: def, Installation: rowanInstallation(), Inputs: map[string]any{}, Read: read, Rotate: rotate})
}

func changes(p Installation) map[string]Change {
	out := map[string]Change{}
	for _, f := range p.Files {
		out[f.Path] = f.Change
	}
	return out
}

// The proxy's Secret on record predates its revision: the render adds the
// revision key, nothing else. A plain reconcile keeps the file as recorded
// and creates the revision Secret: no value rotates, nothing is refused.
func TestBuildKeepsAFileOnRecordThatPredatesItsRevision(t *testing.T) {
	_, rendered := kagentDefinition()
	old := kagentSecret("kagent-oauth2-proxy-credentials", "client-id: kagent", "client-secret: x", "cookie-secret: x")
	p := buildKagent(t, map[string]string{
		proxyFile:    encryptedOnRecord(old),
		proxyDexFile: encryptedOnRecord(rendered[proxyDexFile]),
		proxyMarker:  rendered[proxyMarker],
	})
	if r := p.FrozenRefusal(); r != "" {
		t.Fatalf("refused: %s", r)
	}
	if len(p.Rotating()) != 0 {
		t.Fatalf("rotating %v", p.Rotating())
	}
	want := map[string]Change{proxyFile: ChangeUnchanged, proxyRevFile: ChangeCreate, proxyDexFile: ChangeUnchanged, proxyMarker: ChangeUnchanged}
	if got := changes(p); !maps.Equal(got, want) {
		t.Fatalf("changes %v, want %v", got, want)
	}
	// A rotation asked for rewrites the file from the render: the revision
	// joins it and rolls the proxy, the client secret the file holds drawn
	// with it.
	p = buildKagent(t, map[string]string{
		proxyFile:    encryptedOnRecord(old),
		proxyRevFile: encryptedOnRecord(rendered[proxyRevFile]),
		proxyDexFile: encryptedOnRecord(rendered[proxyDexFile]),
		proxyMarker:  rendered[proxyMarker],
	}, proxyCookie)
	if r := p.FrozenRefusal(); r != "" {
		t.Fatalf("the requested rotation is refused: %s", r)
	}
	if got := p.Rotating(); !slices.Equal(got, []string{proxyCookie, proxyRevision, proxyDex}) {
		t.Fatalf("rotating %v", got)
	}
	if got := changes(p); got[proxyFile] != ChangeUpdate || got[proxyRevFile] != ChangeUpdate || got[proxyDexFile] != ChangeUpdate {
		t.Fatalf("changes %v", got)
	}
}

// The proxy's Secret on record holds its revision: every file stands.
func TestBuildKeepsAFileOnRecordThatHoldsItsRevision(t *testing.T) {
	_, rendered := kagentDefinition()
	record := map[string]string{proxyMarker: rendered[proxyMarker]}
	for _, path := range []string{proxyFile, proxyRevFile, proxyDexFile} {
		record[path] = encryptedOnRecord(rendered[path])
	}
	p := buildKagent(t, record)
	if r := p.FrozenRefusal(); r != "" || len(p.Rotating()) != 0 || p.Diff[ChangeUnchanged] != len(p.Files) {
		t.Fatalf("refusal %q, rotating %v, diff %v", r, p.Rotating(), p.Diff)
	}
}

// Only a revision waits: a render that adds any other value to the file on
// record rewrites it, and the rotation that forces is refused.
func TestBuildRewritesAFileOnRecordMissingAValue(t *testing.T) {
	_, rendered := kagentDefinition()
	noCookie := kagentSecret("kagent-oauth2-proxy-credentials", "client-id: kagent", "client-secret: x", "credentials-revision: x")
	p := buildKagent(t, map[string]string{
		proxyFile:    encryptedOnRecord(noCookie),
		proxyRevFile: encryptedOnRecord(rendered[proxyRevFile]),
		proxyDexFile: encryptedOnRecord(rendered[proxyDexFile]),
		proxyMarker:  rendered[proxyMarker],
	})
	if r := p.FrozenRefusal(); !strings.Contains(r, proxyDex+" would rotate") {
		t.Fatalf("refusal %q", r)
	}
}

func TestPendingRevisions(t *testing.T) {
	revisions := map[string]bool{"rev": true}
	rendered := kagentSecret("s", "a: "+render.Placeholder("a"), "revision: "+render.Placeholder("rev"))
	old := encryptedOnRecord(kagentSecret("s", "a: x"))
	without, names := pendingRevisions(rendered, old, revisions)
	if !slices.Equal(names, []string{"rev"}) || !sameSkeleton(without, old) {
		t.Fatalf("names %v, without %q", names, without)
	}
	if _, names := pendingRevisions(rendered, encryptedOnRecord(rendered), revisions); names != nil {
		t.Fatalf("a revision on record is pending: %v", names)
	}
	if _, names := pendingRevisions(rendered, old, map[string]bool{"a": true}); names != nil {
		t.Fatalf("a value on record is pending: %v", names)
	}
	if _, names := pendingRevisions(rendered, kagentSecret("s", "a: x"), revisions); names != nil {
		t.Fatalf("a plain file: %v", names)
	}
}
