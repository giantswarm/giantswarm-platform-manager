package tools

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/sopsenc"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// A repository without a .sops.yaml — teleport-fleet, whose tunnelport
// values are plain — takes the plan's plain files as they are: nothing to
// encrypt, nothing refused.
func TestEncryptPassesPlainFilesThroughWithoutSopsConfig(t *testing.T) {
	noSops := fmt.Errorf("giantswarm/teleport-fleet has no %s readable as you (%w)", SopsConfig, gh.ErrNotFound)
	tg := &target{noSops: noSops, exists: map[string]bool{"kubernetes/envs/prod/kept.yaml": true}, files: []sopsenc.File{
		{Path: "kubernetes/envs/prod/values.yaml", Content: []byte("tunnelport:\n  tunnels: []\n")},
		{Path: "kubernetes/envs/prod/kept.yaml", Content: []byte("on record\n")},
	}}
	out, err := encrypt("giantswarm/teleport-fleet", tg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || string(out["kubernetes/envs/prod/values.yaml"]) != "tunnelport:\n  tunnels: []\n" {
		t.Errorf("the plain file passes through byte for byte and a file on record is left out, got %q", out)
	}
	if !tg.isSecretFile("kubernetes/envs/prod/values.yaml") == false {
		t.Errorf("without rules nothing is a secret file")
	}
}

// A generated value has nowhere to land in a repository without recipients:
// the commit is refused naming the file and the .sops.yaml.
func TestEncryptRefusesAGeneratedValueWithoutSopsConfig(t *testing.T) {
	noSops := fmt.Errorf("giantswarm/teleport-fleet has no %s readable as you (%w)", SopsConfig, gh.ErrNotFound)
	tg := &target{noSops: noSops, exists: map[string]bool{}, files: []sopsenc.File{
		{Path: "kubernetes/envs/prod/token.yaml", Content: []byte("token: GENERATED(x)\n"), Generated: []sopsenc.Generated{{Name: "x", Placeholder: "GENERATED(x)"}}},
	}}
	_, err := encrypt("giantswarm/teleport-fleet", tg)
	if err == nil || !strings.Contains(err.Error(), SopsConfig) || !strings.Contains(err.Error(), "token.yaml") || !errors.Is(err, gh.ErrNotFound) {
		t.Fatalf("a generated value without recipients is refused naming the file and %s, got %v", SopsConfig, err)
	}
}
