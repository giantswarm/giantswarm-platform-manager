package verify

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The patches of an invented installation the planned keys are tried on.
const (
	testPlatformPatch = "installations/x/apps/agent-platform/configmap-values.yaml.patch"
	testDexPatch      = "installations/x/apps/dex-app/configmap-values.yaml.patch"
)

// Every key of the agent-platform migrations names a path some golden
// render emits — a leaf, or an entry of a scalar the plan merges as a set —
// a key nothing renders any more is stale and fails here, not on the fleet
// by marking nothing.
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
		fsys := os.DirFS(golden)
		err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			content, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			fd := &fileDiff{path: p, kind: kindOf(p)}
			for i, k := range migs {
				if covered[i] {
					continue
				}
				for leaf, value := range flattenYAML(string(content)) {
					if slices.ContainsFunc(leafPaths(p, leaf, value), func(lp string) bool { return k.covers(fd, lp) }) {
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

// leafPaths are the paths a rendered leaf is named by: its own and, for a
// scalar the plan merges as a comma-separated set, each entry's.
func leafPaths(filePath, yamlPath, value string) []string {
	out := []string{yamlPath}
	if plan.JoinedList(filePath, yamlPath) {
		for _, id := range plan.SplitJoined(value) {
			out = append(out, entryPath(yamlPath, id))
		}
	}
	return out
}

// A scalar the plan merges as a comma-separated set — the kagent UI's
// oidc-extra-audience — is planned when the record's entries are all
// rendered and every entry the render adds is named, as an entry of the
// scalar, by a migration key: the reasons joined, each once. An added entry
// no key names keeps the difference (by input or drift); a removed entry
// keeps it as well; the scalar the record lacks is the scalar's own key's.
// A file the migrations name as a whole — the mcp-* Valkey Secret — is
// planned at every leaf, the kustomization entry and the oauth Secret's
// password with it.
func TestJoinedScalarsAndTheValkeySecretArePlanned(t *testing.T) {
	const m5, m5auth, m9, m30 = "M5 — kagent", "M5 — authenticator", "M9 — valkey", "M30 — backstage"
	const list = plan.ListExtraAudience
	migs := readMigrations([]definitions.Migration{
		{Key: "configmap:" + list + "[kagent]", Reason: m5},
		{Key: "configmap:" + list, Reason: m5},
		{Key: "configmap:" + list + "[dex-k8s-authenticator]", Reason: m5auth},
		{Key: "configmap:" + list + "[backstage]", Reason: m30},
		{Key: "extras:mcp-<name>/valkey-credentials.enc.yaml", Reason: m9},
		{Key: "extras:mcp-<name>/kustomization.yaml resources[valkey-credentials.enc.yaml]", Reason: m9},
		{Key: "extras:mcp-<name>/oauth-credentials.enc.yaml stringData.VALKEY_PASSWORD", Reason: m9},
		{Key: "extras:mcp-<name>/oauth-credentials.enc.yaml type", Reason: m9},
	})
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	dexPatch := &fileDiff{path: testDexPatch, kind: definitions.KindDexSecret}
	valkey := &fileDiff{path: "management-clusters/x/extras/mcp-capi/valkey-credentials.enc.yaml", kind: definitions.KindExtras}
	kust := &fileDiff{path: "management-clusters/x/extras/mcp-capi/kustomization.yaml", kind: definitions.KindExtras}
	oauth := &fileDiff{path: "management-clusters/x/extras/mcp-capi/oauth-credentials.enc.yaml", kind: definitions.KindExtras}
	scalar := func(rendered, current string) *Difference {
		return &Difference{Path: list, Rendered: rendered, Current: current, Input: "portal"}
	}
	absent := func(p string) *Difference { return &Difference{Path: p, Rendered: Redacted, absent: true} }
	for _, tc := range []struct {
		name string
		fd   *fileDiff
		d    *Difference
		want string
	}{
		{"only planned additions, the reasons joined once each", patch, scalar("dex-k8s-authenticator,kagent,portal,backstage", "portal"), m5auth + "; " + m5 + "; " + m30},
		{"one planned addition", patch, scalar("kagent,portal", "portal"), m5},
		{"the record's own entries stay", patch, scalar("kagent,portal,own", "portal,own"), m5},
		{"an addition no key names", patch, scalar("kagent,portal,other", "portal"), ""},
		{"a removed entry", patch, scalar("kagent,portal", "portal,gone"), ""},
		{"the same set in another order", patch, scalar("kagent,portal", "portal,kagent"), ""},
		{"the scalar's key does not name an entry", patch, scalar("kagent,portal,other", "kagent,portal"), ""},
		{"the scalar the record lacks", patch, &Difference{Path: list, Rendered: "kagent,portal,other", absent: true}, m5},
		{"a scalar of another file", dexPatch, scalar("kagent,portal", "portal"), ""},
		{"the Valkey Secret created", valkey, absent(""), m9},
		{"every leaf of the Valkey Secret", valkey, absent("type"), m9},
		{"its password", valkey, absent("stringData.default"), m9},
		{"its kustomization entry", kust, absent("resources[valkey-credentials.enc.yaml]"), m9},
		{"the oauth Secret's password", oauth, absent("stringData.VALKEY_PASSWORD"), m9},
		{"the oauth Secret's type, added with the password", oauth, &Difference{Path: "type", Rendered: "Opaque", absent: true}, m9},
		{"the oauth Secret's other leaves", oauth, absent("stringData.DEX_CLIENT_SECRET"), ""},
	} {
		if got := planned(tc.fd, tc.d, nil, migs); got != tc.want {
			t.Errorf("%s: %s#%s planned %q, want %q", tc.name, tc.fd.path, tc.d.Path, got, tc.want)
		}
	}
}
