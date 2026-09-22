package verify

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// A file dimension's key names files and YAML paths. A word with a slash
// or a file suffix names a file by its observed path, a directory above it
// or its name at any depth, <x> a part of a name; every other word is a
// YAML path covering everything beneath it, [*] any list entry (a trailing
// one, like .*, the path itself), [x] one entry, <x> any map key or part of
// one. " / " separates alternatives sharing the file named before them; a
// remark in parentheses is not read; within an alternative a file word and
// a path word must both hit, and more words are more specific. <domain>
// stands for the installation's own base domain, never for any name: a word
// with it names nothing on an installation without the fact. A catch-all's
// and a live dimension's key is prose.
func TestMatcherReadsKeys(t *testing.T) {
	m := func(kind, key string) matcher { return newMatcher(definitions.Dimension{Kind: kind, Key: key}, nil) }
	hits := func(t *testing.T, k matcher, rel string, paths ...string) {
		t.Helper()
		for _, p := range paths {
			if k.match(rel, p) == 0 {
				t.Errorf("%q does not name %s#%s", k.dim.Key, rel, p)
			}
		}
	}
	misses := func(t *testing.T, k matcher, rel string, paths ...string) {
		t.Helper()
		for _, p := range paths {
			if k.match(rel, p) != 0 {
				t.Errorf("%q names %s#%s", k.dim.Key, rel, p)
			}
		}
	}
	const appConfig, userValues, secrets = "backstage/app-config.yaml", "backstage/user-values.yaml", "backstage/user-secrets.enc.yaml"

	k := m(definitions.KindBackstage, "app-config.yaml app.title / app.baseUrl / organization")
	if got := k.words(); !reflect.DeepEqual(got, []string{"app-config.yaml", "app.title", "app.baseUrl", "organization"}) {
		t.Errorf("words %v", got)
	}
	hits(t, k, appConfig, "app.title", "app.baseUrl", "organization", "organization.name")
	misses(t, k, appConfig, "app", "app.titleX", "app.routes")
	misses(t, k, userValues, "app.title")

	whole, part := m(definitions.KindBackstage, "user-secrets.enc.yaml"), m(definitions.KindBackstage, "user-secrets.enc.yaml dexAuthCredentials.<name>")
	hits(t, whole, secrets, "stringData.values", "dexAuthCredentials.maple", "")
	misses(t, whole, appConfig, "stringData.values")
	hits(t, part, secrets, "dexAuthCredentials.maple", "dexAuthCredentials.maple.clientId")
	misses(t, part, secrets, "authSessionSecret", "dexAuthCredentials")
	if whole.match(secrets, "dexAuthCredentials.maple") >= part.match(secrets, "dexAuthCredentials.maple") {
		t.Error("a file word alone is less specific than the file with a path")
	}

	dir, file := m(definitions.KindExtras, "mcp-<name>/"), m(definitions.KindExtras, "mcp-<name>/kustomization.yaml")
	hits(t, dir, "mcp-capi/oauth-credentials.enc.yaml", "stringData.x", "")
	hits(t, dir, "mcp-capi/kustomization.yaml", "resources[a.yaml]")
	misses(t, dir, "agent-platform/secrets/kustomization.yaml", "resources[a.yaml]")
	if dir.match("mcp-capi/kustomization.yaml", "kind") >= file.match("mcp-capi/kustomization.yaml", "kind") {
		t.Error("a directory is less specific than a file in it")
	}
	sub := m(definitions.KindBackstage, "backstage/kustomization.yaml patches[0]")
	hits(t, sub, "backstage/kustomization.yaml", "patches[0].patch", "patches[0].target.kind")
	misses(t, sub, "backstage/kustomization.yaml", "patches[1].patch", "resources[a]")
	misses(t, sub, "kustomization.yaml", "patches[0].patch")
	hits(t, m(definitions.KindBackstage, "kustomization.yaml resources"), "kustomization.yaml", "resources[./backstage/]")
	hits(t, m(definitions.KindBackstage, "kustomization.yaml resources"), "backstage/kustomization.yaml", "resources[app-config.yaml]")

	patch := m(definitions.KindConfigMap, "agent-platform/secret-values.yaml.patch")
	hits(t, patch, "agent-platform/secret-values.yaml.patch", "a")
	misses(t, patch, "agent-platform/configmap-values.yaml.patch", "a")

	k = m(definitions.KindBackstage, "kubernetes.clusterLocatorMethods[*].clusters[*]")
	hits(t, k, appConfig, "kubernetes.clusterLocatorMethods[0].clusters[0].url", "kubernetes.clusterLocatorMethods[0].clusters")
	misses(t, k, appConfig, "kubernetes.clusterLocatorMethods[0].type", "kubernetes.clusterLocatorMethods.clusters", "kubernetes.serviceLocatorMethod")
	if k.match(appConfig, "kubernetes.clusterLocatorMethods[0].clusters[0].url") <= m(definitions.KindBackstage, "kubernetes").match(appConfig, "kubernetes.clusterLocatorMethods[0].clusters[0].url") {
		t.Error("the longer path word is more specific")
	}
	k = m(definitions.KindDexConfigMap, "oidc.extraStaticClients[backstage]")
	hits(t, k, "dex-app/configmap-values.yaml.patch", "oidc.extraStaticClients[backstage].id")
	misses(t, k, "dex-app/configmap-values.yaml.patch", "oidc.extraStaticClients[kagent].id", "oidc.extraStaticClients")
	k = m(definitions.KindBackstage, "gs.installations.<name> / auth.providers.oidc-<name> / valkey.*")
	hits(t, k, appConfig, "gs.installations.maple.x", "auth.providers.oidc-maple.clientId", "valkey", "valkey.valkey.auth")
	misses(t, k, appConfig, "gs.installations[0].x", "auth.providers.github", "gs.installations")
	k = m(definitions.KindConfigMap, "agent-platform-mcps.mcpServers[http://<name>.svc:8080/mcp]")
	if len(k.alts) != 1 || len(k.alts[0].files) != 0 {
		t.Errorf("an index with a slash is a path word, got %+v", k.alts)
	}
	hits(t, k, "agent-platform/configmap-values.yaml.patch", "agent-platform-mcps.mcpServers[http://mcp-capi.mcp-capi.svc:8080/mcp].url")
	misses(t, k, "agent-platform/configmap-values.yaml.patch", "agent-platform-mcps.mcpServers[https://mcp-capi.other.example/mcp].url", "agent-platform-mcps.mcpServers")

	own := facts{factDomain: "rowan.acme.test"}
	k = newMatcher(definitions.Dimension{Kind: definitions.KindConfigMap, Key: "agent-platform-mcps.mcpServers[http://<name>.svc:8080/mcp] / agent-platform-mcps.mcpServers[<scheme>://mcp-<name>.<domain>/mcp]"}, own)
	hits(t, k, "agent-platform/configmap-values.yaml.patch", "agent-platform-mcps.mcpServers[https://mcp-prometheus.rowan.acme.test/mcp].url", "agent-platform-mcps.mcpServers[http://mcp-capi.mcp-capi.svc:8080/mcp].url")
	misses(t, k, "agent-platform/configmap-values.yaml.patch", "agent-platform-mcps.mcpServers[https://mcp-prometheus.birch.acme.test/mcp].url")
	k = newMatcher(definitions.Dimension{Kind: definitions.KindConfigMap, Key: "agent-platform-mcps.mcpServers[http://<name>.svc:8080/mcp] / agent-platform-mcps.mcpServers[<scheme>://mcp-<name>.<domain>/mcp]"}, nil)
	hits(t, k, "agent-platform/configmap-values.yaml.patch", "agent-platform-mcps.mcpServers[http://mcp-capi.mcp-capi.svc:8080/mcp].url")
	misses(t, k, "agent-platform/configmap-values.yaml.patch", "agent-platform-mcps.mcpServers[https://mcp-prometheus.rowan.acme.test/mcp].url")
	if len(k.words()) != 2 {
		t.Errorf("a word with a fact the installation lacks names nothing but stays a word: %v", k.words())
	}

	k = m(definitions.KindBackstage, "kustomization.yaml components (./agent-platform/, the platform's Component)")
	if got := k.words(); !reflect.DeepEqual(got, []string{"kustomization.yaml", "components"}) {
		t.Errorf("a remark is not read: %v", got)
	}
	hits(t, k, "kustomization.yaml", "components[./agent-platform/]")
	misses(t, k, "kustomization.yaml", "resources[./backstage/]")

	for _, d := range []definitions.Dimension{
		{Kind: definitions.KindConfigMap, CatchAll: true, Key: "the patch's top-level keys"},
		{Kind: definitions.KindLive, Key: "HelmRelease backstage Ready in flux-giantswarm"},
	} {
		if k := newMatcher(d, nil); len(k.alts) != 0 || len(k.words()) != 0 || k.match("kustomization.yaml", "the") != 0 {
			t.Errorf("%q: prose names nothing, got %v", d.Key, k.words())
		}
	}
}

