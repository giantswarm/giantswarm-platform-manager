package verify

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// noInput is a definition's parsed input that supplies, misses and selects
// nothing.
type noInput struct{}

func (noInput) SuppliedMarkers() map[string]string       { return nil }
func (noInput) SuppliedSecretFields() []string           { return nil }
func (noInput) MissingInputs() []string                  { return nil }
func (noInput) BuiltInDexClientID(string) string         { return "" }
func (noInput) CustomerActions() []render.CustomerAction { return nil }
func (noInput) Selected() map[string]any                 { return nil }
func (noInput) Facts() []render.Fact                     { return nil }

// A definition that stopped rendering an extras Secret file whose
// removals.yaml names it (kind deleted-file): the comparison reads the
// removals, the plan deletes the file and unlists it, and both changes are
// planned — the file whole under the removal's reason, the kustomization's
// entry too — never drift.
func TestCompareReadsADeletedFileRemovalAsPlanned(t *testing.T) {
	const rowanName = "rowan"
	const (
		repo          = "acme/management-clusters"
		dir           = "management-clusters/rowan/extras/agent-platform/secrets/"
		kustomization = dir + "kustomization.yaml"
		old           = dir + "old-credentials.yaml"
		why           = "Removed: the old credentials' Secret is no longer read" // #nosec G101 -- a reason, not a value
		secret        = "apiVersion: v1\nkind: " + kindSecret + "\nmetadata:\n  name: old-credentials\n  namespace: flux-giantswarm\n"
	)
	def := installations.Capability{Name: installations.AgentPlatform,
		Parse: func(any) (render.Input, error) { return noInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{Files: render.Fileset{"giantswarm/acme-management-clusters": {kustomization: {Content: []byte("resources:\n  - new.yaml\n")}}}}, nil
		}}
	onRecord := map[string]string{kustomization: "resources:\n  - new.yaml\n  - old-credentials.yaml\n", old: secret}
	read := func(_ context.Context, repository, path string) (string, error) {
		if c, ok := onRecord[path]; ok && repository == repo {
			return c, nil
		}
		return "", gh.ErrNotFound
	}
	inst := installations.Installation{Name: rowanName, Customer: "acme", Repositories: installations.Repositories{Configs: "acme/configs", ManagementClusters: repo}}
	opts := Options{Definition: def, Installation: inst, Inputs: Inputs{Values: map[string]any{}}, Read: read}
	opts.deleted = definitions.DeletedFiles([]definitions.Removal{{Key: "extras:agent-platform/secrets/old-credentials.yaml", Kind: definitions.RemovalDeletedFile, Reason: why}})
	c, p, err := compare(context.Background(), opts, nil, nil)
	require.NoError(t, err)
	byPath := map[string]plan.Change{}
	for _, f := range p.Files {
		byPath[f.Path] = f.Change
	}
	require.Equal(t, map[string]plan.Change{kustomization: plan.ChangeUpdate, old: plan.ChangeDelete}, byPath)
	require.Equal(t, []plan.Object{{Kind: kindSecret, Namespace: "flux-giantswarm", Name: "old-credentials", Repository: repo}}, p.Checklist)
	for _, f := range []string{kustomization, old} {
		fd := c.files[repo+":"+f]
		require.NotNil(t, fd, f)
		require.Len(t, fd.diffs, 1, f)
		require.Equal(t, why, fd.diffs[0].Planned, "%s: %+v", f, fd.diffs[0])
	}
}
