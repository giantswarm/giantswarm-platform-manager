package tools

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/sopsenc"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
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

// The commit writes what the plan changes beyond the render: the file a
// removal of kind deleted-file deletes, taken out of the tree, and the
// kustomization on record it is unlisted from, written as the plan has it.
// A rendered file and an include's kustomization take their own paths; an
// unchanged file is not written.
func TestCommitCarriesTheFilesThePlanDeletes(t *testing.T) {
	const (
		repo          = "acme/management-clusters"
		dir           = "management-clusters/rowan/extras/agent-platform/secrets/"
		kustomization = dir + "kustomization.yaml"
		old           = dir + "old-credentials.yaml"
		extras        = "management-clusters/rowan/extras/kustomization.yaml"
	)
	inst := installations.Installation{Name: "rowan", Customer: stAcme, Repositories: installations.Repositories{Configs: "acme/configs", ManagementClusters: repo}}
	rendered := render.Fileset{"giantswarm/acme-management-clusters": {dir + "new-credentials.yaml": {}}}
	p := plan.Installation{Name: "rowan", Files: []plan.File{
		{Repository: repo, Path: dir + "new-credentials.yaml", Change: plan.ChangeUpdate},
		{Repository: repo, Path: extras, Change: plan.ChangeUpdate},
		{Repository: repo, Path: kustomization, Change: plan.ChangeUpdate, Content: "resources:\n  - new-credentials.yaml\n"},
		{Repository: repo, Path: old, Change: plan.ChangeDelete},
		{Repository: repo, Path: dir + "same.yaml", Change: plan.ChangeUnchanged},
	}, Includes: []plan.Include{{Repository: repo, Path: extras, List: plan.ListResources, Resource: "./agent-platform/"}}}
	got := unrendered(p, rendered, inst, installations.Installation{})
	if len(got) != 2 || got[0].Path != kustomization || got[1].Path != old {
		t.Fatalf("written beyond the render: %+v", got)
	}
	tg := &target{noSops: gh.ErrNotFound, exists: map[string]bool{}, files: []sopsenc.File{{Path: kustomization, Content: []byte(got[0].Content)}}, deletes: []string{old}}
	out, err := encrypt(repo, tg)
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := out[old]; !ok || c != nil || string(out[kustomization]) != got[0].Content {
		t.Errorf("the deleted file is not taken out of the tree, or the kustomization not written as planned: %q", out)
	}
}