// A rendered file's kind and observed path: the dex-app's two patches are
// two kinds, a patch is observed under apps/, an extras file under extras/,
// a backstage file under extras/backstage/, a file elsewhere whole.
func TestKindOfAndObservedPath(t *testing.T) {
	for _, tc := range []struct{ path, kind, rel string }{
		{"installations/x/apps/dex-app/configmap-values.yaml.patch", definitions.KindDexConfigMap, "dex-app/configmap-values.yaml.patch"},
		{"installations/x/apps/dex-app/secret-values.yaml.patch", definitions.KindDexSecret, "dex-app/secret-values.yaml.patch"},
		{"installations/x/apps/agent-platform/configmap-values.yaml.patch", definitions.KindConfigMap, "agent-platform/configmap-values.yaml.patch"},
		{"management-clusters/x/extras/agent-platform/secrets/kustomization.yaml", definitions.KindExtras, "agent-platform/secrets/kustomization.yaml"},
		{"management-clusters/x/extras/mcp-capi/user-values.yaml", definitions.KindExtras, "mcp-capi/user-values.yaml"},
		{"kubernetes/envs/prod/values.yaml", definitions.KindExtras, "kubernetes/envs/prod/values.yaml"},
		{"management-clusters/x/extras/backstage/kustomization.yaml", definitions.KindBackstage, "kustomization.yaml"},
		{"management-clusters/x/extras/backstage/backstage/user-values.yaml", definitions.KindBackstage, "backstage/user-values.yaml"},
		{"management-clusters/x/extras/backstage/agent-platform/app-config.yaml", definitions.KindBackstage, "agent-platform/app-config.yaml"},
	} {
		if kind := kindOf(tc.path); kind != tc.kind {
			t.Errorf("%s: kind %q, want %q", tc.path, kind, tc.kind)
		}
		if rel := observedPath(tc.kind, tc.path); rel != tc.rel {
			t.Errorf("%s: observed at %q, want %q", tc.path, rel, tc.rel)
		}
	}
}

