package verify

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The patches of an invented installation the planned keys are tried on.
const (
	testPlatformPatch = "installations/x/apps/agent-platform/configmap-values.yaml.patch"
	testDexPatch      = "installations/x/apps/dex-app/configmap-values.yaml.patch"
	// testDexSecretPatch is the dex-app's secret patch, testComponentAppConfig
	// the platform Component's app-config fragment in a portal's tree.
	testDexSecretPatch     = "installations/x/apps/dex-app/secret-values.yaml.patch"
	testComponentAppConfig = "management-clusters/x/extras/backstage/agent-platform/app-config.yaml"
	testDomain             = "x.example.test"
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
	migs := readMigrations(ms, nil)
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
	}, nil)
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	dexPatch := &fileDiff{path: testDexPatch, kind: definitions.KindDexConfigMap}
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

// The installation's own three MCP servers on record (mcp-kubernetes,
// mcp-prometheus, mcp-capi) repeat what the shared defaults register — in
// their in-cluster form, or on the installation's own base domain: their
// removal names the server and is no migration. Any other entry of the list
// is M19, as is the list itself: a foreign server, a private target's tunnel
// host, and a target's public URL a hub still carries by hand — a server on
// the target's domain, however own it looks. Without a base domain on record
// no public entry is the installation's own.
func TestOwnMCPServersAreNotM19(t *testing.T) {
	rs, err := definitions.Removals(installations.AgentPlatform)
	if err != nil {
		t.Fatal(err)
	}
	rms, noDomain := readRemovals(rs, facts{factDomain: testDomain}), readRemovals(rs, nil)
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	const list = "agent-platform-mcps.mcpServers"
	for _, tc := range []struct {
		name, path, names string
		keys              plannedKeys
		m19               bool
	}{
		{"own mcp-kubernetes on the installation's domain", list + "[https://mcp-kubernetes." + testDomain + "/mcp].url", "this mcp-kubernetes entry at mcp-kubernetes." + testDomain, rms, false},
		{"own mcp-prometheus in the cluster", list + "[http://mcp-prometheus.mcp-prometheus.svc:8080/mcp].timeout", "this mcp-prometheus entry repeats the in-cluster one", rms, false},
		{"own mcp-capi's auth mode", list + "[https://mcp-capi." + testDomain + "/mcp].auth.mode", "this mcp-capi entry at mcp-capi." + testDomain, rms, false},
		{"a target's public URL on a hub", list + "[https://mcp-kubernetes.y.example.test/mcp].url", "", rms, true},
		{"a foreign server", list + "[http://mcp-foo.mcp-foo.svc:8080/mcp].url", "", rms, true},
		{"a private target's tunnel host", list + "[https://mcp-kubernetes-y.agent-platform.svc.cluster.local:8443/mcp].url", "", rms, true},
		{"the list itself", list, "", rms, true},
		{"no base domain on record: a public entry is not own", list + "[https://mcp-kubernetes." + testDomain + "/mcp].url", "", noDomain, true},
		{"no base domain on record: the in-cluster entry still is", list + "[http://mcp-capi.mcp-capi.svc:8080/mcp].url", "this mcp-capi entry repeats the in-cluster one", noDomain, false},
	} {
		got := tc.keys.reason(patch, tc.path)
		if got == "" {
			t.Errorf("%s: %s names no removal", tc.name, tc.path)
			continue
		}
		if isM19 := strings.HasSuffix(got, "· M19"); isM19 != tc.m19 {
			t.Errorf("%s: %s planned %q, want M19 %v", tc.name, tc.path, got, tc.m19)
		}
		if tc.names != "" && !strings.Contains(got, tc.names) {
			t.Errorf("%s: %s planned %q, want it to say %q", tc.name, tc.path, got, tc.names)
		}
	}
}

