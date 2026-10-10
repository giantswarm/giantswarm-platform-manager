package plan

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A token-exchange pair: hazel's credentials Secret for oak and oak's
// Dex-side copy, one generated value in two installations' plans.
const (
	pairName     = "muster-token-exchange-oak-client-secret" // #nosec G101 -- a generated value's name, not a value
	pairHubSide  = "management-clusters/hazel/extras/agent-platform/secrets/oak-token-exchange-credentials.yaml"
	pairPeerSide = "management-clusters/oak/extras/agent-platform/secrets/dex-client-muster-token-exchange-oak-secret.yaml"
	renderedHub  = "giantswarm/example-management-clusters"
)

// pairManifest is a side of the pair with value under client-secret.
func pairManifest(name, value string) string {
	return "apiVersion: v1\nkind: Secret\nmetadata:\n  name: " + name + "\n  namespace: giantswarm\ntype: Opaque\nstringData:\n  client-secret: " + value + "\n"
}

// pairOnRecord is a side of the pair as SOPS keeps it on record.
func pairOnRecord(name string) string {
	return pairManifest(name, "ENC[AES256_GCM,data:fixture,iv:fixture,tag:fixture,type:str]") +
		"sops:\n  age:\n    - recipient: age1fixture\n  encrypted_regex: ^(data|stringData)$\n  version: 3.9.0\n"
}

// pairDefinition renders hazel's side of the pair, naming peer as the other.
func pairDefinition(peer *render.Peer) installations.Capability {
	return installations.Capability{
		Name:  "pair",
		Parse: func(any) (render.Input, error) { return fakeInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{Files: render.Fileset{renderedHub: {pairHubSide: render.File{
				Content:   []byte(pairManifest("oak-token-exchange-credentials", render.Placeholder(pairName))),
				Generated: []render.Generated{{Name: pairName, Placeholder: render.Placeholder(pairName), Kind: render.Base64, Length: 32, Peer: peer}},
			}}}}, nil
		},
	}
}

// A value with a peer is drawn by neither side alone: kept on both sides it
// stands; created here while the peer's file is on record, the person
// supplies it (the plan asks for it by name); created here while the peer's
// file is absent, the commit is refused before any write, naming the pair,
// the peer's file and the wave as the way out; rotated here, forced or
// asked for, refused alike; a peer that cannot be read or resolved refuses
// too. A value without a peer (a target that keeps its Dex client by hand)
// is created here as any other.
func TestBuildRefusesADrawOfOneSideOfAPair(t *testing.T) {
	peer := &render.Peer{Installation: oak.Name, Path: pairPeerSide}
	peerFile := oakMCs + ":" + pairPeerSide
	hubFile, peerKey := hubMCs+":"+pairHubSide, oakMCs+":"+pairPeerSide
	both := map[string]string{hubFile: pairOnRecord("oak-token-exchange-credentials"), peerKey: pairOnRecord("dex-client-muster-token-exchange-oak")}
	for _, tc := range []struct {
		name     string
		peer     *render.Peer
		onRecord map[string]string
		readErr  error
		registry map[string]installations.Installation
		rotate   []string
		want     string // a part of the refusal; empty: none
		kept     bool
		supplied bool
	}{
		{name: "both sides kept", peer: peer, onRecord: both, registry: byName, kept: true},
		{name: "creating this side, the peer absent", peer: peer, onRecord: map[string]string{}, registry: byName, want: "oak's file is not on record, so each side would draw its own value and the token exchange would fail with invalid_client — the manager decrypts nothing, so reconcile oak in the same wave, which draws the value once for both sides, or a person puts the one value on both sides"},
		{name: "creating this side, the peer on record", peer: peer, onRecord: map[string]string{peerKey: both[peerKey]}, registry: byName, supplied: true},
		{name: "rotating this side on request", peer: peer, onRecord: both, registry: byName, rotate: []string{pairName}, want: "would not reach oak's file, which keeps its own"},
		{name: "the peer unreadable", peer: peer, onRecord: map[string]string{}, readErr: errors.New("403 forbidden"), registry: byName, want: "oak's file could not be read (403 forbidden)"},
		{name: "the peer not in the registry", peer: peer, onRecord: map[string]string{}, registry: map[string]installations.Installation{hazel.Name: hazel}, want: "could not be read (not in the registry)"},
		{name: "no peer: the target keeps its client by hand", onRecord: map[string]string{}, registry: byName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func(_ context.Context, repository, path string) (string, error) {
				key := repository + ":" + path
				if c, ok := tc.onRecord[key]; ok {
					return c, nil
				}
				if tc.readErr != nil && key == peerKey {
					return "", tc.readErr
				}
				return "", gh.ErrNotFound
			}
			p := Build(context.Background(), Options{Definition: pairDefinition(tc.peer), Installation: hazel, Hub: hazel, Inputs: map[string]any{}, Read: read, Rotate: tc.rotate, Installations: tc.registry})
			if p.Refused != "" || len(p.GeneratedSecrets) != 1 {
				t.Fatalf("refused %q, generated %+v", p.Refused, p.GeneratedSecrets)
			}
			g := p.GeneratedSecrets[0]
			if tc.peer != nil && tc.registry[oak.Name].Name != "" && g.Peer != peerFile {
				t.Errorf("peer %q, want %q", g.Peer, peerFile)
			}
			refusal := p.FrozenRefusal()
			switch {
			case tc.want == "" && refusal != "":
				t.Errorf("refused: %s", refusal)
			case tc.want != "" && (!strings.Contains(refusal, tc.want) || !strings.HasPrefix(refusal, pairName+" is one value with oak's ") || !strings.Contains(refusal, pairPeerSide) || g.Rotates):
				t.Errorf("refusal %q (rotates %v), want the pair, the peer's file and %q", refusal, g.Rotates, tc.want)
			}
			if g.Kept != tc.kept {
				t.Errorf("kept %v, want %v", g.Kept, tc.kept)
			}
			if g.Supplied != tc.supplied || slices.Contains(p.SuppliedSecrets, pairName) != tc.supplied || g.DrawnWith != "" {
				t.Errorf("supplied %v, suppliedSecrets %v, drawn with %q; want supplied %v", g.Supplied, p.SuppliedSecrets, g.DrawnWith, tc.supplied)
			}
		})
	}
}

