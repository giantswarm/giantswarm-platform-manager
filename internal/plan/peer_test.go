package plan

import (
	"context"
	"errors"
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
// stands; created here — the peer absent or on record — or rotated here,
// forced or asked for, the commit is refused before any write, naming the
// pair and the peer's file; a peer that cannot be read or resolved refuses
// alike. A value without a peer (a target that keeps its Dex client by hand)
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
	}{
		{name: "both sides kept", peer: peer, onRecord: both, registry: byName, kept: true},
		{name: "creating this side, the peer absent", peer: peer, onRecord: map[string]string{}, registry: byName, want: "oak's file is not on record, so each side would draw its own value"},
		{name: "creating this side, the peer on record", peer: peer, onRecord: map[string]string{peerKey: both[peerKey]}, registry: byName, want: "would not reach oak's file, which keeps its own"},
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
		})
	}
}