// matchersOf are the matchers of a definition's file dimensions under the
// installation's facts.
func matchersOf(feats []definitions.Feature, own facts) []matcher {
	var out []matcher
	for _, f := range feats {
		for _, d := range f.Dimensions {
			if d.Kind != definitions.KindLive {
				out = append(out, newMatcher(d, own))
			}
		}
	}
	return out
}

// Every file dimension of every definition names leaves — its key has a
// file or path word, which under the grammar every word outside a remark
// is, so only an empty or all-remark key fails here — or is the declared
// catch-all of its kind, of which a kind has at most one; ids are unique.
// Whether the words name leaves the definition renders is
// TestEveryRenderedLeafHasADimension's.
func TestEveryDimensionNamesLeavesOrIsTheCatchAll(t *testing.T) {
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range caps {
		feats, err := definitions.Features(c)
		if err != nil {
			t.Fatal(err)
		}
		ids, catchAlls := map[string]bool{}, map[string]string{}
		for _, f := range feats {
			for _, d := range f.Dimensions {
				if ids[d.ID] {
					t.Errorf("%s: dimension %s twice", c, d.ID)
				}
				ids[d.ID] = true
				if d.Kind == definitions.KindLive {
					continue
				}
				m := newMatcher(d, nil)
				switch {
				case d.CatchAll && catchAlls[d.Kind] != "":
					t.Errorf("%s: %s and %s are both the catch-all of kind %s", c, catchAlls[d.Kind], d.ID, d.Kind)
				case d.CatchAll:
					catchAlls[d.Kind] = d.ID
				case len(m.words()) == 0:
					t.Errorf("%s: %s (%s) names nothing: %q is prose and the dimension is no catch-all", c, d.ID, d.Kind, d.Key)
				}
			}
		}
	}
}