// peerDefinition renders oak's side of the pair, the Dex-side copy, naming
// peer as the other.
func peerDefinition(peer *render.Peer) installations.Capability {
	return installations.Capability{
		Name:  "pair",
		Parse: func(any) (render.Input, error) { return fakeInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{Files: render.Fileset{renderedMCs: {pairPeerSide: render.File{
				Content:   []byte(pairManifest("dex-client-muster-token-exchange-oak", render.Placeholder(pairName))),
				Generated: []render.Generated{{Name: pairName, Placeholder: render.Placeholder(pairName), Kind: render.Base64, Length: 32, Peer: peer}},
			}}}}, nil
		},
	}
}

// A pair both of whose sides one set creates — hazel's credentials Secret
// for oak in hazel's plan, oak's Dex-side copy in oak's — is drawn once for
// the wave: each plan alone refuses its side; paired, each names the other's
// installation and refuses nothing. With one side on record that side is
// kept and the other supplied, and nothing is paired; a side whose peer's
// plan is not in the set stays refused, naming the wave.
func TestShareDrawsAPairBothSidesOfWhichTheSetCreates(t *testing.T) {
	hubFile, peerFile := hubMCs+":"+pairHubSide, oakMCs+":"+pairPeerSide
	hubDef := pairDefinition(&render.Peer{Installation: oak.Name, Path: pairPeerSide})
	peerDef := peerDefinition(&render.Peer{Installation: hazel.Name, Path: pairHubSide})
	build := func(def installations.Capability, inst installations.Installation, onRecord map[string]string) *Installation {
		read := func(_ context.Context, repository, path string) (string, error) {
			if c, ok := onRecord[repository+":"+path]; ok {
				return c, nil
			}
			return "", gh.ErrNotFound
		}
		p := Build(context.Background(), Options{Definition: def, Installation: inst, Hub: hazel, Inputs: map[string]any{}, Read: read, Installations: byName})
		if p.Refused != "" || len(p.GeneratedSecrets) != 1 {
			t.Fatalf("%s: refused %q, generated %+v", inst.Name, p.Refused, p.GeneratedSecrets)
		}
		return &p
	}
	generated := func(p *Installation) GeneratedSecret { return p.GeneratedSecrets[0] }
	t.Run("both sides created", func(t *testing.T) {
		h, o := build(hubDef, hazel, nil), build(peerDef, oak, nil)
		if h.FrozenRefusal() == "" || o.FrozenRefusal() == "" {
			t.Fatalf("alone, each side is refused: %q, %q", h.FrozenRefusal(), o.FrozenRefusal())
		}
		if changed := ShareDraws([]*Installation{h, o}); !slices.Equal(changed, []string{hazel.Name, oak.Name}) {
			t.Fatalf("changed %v", changed)
		}
		if g := generated(h); g.DrawnWith != oak.Name || g.Peer != peerFile || g.Supplied || h.FrozenRefusal() != "" {
			t.Errorf("hazel's side %+v", g)
		}
		if g := generated(o); g.DrawnWith != hazel.Name || g.Peer != hubFile || g.Supplied || o.FrozenRefusal() != "" {
			t.Errorf("oak's side %+v", g)
		}
	})
	t.Run("the hub's side on record", func(t *testing.T) {
		onRecord := map[string]string{hubFile: pairOnRecord("oak-token-exchange-credentials")}
		h, o := build(hubDef, hazel, onRecord), build(peerDef, oak, onRecord)
		if changed := ShareDraws([]*Installation{h, o}); len(changed) != 0 {
			t.Fatalf("changed %v", changed)
		}
		if g := generated(h); !g.Kept || g.DrawnWith != "" || g.Supplied || h.FrozenRefusal() != "" {
			t.Errorf("hazel's side %+v", g)
		}
		if g := generated(o); !g.Supplied || g.DrawnWith != "" || o.FrozenRefusal() != "" || !slices.Equal(o.SuppliedSecrets, []string{pairName}) {
			t.Errorf("oak's side %+v, supplied %v", g, o.SuppliedSecrets)
		}
	})
	t.Run("the peer's plan not in the set", func(t *testing.T) {
		h := build(hubDef, hazel, nil)
		if changed := ShareDraws([]*Installation{h}); len(changed) != 0 || generated(h).DrawnWith != "" || !strings.Contains(h.FrozenRefusal(), "reconcile oak in the same wave") {
			t.Errorf("changed %v, %+v", changed, generated(h))
		}
	})
}