// A key's placeholders are filled into its reason from the path they
// matched: the mcp-* server's name from its extras directory, a Dex
// client's from the map key or the Secret's file name, a ReferenceGrant's
// namespace from the object's identity, a fact's placeholder with the fact.
// A placeholder the key declares twice stands for one value, so a Secret
// of one server in another's directory is nobody's; a <x> the key does not
// declare stays as it is.
func TestPlaceholdersFillTheReason(t *testing.T) {
	migs := readMigrations([]definitions.Migration{
		{Key: "extras:mcp-<name>/valkey-credentials.enc.yaml", Reason: "the mcp-<name> server's Valkey"},
		{Key: "extras:mcp-<name>/kustomization.yaml resources[dex-client-mcp-<name>-secret.yaml]", Reason: "the mcp-<name> server's Dex client Secret"},
		{Key: "extras:agent-platform/secrets/dex-client-<name>-secret.yaml", Reason: "the <name> Dex client's secret"},
		{Key: "dex-configmap:oidc.staticClients.<name>.clientSecretRef", Reason: "the Dex client <name>"},
		{Key: "configmap:extraObjects[ReferenceGrant/<namespace>/agentgateway-jwks-dex]", Reason: "a ReferenceGrant in <namespace>"},
		{Key: "configmap:muster.muster.oauth.server.tokenExchangeBroker.targets.<name>.clientCredentialsSecretRef", Reason: "the target <name> (<other> stays)"},
	}, nil)
	rms := readRemovals([]definitions.Removal{
		{Key: "configmap:agent-platform-mcps.mcpServers[<scheme>://mcp-capi.<domain>/mcp]", Reason: "mcp-capi at mcp-capi.<domain> over <scheme>"},
	}, facts{factDomain: testDomain})
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	dexCM := &fileDiff{path: testDexPatch, kind: definitions.KindDexConfigMap}
	valkey := &fileDiff{path: "management-clusters/x/extras/mcp-prometheus/valkey-credentials.enc.yaml", kind: definitions.KindExtras}
	mcpKust := &fileDiff{path: "management-clusters/x/extras/mcp-prometheus/kustomization.yaml", kind: definitions.KindExtras}
	secret := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/dex-client-muster-secret.yaml", kind: definitions.KindExtras}
	absent := func(p string) *Difference { return &Difference{Path: p, Rendered: "x", absent: true} }
	for _, tc := range []struct {
		name string
		fd   *fileDiff
		d    *Difference
		want string
	}{
		{"the server from its directory", valkey, absent("stringData.default"), "the mcp-prometheus server's Valkey"},
		{"the server from the directory and the entry", mcpKust, absent("resources[dex-client-mcp-prometheus-secret.yaml]"), "the mcp-prometheus server's Dex client Secret"},
		{"another server's Secret in the directory is nobody's", mcpKust, absent("resources[dex-client-mcp-kubernetes-secret.yaml]"), ""},
		{"the client from the file name", secret, absent(""), "the muster Dex client's secret"},
		{"the client from the map key", dexCM, absent("oidc.staticClients.mcpCapi.clientSecretRef.name"), "the Dex client mcpCapi"},
		{"the namespace from the identity", patch, absent("extraObjects[ReferenceGrant/dex/agentgateway-jwks-dex].kind"), "a ReferenceGrant in dex"},
		{"an undeclared placeholder stays", patch, absent("muster.muster.oauth.server.tokenExchangeBroker.targets.y.clientCredentialsSecretRef"), "the target y (<other> stays)"},
		{"a fact and a part of the identity", patch, &Difference{Path: "agent-platform-mcps.mcpServers[https://mcp-capi." + testDomain + "/mcp].url", Current: "x"}, "mcp-capi at mcp-capi." + testDomain + " over https"},
	} {
		if got := planned(tc.fd, tc.d, rms, migs); got != tc.want {
			t.Errorf("%s: %s#%s planned %q, want %q", tc.name, tc.fd.path, tc.d.Path, got, tc.want)
		}
	}
}

// Every placeholder a reason references is one its key declares, so the
// comparison fills it: a <x> left in a reason would reach the Dev Portal as
// it is. The keys are read with a base domain, so one with <domain> is
// parsed too.
func TestEveryReasonPlaceholderIsDeclared(t *testing.T) {
	ref := regexp.MustCompile(`<([^<>\s]+)>`)
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range caps {
		rs, err := definitions.Removals(capability)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := definitions.Migrations(capability)
		if err != nil {
			t.Fatal(err)
		}
		keys := map[string]string{}
		for _, r := range rs {
			keys[r.Key] = r.Reason
		}
		for _, m := range ms {
			keys[m.Key] = m.Reason
		}
		for key, reason := range keys {
			k, ok := parseKey(key, reason, facts{factDomain: testDomain})
			if !ok {
				continue
			}
			for _, m := range ref.FindAllStringSubmatch(reason, -1) {
				if !slices.Contains(k.names, m[1]) {
					t.Errorf("%s: %q references <%s>, which its key %q does not declare", capability, reason, m[1], key)
				}
			}
		}
	}
}