// The portal's kustomization and values leaves reach their dimensions, the
// cluster locator's leaves the dimension whose key has [*], and every leaf
// of the dex patch the one dimension of its kind, the catch-all. A leaf no
// dimension names is routed nowhere, for assign to report.
func TestRoutesThePortalsLeaves(t *testing.T) {
	feats, err := definitions.Features("customer-portal")
	if err != nil {
		t.Fatal(err)
	}
	matchers := matchersOf(feats, nil)
	const portal = "management-clusters/maple/extras/backstage/"
	fd := func(p string) *fileDiff { return &fileDiff{path: p, kind: kindOf(p)} }
	for _, tc := range []struct{ file, path, want string }{
		{portal + "backstage/kustomization.yaml", "patches[0].patch", "chart-line"},
		{portal + "backstage/kustomization.yaml", "patches[1].patch", "values-sources"},
		{portal + "backstage/kustomization.yaml", "resources[dex-client-backstage-secret.enc.yaml]", "extras-kustomization"},
		{portal + "kustomization.yaml", "resources[./backstage/]", "extras-kustomization"},
		{portal + "kustomization.yaml", "components[./agent-platform/]", "platform-component"},
		{portal + "backstage/user-values.yaml", "route.hostnames[portal.maple.example.test]", "route"},
		{portal + "backstage/user-values.yaml", "backstage.extraVolumeMounts[tunnelport-spiffe-bundle].mountPath", "tunnel-mount"},
		{portal + "backstage/user-values.yaml", "metadata.name", "values-configmaps"},
		{portal + "backstage/app-config.yaml", "kubernetes.clusterLocatorMethods[0].clusters[0].url", "kubernetes-cluster"},
		{portal + "backstage/app-config.yaml", "kubernetes.serviceLocatorMethod.type", "kubernetes-cluster"},
		{portal + "backstage/app-config.yaml", "grafana.domain", "plugins"},
		{portal + "backstage/app-config.yaml", "backend.auth.pluginKeyStore.type", "plugin-keys"},
		{portal + "backstage/app-config.yaml", "backend.csp.report-uri", "app-telemetry"},
		{portal + "backstage/app-config.yaml", "backend.csp.default-src", "backend"},
		{portal + "backstage/app-config.yaml", "gs.installations.maple.baseDomain", "gs-homepage-and-installation"},
		{portal + "backstage/user-secrets.enc.yaml", "authSessionSecret", "session-secret"},
		{portal + "backstage/user-secrets.enc.yaml", "dexAuthCredentials.maple", "dex-client-values"},
		{portal + "backstage/user-secrets.enc.yaml", "stringData.values", "user-secrets"},
		{portal + "backstage/tunnelport-spiffe-bundle.yaml", "[Secret/backstage/tunnelport-spiffe-bundle].type", "tunnel-bundle"},
		{"installations/maple/apps/dex-app/configmap-values.yaml.patch", "oidc.extraStaticClients[backstage].id", "dex-client-entry"},
		{"installations/maple/apps/dex-app/configmap-values.yaml.patch", "ingress.enabled", "dex-client-entry"},
	} {
		got := route(matchers, fd(tc.file), tc.path)
		if got == nil || got.ID != tc.want {
			id := "<nothing>"
			if got != nil {
				id = got.ID
			}
			t.Errorf("%s#%s is observed under %s, want %s", tc.file, tc.path, id, tc.want)
		}
	}
	if d := route(matchers, fd(portal+"backstage/user-values.yaml"), "handEdited"); d != nil {
		t.Errorf("a leaf no dimension names is observed under %s", d.ID)
	}
}