// A value another capability holds (render.Generated's HeldBy) is created
// and kept here as any other, but a rotation here alone is refused, naming
// the holder; a value without an outside holder rotates on request.
func TestBuildRefusesARotationOfAValueHeldOutside(t *testing.T) {
	const holder = "the customer-portal capability's federation.tokenBroker"
	hubFile := hubMCs + ":" + pairHubSide
	onRecord := map[string]string{hubFile: pairOnRecord("oak-token-exchange-credentials")}
	for _, tc := range []struct {
		name     string
		heldBy   string
		onRecord map[string]string
		rotate   []string
		refused  bool
		rotates  bool
	}{
		{name: "created", heldBy: holder, onRecord: map[string]string{}},
		{name: "kept", heldBy: holder, onRecord: onRecord},
		{name: "rotated on request", heldBy: holder, onRecord: onRecord, rotate: []string{pairName}, refused: true},
		{name: "no outside holder, rotated on request", onRecord: onRecord, rotate: []string{pairName}, rotates: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := pairDefinition(nil)
			base := def.Render
			def.Render = func(in any, m map[string]string, mode render.Mode) (*render.Result, error) {
				res, err := base(in, m, mode)
				f := res.Files[renderedHub][pairHubSide]
				res.Files[renderedHub][pairHubSide] = f.HeldBy(pairName, tc.heldBy)
				return res, err
			}
			read := func(_ context.Context, repository, path string) (string, error) {
				if c, ok := tc.onRecord[repository+":"+path]; ok {
					return c, nil
				}
				return "", gh.ErrNotFound
			}
			p := Build(context.Background(), Options{Definition: def, Installation: hazel, Hub: hazel, Inputs: map[string]any{}, Read: read, Rotate: tc.rotate, Installations: byName})
			if p.Refused != "" || len(p.GeneratedSecrets) != 1 {
				t.Fatalf("refused %q, generated %+v", p.Refused, p.GeneratedSecrets)
			}
			g, refusal := p.GeneratedSecrets[0], p.FrozenRefusal()
			if tc.refused != (refusal != "") || tc.refused && !strings.Contains(refusal, holder+" holds it too") {
				t.Errorf("refusal %q, want refused %v naming %q", refusal, tc.refused, holder)
			}
			if g.Rotates != tc.rotates || g.HeldBy != tc.heldBy {
				t.Errorf("rotates %v held by %q, want %v %q", g.Rotates, g.HeldBy, tc.rotates, tc.heldBy)
			}
		})
	}
}
