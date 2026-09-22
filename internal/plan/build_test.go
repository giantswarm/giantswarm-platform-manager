package plan

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// fakeInput is a definition's parsed input that supplies, misses and
// selects nothing.
type fakeInput struct{}

func (fakeInput) SuppliedMarkers() map[string]string       { return nil }
func (fakeInput) SuppliedSecretFields() []string           { return nil }
func (fakeInput) MissingInputs() []string                  { return nil }
func (fakeInput) BuiltInDexClientID(string) string         { return "" }
func (fakeInput) CustomerActions() []render.CustomerAction { return nil }
func (fakeInput) Selected() map[string]any                 { return nil }

// rowanKustomization is the kustomization the fake definition's include
// lands in.
const rowanKustomization = "installations/rowan/kustomization.yaml"

// filesDefinition is a definition that renders files under the customer's
// configs repository and lists one include in a kustomization there.
func filesDefinition(files map[string]string) installations.Capability {
	return installations.Capability{
		Name:  "files",
		Parse: func(any) (render.Input, error) { return fakeInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			fs := map[string]render.File{}
			for path, content := range files {
				fs[path] = render.File{Content: []byte(content)}
			}
			return &render.Result{
				Files:    render.Fileset{renderedConfigs: fs},
				Includes: []render.Include{{Repository: renderedConfigs, Path: rowanKustomization, Resource: "extras"}},
			}, nil
		},
	}
}

// The plan reads every file it compares against at once — the rendered
// files' current content and the kustomization its include lands in — not
// one round trip after another: a reader that holds every read until all of
// them have started sees them all start, and the plan is the same as before.
func TestBuildReadsThePlansFilesAtOnce(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		files["installations/rowan/"+n+".yaml"] = "key: " + n + "\n"
	}
	const reads = 8 // seven files and the kustomization
	var (
		mu      sync.Mutex
		seen    = map[string]int{}
		started = make(chan struct{}, reads)
		release = make(chan struct{})
	)
	read := func(ctx context.Context, repository, path string) (string, error) {
		mu.Lock()
		seen[repository+":"+path]++
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		switch path {
		case rowanKustomization:
			return "resources:\n- other\n", nil
		case "installations/rowan/a.yaml":
			return "key: a\n", nil
		case "installations/rowan/b.yaml":
			return "", gh.ErrNotFound
		}
		return "key: old\n", nil
	}
	go func() {
		for range reads {
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Error("the plan's reads did not all start together: it read one file after another")
				close(release)
				return
			}
		}
		close(release)
	}()
	inst := installations.Installation{Name: "rowan", Customer: "acme", Repositories: installations.Repositories{Configs: "acme/configs", ManagementClusters: "acme/management-clusters"}}
	p := Build(context.Background(), Options{Definition: filesDefinition(files), Installation: inst, Inputs: map[string]any{}, Content: true, Read: read})
	if p.Refused != "" {
		t.Fatalf("refused: %s", p.Refused)
	}
	if len(seen) != reads {
		t.Errorf("read %d files, want %d: %v", len(seen), reads, seen)
	}
	for key, n := range seen {
		if n != 1 {
			t.Errorf("%s read %d times", key, n)
		}
	}
	want := map[string]Change{"installations/rowan/a.yaml": ChangeUnchanged, "installations/rowan/b.yaml": ChangeCreate, "installations/rowan/c.yaml": ChangeUpdate, rowanKustomization: ChangeUpdate}
	for _, f := range p.Files {
		if f.Repository != "acme/configs" {
			t.Errorf("%s in %s, want the configs repository on record", f.Path, f.Repository)
		}
		if c, ok := want[f.Path]; ok && f.Change != c {
			t.Errorf("%s: %s, want %s (%s)", f.Path, f.Change, c, f.Error)
		}
	}
	if len(p.Files) != reads {
		t.Errorf("%d files in the plan, want %d", len(p.Files), reads)
	}
}

// A context over before the files are fetched is every file's answer: the
// plan says why each could not be read, and the reader is not asked.
func TestBuildFetchStopsWithTheContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	read := func(context.Context, string, string) (string, error) {
		t.Error("read after the context was over")
		return "", nil
	}
	f := fetch(ctx, read, []fileRef{{"r", "p"}, {"r", "p"}, {"r", "q"}})
	for _, path := range []string{"p", "q"} {
		if _, err := f.read(ctx, "r", path); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, err := f.read(ctx, "r", "never"); err == nil {
		t.Error("a file the plan did not name read without an error")
	}
}