// A leaf no dimension of its kind names, where the kind has no catch-all,
// is reported under the dimension of OtherFeature for the kind instead of
// dropped: a difference marks it, a choice not on record alone makes it not
// checked with the choice named; a kind whose every leaf is named has no
// such dimension.
func TestUnnamedLeavesAreReportedUnderOther(t *testing.T) {
	const values, dex = "management-clusters/x/extras/backstage/backstage/user-values.yaml", "installations/x/apps/dex-app/configmap-values.yaml.patch"
	feats := []definitions.Feature{{ID: "portal", Dimensions: []definitions.Dimension{
		{ID: "route", Kind: definitions.KindBackstage, Key: "user-values.yaml route"},
		{ID: "clients", Kind: definitions.KindDexConfigMap, CatchAll: true, Key: "the portal's client"},
	}}}
	vd := &fileDiff{key: "r:" + values, path: values, kind: definitions.KindBackstage,
		diffs:   []Difference{{File: "r:" + values, Path: "handEdited", Current: "x"}},
		missing: map[string][]string{"backstage.image": {"portal.image"}}}
	dd := &fileDiff{key: "c:" + dex, path: dex, kind: definitions.KindDexConfigMap, diffs: []Difference{{File: "c:" + dex, Path: "ingress.enabled", Current: "true"}}}
	files := map[string]*fileDiff{vd.key: vd, dd.key: dd}
	dims, others := assign(&comparison{files: files}, feats, "", nil)
	if d := dims["clients"]; d.Mark != Drifted || len(d.Differences) != 1 {
		t.Errorf("the declared catch-all: %+v", *d)
	}
	if d := dims["route"]; d.Mark != AsDefined || d.Reason != "" {
		t.Errorf("the dimension beside the unnamed leaf: %+v", *d)
	}
	if len(others) != 1 || others[0].ID != OtherFeature+"-"+definitions.KindBackstage || others[0].Kind != definitions.KindBackstage {
		t.Fatalf("other dimensions %+v", others)
	}
	if d := others[0]; d.Mark != Drifted || len(d.Differences) != 1 || d.Differences[0].Path != "handEdited" || !reflect.DeepEqual(d.Files, []string{vd.key}) {
		t.Errorf("the unnamed difference: %+v", *d)
	}
	vd.diffs = nil
	if _, others = assign(&comparison{files: files}, feats, "", nil); len(others) != 1 || others[0].Mark != NotChecked || others[0].Reason != ReasonMissingChoice+": portal.image" {
		t.Errorf("the unnamed choice: %+v", others)
	}
	vd.missing = nil
	if _, others = assign(&comparison{files: files}, feats, "", nil); len(others) != 0 {
		t.Errorf("nothing unnamed, yet %+v", others)
	}
}

// Every leaf the definitions render is observed under a dimension: every
// file of every golden of the render packages, with the YAML text a
// ConfigMap's data or a Secret's stringData holds decoded to its own leaves
// the way the comparison reads it, routes to a dimension of the definition
// — none is left for OtherFeature. The kustomizations other owners keep,
// which the plan lists an entry in, are not the definition's files.
func TestEveryRenderedLeafHasADimension(t *testing.T) {
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range caps {
		feats, err := definitions.Features(c)
		if err != nil {
			t.Fatal(err)
		}
		matchers := matchersOf(feats, nil)
		goldens, _ := filepath.Glob(filepath.Join("..", "..", "render", strings.ReplaceAll(c, "-", ""), "testdata", "*", "golden"))
		if len(goldens) == 0 {
			t.Fatalf("%s: no golden under render/", c)
		}
		for _, golden := range goldens {
			shape := filepath.Base(filepath.Dir(golden))
			includes := includesOf(t, golden)
			err := filepath.WalkDir(filepath.Join(golden, "giantswarm"), func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				rel, _ := filepath.Rel(golden, p)
				parts := strings.SplitN(filepath.ToSlash(rel), "/", 3)
				if includes[parts[0]+"/"+parts[1]+":"+parts[2]] {
					return nil
				}
				content, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				fd := &fileDiff{path: parts[2], kind: kindOf(parts[2])}
				for _, leaf := range decodedLeaves(flattenYAML(string(content)), true) {
					if route(matchers, fd, leaf) == nil {
						t.Errorf("%s %s: %s#%s (%s) is observed under no dimension", c, shape, parts[2], leaf, fd.kind)
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

// includesOf reads a golden's includes.txt: the files of other owners the
// plan lists an entry in, as repository:path.
func includesOf(t *testing.T, golden string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	f, err := os.Open(filepath.Join(golden, "includes.txt"))
	if err != nil {
		return out
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if fields := strings.Fields(s.Text()); len(fields) > 0 {
			out[fields[0]] = true
		}
	}
	return out
}

// decodedLeaves are a flat file's leaves with the YAML text a ConfigMap's
// data or a Secret's stringData holds decoded to its own leaves, by their
// path inside the text — and the text such a document holds in turn (the
// portal's app-config). A value that is no YAML mapping over several lines
// stays a leaf.
func decodedLeaves(flat map[string]string, top bool) []string {
	var out []string
	for p, v := range flat {
		if s := segments(p); strings.Contains(v, "\n") && (!top || len(s) > 0 && (s[0] == "data" || s[0] == "stringData")) {
			if inner := flattenYAML(v); len(inner) > 0 && inner[""] == "" {
				out = append(out, decodedLeaves(inner, false)...)
				continue
			}
		}
		out = append(out, p)
	}
	return out
}
