package plan

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A definition that stopped rendering an extras Secret file: its secrets
// kustomization lists the one file it still renders, while the record holds
// the old file too, listed beside an entry another owner added. A removal of
// kind deleted-file that names the old file deletes it, unlists it from the
// kustomization (the other owner's entry stays) and lists its Secret for a
// person to delete. Without the removal, or where another capability on
// record renders the file, the file and its entry stay and nothing is listed.
func TestBuildDeletesTheFilesARemovalNames(t *testing.T) {
	const (
		dir           = "management-clusters/rowan/extras/agent-platform/secrets/"
		kustomization = dir + "kustomization.yaml"
		old           = dir + "old-credentials.yaml"
		why           = "Removed: the old credentials' Secret is no longer read"
	)
	def := installations.Capability{
		Name:  "files",
		Parse: func(any) (render.Input, error) { return fakeInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{Files: render.Fileset{renderedMCs: {
				kustomization:                {Content: []byte("resources:\n- new-credentials.yaml\n")},
				dir + "new-credentials.yaml": {Content: []byte(secretManifest("new-credentials"))},
			}}}, nil
		},
	}
	onRecord := map[string]string{
		kustomization:                "resources:\n- new-credentials.yaml\n- old-credentials.yaml\n- by-hand.yaml\n",
		dir + "new-credentials.yaml": secretManifest("new-credentials"),
		old:                          secretManifest("old-credentials"),
	}
	read := func(_ context.Context, repository, path string) (string, error) {
		if c, ok := onRecord[path]; ok && repository == acmeMCs {
			return c, nil
		}
		return "", gh.ErrNotFound
	}
	removal := []definitions.DeletedFile{{Path: "agent-platform/secrets/old-credentials.yaml", Reason: why}}
	rendersOld := func(context.Context) ([]Remaining, error) {
		return []Remaining{{Capability: "other", Result: &render.Result{Files: render.Fileset{renderedMCs: {old: {Content: []byte(secretManifest("old-credentials"))}}}}}}, nil
	}
	for _, c := range []struct {
		name      string
		deleted   []definitions.DeletedFile
		remaining func(context.Context) ([]Remaining, error)
		deletes   bool
	}{
		{name: "named by a removal", deleted: removal, deletes: true},
		{name: "no removal names it"},
		{name: "another capability renders it", deleted: removal, remaining: rendersOld},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Build(context.Background(), Options{Definition: def, Installation: rowanInstallation(), Inputs: map[string]any{}, Content: true, Read: read,
				DeletedFiles: c.deleted, Remaining: c.remaining})
			require.Empty(t, p.Refused)
			byPath := map[string]File{}
			for _, f := range p.Files {
				byPath[f.Path] = f
			}
			k := byPath[kustomization]
			if !c.deletes {
				require.NotContains(t, byPath, old)
				require.Equal(t, ChangeUnchanged, k.Change, k.Content)
				require.Empty(t, p.Checklist)
				return
			}
			require.Equal(t, File{Repository: acmeMCs, Path: old, Change: ChangeDelete, Why: why, Current: secretManifest("old-credentials"),
				ListedIn: []string{kustomization + " resources[old-credentials.yaml]"}}, byPath[old])
			require.Equal(t, 1, p.Diff[ChangeDelete])
			require.Equal(t, ChangeUpdate, k.Change)
			require.Equal(t, "resources:\n  - new-credentials.yaml\n  - by-hand.yaml\n", k.Content)
			require.Equal(t, []Kept{{List: ListResources, Entry: "by-hand.yaml"}}, k.Kept)
			require.Equal(t, []Object{{Kind: kindSecret, Namespace: platformNS, Name: "old-credentials", Repository: acmeMCs}}, p.Checklist)
			require.Contains(t, Summary(p), "agent-platform: resources −old-credentials.yaml; removes old-credentials.yaml")
		})
	}
}

func secretManifest(name string) string {
	return "apiVersion: v1\nkind: " + kindSecret + "\nmetadata:\n  name: " + name + "\n  namespace: " + platformNS + "\ntype: Opaque\n"
}
