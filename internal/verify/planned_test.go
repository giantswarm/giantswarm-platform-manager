package verify

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// Every key of the agent-platform migrations names a path some golden
// render emits: a key nothing renders any more is stale and fails here, not
// on the fleet by marking nothing.
func TestEveryMigrationKeyIsRendered(t *testing.T) {
	ms, err := definitions.Migrations(installations.AgentPlatform)
	if err != nil {
		t.Fatal(err)
	}
	migs := readMigrations(ms)
	if len(migs) != len(ms) {
		t.Fatalf("%d of %d migration keys name a file kind the comparison observes", len(migs), len(ms))
	}
	covered := make([]bool, len(migs))
	shapes, err := filepath.Glob(filepath.Join("..", "..", "render", "agentplatform", "testdata", "*", "golden"))
	if err != nil || len(shapes) == 0 {
		t.Fatalf("golden shapes: %v (%d)", err, len(shapes))
	}
	for _, golden := range shapes {
		err := filepath.WalkDir(golden, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			content, err := os.ReadFile(p) // #nosec G304 -- a golden file of the render's testdata
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(p, golden+string(filepath.Separator))
			fd := &fileDiff{path: filepath.ToSlash(rel), kind: kindOf(filepath.ToSlash(rel))}
			for i, k := range migs {
				if covered[i] {
					continue
				}
				for leaf := range flattenYAML(string(content)) {
					if k.covers(fd, leaf) {
						covered[i] = true
						break
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, ok := range covered {
		if !ok {
			t.Errorf("migrations.yaml: %q (%s) names nothing the goldens render", ms[i].Key, ms[i].Reason)
		}
	}
}
