package plan

import (
	"context"
	"errors"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestDeferOnlyNewPairsOutsideThePlan(t *testing.T) {
	for _, test := range []struct {
		name                                          string
		local, remote                                 bool
		peerError                                     error
		selected, rotate, deferred, refused, supplied bool
	}{
		{name: "new pair outside the plan", deferred: true},
		{name: "peer in the plan", selected: true, refused: true},
		{name: "existing peer", remote: true, supplied: true},
		{name: "existing local side", local: true},
		{name: "rotation of an existing pair", local: true, remote: true, rotate: true, refused: true},
		{name: "requested draw", rotate: true, refused: true},
		{name: "unreadable peer", peerError: errors.New("forbidden"), refused: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := pairDefinition(&render.Peer{Installation: oak.Name, Path: pairPeerSide})
			original := definition.Render
			definition.Render = func(input any, supplied map[string]string, mode render.Mode) (*render.Result, error) {
				result, err := original(input, supplied, mode)
				result.WithoutPairs = func(names []string) (*render.Result, error) {
					if len(names) != 1 || names[0] != pairName {
						t.Fatalf("deferred names %v", names)
					}
					return &render.Result{}, nil
				}
				return result, err
			}
			read := func(_ context.Context, repository, path string) (string, error) {
				if repository == hubMCs && path == pairHubSide && test.local {
					return pairOnRecord("oak-token-exchange-credentials"), nil
				}
				if repository == oakMCs && path == pairPeerSide {
					if test.peerError != nil {
						return "", test.peerError
					}
					if test.remote {
						return pairOnRecord("dex-client-muster-token-exchange-oak"), nil
					}
				}
				return "", gh.ErrNotFound
			}
			options := Options{Definition: definition, Installation: hazel, Hub: hazel, Read: read, Installations: byName,
				PlanInstallations: map[string]bool{hazel.Name: true, oak.Name: test.selected}}
			if test.rotate {
				options.Rotate = []string{pairName}
			}
			result := Build(t.Context(), options)
			if result.Refused != "" {
				t.Fatal(result.Refused)
			}
			if (len(result.DeferredPairs) == 1) != test.deferred || (result.FrozenRefusal() != "") != test.refused {
				t.Fatalf("deferred %+v, refusal %q", result.DeferredPairs, result.FrozenRefusal())
			}
			if test.deferred {
				if len(result.Files) != 0 || len(result.GeneratedSecrets) != 0 || len(result.SuppliedSecrets) != 0 {
					t.Fatalf("deferred pair is still writable: %+v", result)
				}
			} else if len(result.GeneratedSecrets) != 1 || result.GeneratedSecrets[0].Supplied != test.supplied {
				t.Fatalf("generated %+v", result.GeneratedSecrets)
			}
		})
	}
}
