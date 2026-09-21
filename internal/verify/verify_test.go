package verify

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A leaf of the render that carries a choice not on record (a Missing
// marker) is never a difference: differences leaves it out whatever the
// record holds, missingLeaves names the fields it carries, and the
// dimension it is observed under is not checked with the reason naming the
// choice where nothing else in it is off; drift beside it still shows.
func TestMissingChoiceIsNotCheckedNeverADifference(t *testing.T) {
	const field = "plugins.grafana.domain"
	want := flattenYAML("a: 1\ngrafana:\n  domain: " + render.Missing(field) + "\n")
	if got := differences("r:p", want, "a: 1\n", nil); len(got) != 0 {
		t.Errorf("the record without the leaf: %+v", got)
	}
	if got := differences("r:p", want, "a: 1\ngrafana:\n  domain: https://g\n", nil); len(got) != 0 {
		t.Errorf("the record with another value: %+v", got)
	}
	if got := differences("r:p", want, "a: 2\n", nil); len(got) != 1 || got[0].Path != "a" {
		t.Errorf("drift beside the leaf: %+v", got)
	}
	if got := missingLeaves(want); !reflect.DeepEqual(got, map[string][]string{"grafana.domain": {field}}) {
		t.Errorf("missing leaves %v", got)
	}
	if got := missingFields("x " + render.Missing("b") + " " + render.Missing("a") + " " + render.Missing("a")); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("missing fields %v", got)
	}
	if got := missingLeaves(flattenYAML("a: " + render.Supplied("a") + "\n")); got != nil {
		t.Errorf("a supplied marker is no missing choice: %v", got)
	}

	const appConfig = "management-clusters/x/extras/backstage/backstage/app-config.yaml"
	feats := []definitions.Feature{{ID: "portal", Dimensions: []definitions.Dimension{
		{ID: "plugins", Kind: definitions.KindBackstage, Key: "app-config grafana.domain / grafana.other"},
		{ID: "rest", Kind: definitions.KindBackstage, Key: "everything else of the app-config"},
	}}}
	fd := &fileDiff{key: "r:" + appConfig, path: appConfig, kind: definitions.KindBackstage, missing: map[string][]string{"grafana.domain": {field}}}
	dims := assign(&comparison{files: map[string]*fileDiff{fd.key: fd}}, feats, "")
	if d := dims["plugins"]; d.Mark != NotChecked || d.Reason != ReasonMissingChoice+": "+field || len(d.Differences) != 0 {
		t.Errorf("the choice's dimension: %+v", *d)
	}
	if d := dims["rest"]; d.Mark != AsDefined || d.Reason != "" {
		t.Errorf("the dimension beside it: %+v", *d)
	}
	fd.diffs = []Difference{{File: fd.key, Path: "grafana.other", Rendered: "x", Current: "y"}}
	dims = assign(&comparison{files: map[string]*fileDiff{fd.key: fd}}, feats, "")
	if d := dims["plugins"]; d.Mark != Drifted || d.Reason != "" || len(d.Differences) != 1 {
		t.Errorf("drift beside the choice: %+v", *d)
	}
}

// The roll-up: drifted over differs by input over as defined; not checked
// dimensions never taint a feature that has a checked one.
func TestRollUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []Mark
		want Mark
	}{
		{"empty", nil, NotChecked},
		{"all not checked", []Mark{NotChecked, NotChecked}, NotChecked},
		{"checked beside not checked", []Mark{NotChecked, AsDefined}, AsDefined},
		{"planned over defined", []Mark{AsDefined, Planned, NotChecked}, Planned},
		{"input over planned", []Mark{AsDefined, Planned, DiffersByInput, NotChecked}, DiffersByInput},
		{"drift over everything", []Mark{AsDefined, Planned, DiffersByInput, Drifted, NotChecked}, Drifted},
	} {
		dims := make([]Dimension, 0, len(tc.in))
		for _, m := range tc.in {
			dims = append(dims, Dimension{Mark: m})
		}
		if got := rollUp(dims); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A difference under a key the capability's removals name is a planned
// change: the key covers everything beneath it, [*] any list index and
// <name> any map key; a leaf beside it is drift. The key's prefix names
// the file: configmap and dex-configmap the configmap patch of their app,
// dex-secret the secret patch, extras a path under extras/ (a directory
// covering everything beneath), backstage a fileset's file or a whole file.
func TestRemovalsNameThePlannedChanges(t *testing.T) {
	const migration, template, notPlatform, other, m25 = "migration", "template", "not-platform", "other-definition", "M25"
	rms := readRemovals([]definitions.Removal{
		{Key: "configmap:kagent.modelConfigs", Kind: migration, Reason: m25},
		{Key: "configmap:muster.muster.oauth.server.tokenExchangeBroker.clientAudiences.<brokerClientId>[github]", Kind: migration, Reason: "M1"},
		{Key: "configmap:agent-platform-mcps.mcpServers[*].timeout", Kind: template, Reason: "T1"},
		{Key: "dex-configmap:ingress", Kind: notPlatform, Reason: "D1"},
		{Key: "dex-secret:oidc.customer", Kind: notPlatform, Reason: "D2"},
		{Key: "extras:agents", Kind: notPlatform, Reason: "E1"},
		{Key: "extras:agent-platform/kustomization.yaml patches[*]", Kind: template, Reason: "E2"},
		{Key: "backstage:app-config:auth", Kind: other, Reason: "B1"},
		{Key: "backstage:file:user-secrets.enc.yaml", Kind: other, Reason: "B2"},
		{Key: "teleport:tunnels", Kind: "other", Reason: "ignored"},
	})
	if len(rms) != 9 {
		t.Fatalf("%d removals read, want 9 (a prefix of no file kind is left out)", len(rms))
	}
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	dexCM := &fileDiff{path: testDexPatch, kind: definitions.KindDexSecret}
	dexSecret := &fileDiff{path: "installations/x/apps/dex-app/secret-values.yaml.patch", kind: definitions.KindDexSecret}
	agents := &fileDiff{path: "management-clusters/x/extras/agents/kustomization.yaml", kind: definitions.KindExtras}
	kust := &fileDiff{path: "management-clusters/x/extras/agent-platform/kustomization.yaml", kind: definitions.KindExtras}
	appConfig := &fileDiff{path: "management-clusters/x/extras/backstage/app-config.yaml", kind: definitions.KindBackstage}
	secrets := &fileDiff{path: "management-clusters/x/extras/backstage/user-secrets.enc.yaml", kind: definitions.KindBackstage}
	for _, tc := range []struct {
		name string
		fd   *fileDiff
		path string
		want string
	}{
		{"the key itself", patch, "kagent.modelConfigs", m25},
		{"a leaf under the key", patch, "kagent.modelConfigs[claude].provider", m25},
		{"a leaf beside the key", patch, "kagent.modelConfigsX", ""},
		{"a sibling", patch, "kagent.providers.anthropic.model", ""},
		{"<name> is any map key", patch, "muster.muster.oauth.server.tokenExchangeBroker.clientAudiences.abc[github]", "M1"},
		{"<name> is not a list index", patch, "muster.muster.oauth.server.tokenExchangeBroker.clientAudiences[0][github]", ""},
		{"[*] is any index, an identity with dots too", patch, "agent-platform-mcps.mcpServers[http://mcp.svc:8080/mcp].timeout", "T1"},
		{"[*] is not a map key", patch, "agent-platform-mcps.mcpServers.timeout", ""},
		{"the dex configmap patch", dexCM, "ingress.enabled", "D1"},
		{"not the dex secret patch", dexSecret, "ingress.enabled", ""},
		{"the dex secret patch", dexSecret, "oidc.customer.clientSecret", "D2"},
		{"not the dex configmap patch", dexCM, "oidc.customer.clientID", ""},
		{"a directory under extras covers its files", agents, "resources[a.yaml]", "E1"},
		{"a whole file under extras", agents, "", "E1"},
		{"a path in a file under extras", kust, "patches[0].path", "E2"},
		{"a path beside it", kust, "resources[0]", ""},
		{"a backstage fileset", appConfig, "auth.providers", "B1"},
		{"a backstage file", secrets, "", "B2"},
		{"another backstage file", appConfig, "", ""},
	} {
		if got := rms.reason(tc.fd, tc.path); got != tc.want {
			t.Errorf("%s: %s#%s planned %q, want %q", tc.name, tc.fd.path, tc.path, got, tc.want)
		}
	}
}

// A leaf the definition renders and the record lacks, under a key the
// capability's migrations name, is the migration's planned addition; the
// same leaf on record with another value is a difference. <x> in a key
// stands for a map key, or for a part of a file's or a list entry's name; a
// file-level key covers the whole file, a backstage:file directory the
// Component beneath it. A removal's reason comes first.
func TestMigrationsNameThePlannedAdditions(t *testing.T) {
	const m1, m3, m5, m32 = "M1", "M3", "M5", "M32"
	migs := readMigrations([]definitions.Migration{
		{Key: "extras:agent-platform/secrets/dex-client-<name>-token-exchange-secret.yaml", Reason: m32},
		{Key: "dex-configmap:oidc.extraStaticClients[kagent]", Reason: m5},
		{Key: "dex-configmap:oidc.staticClients.<name>.clientSecretRef", Reason: m1},
		{Key: "extras:agent-platform/secrets/dex-client-<name>-secret.yaml", Reason: m1},
		{Key: "extras:agent-platform/secrets/kustomization.yaml resources[dex-client-<name>-secret.yaml]", Reason: m1},
		{Key: "extras:mcp-<name>/kustomization.yaml resources[dex-client-mcp-<name>-secret.yaml]", Reason: m1},
		{Key: "backstage:file:agent-platform", Reason: m3},
		{Key: "teleport:tunnels", Reason: "ignored"},
	})
	if len(migs) != 7 {
		t.Fatalf("%d migrations read, want 7 (a prefix of no file kind is left out)", len(migs))
	}
	rms := readRemovals([]definitions.Removal{{Key: "dex-configmap:oidc.extraStaticClients[*].redirectURIs", Kind: "template", Reason: "R1"}})
	dexCM := &fileDiff{path: testDexPatch, kind: definitions.KindDexSecret}
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	secret := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/dex-client-muster-secret.yaml", kind: definitions.KindExtras}
	exchange := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/dex-client-x-token-exchange-secret.yaml", kind: definitions.KindExtras}
	kust := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/kustomization.yaml", kind: definitions.KindExtras}
	mcpKust := &fileDiff{path: "management-clusters/x/extras/mcp-capi/kustomization.yaml", kind: definitions.KindExtras}
	component := &fileDiff{path: "management-clusters/x/extras/backstage/agent-platform/app-config.yaml", kind: definitions.KindBackstage}
	appConfig := &fileDiff{path: "management-clusters/x/extras/backstage/app-config.yaml", kind: definitions.KindBackstage}
	absent := func(p string) *Difference { return &Difference{Path: p, Rendered: "x", absent: true} }
	present := func(p string) *Difference { return &Difference{Path: p, Rendered: "x", Current: "y"} }
	for _, tc := range []struct {
		name string
		fd   *fileDiff
		d    *Difference
		want string
	}{
		{"an added leaf under the key", dexCM, absent("oidc.extraStaticClients[kagent].id"), m5},
		{"the same leaf with another value on record", dexCM, present("oidc.extraStaticClients[kagent].id"), ""},
		{"an added leaf beside the key", dexCM, absent("oidc.extraStaticClients[other].id"), ""},
		{"<name> is any map key", dexCM, absent("oidc.staticClients.mcpCapi.clientSecretRef.name"), m1},
		{"<name> is not a list index", dexCM, absent("oidc.staticClients[0].clientSecretRef.name"), ""},
		{"a leaf of another file", patch, absent("oidc.staticClients.muster.clientSecretRef.name"), ""},
		{"a file-level key covers a created file", secret, absent(""), m1},
		{"a file-level key covers every leaf of it", secret, absent("stringData.secret"), m1},
		{"the more specific key comes first", exchange, absent(""), m32},
		{"<name> in a list entry's identity", kust, absent("resources[dex-client-backstage-secret.yaml]"), m1},
		{"an identity beside it", kust, absent("resources[muster-oauth-credentials.yaml]"), ""},
		{"<name> in a directory and an identity", mcpKust, absent("resources[dex-client-mcp-capi-secret.yaml]"), m1},
		{"a backstage directory covers the Component's files", component, absent("app.extensions[0]"), m3},
		{"a backstage file beside the directory", appConfig, absent("app.extensions[0]"), ""},
		{"a removal's reason first", dexCM, absent("oidc.extraStaticClients[kagent].redirectURIs[0]"), "R1"},
	} {
		if got := planned(tc.fd, tc.d, rms, migs); got != tc.want {
			t.Errorf("%s: %s#%s planned %q, want %q", tc.name, tc.fd.path, tc.d.Path, got, tc.want)
		}
	}
}

// A file dimension's mark: a planned difference never drifts; beside drift
// or an input's difference, the dimension keeps that mark and the planned
// ones stay marked.
func TestFileMarkWithPlannedChanges(t *testing.T) {
	planned := Difference{Path: "a", Planned: "M1"}
	drift := Difference{Path: "b"}
	input := Difference{Path: "c", Input: "k.v"}
	for _, tc := range []struct {
		name  string
		diffs []Difference
		want  Mark
	}{
		{"only planned", []Difference{planned}, Planned},
		{"planned beside drift", []Difference{planned, drift}, Drifted},
		{"planned beside an input", []Difference{planned, input}, DiffersByInput},
		{"an input beside planned", []Difference{input, planned}, DiffersByInput},
		{"none", nil, AsDefined},
	} {
		if got := fileMark(tc.diffs, true, false); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A dimension's key names YAML paths or files (prefixes) or is prose (a catch-all).
func TestMatcherReadsKeys(t *testing.T) {
	m := newMatcher(definitions.Dimension{Kind: definitions.KindConfigMap, Key: "kagent.oauth2-proxy.config.clientID / clientSecret / cookieSecret"})
	if len(m.prefixes) != 1 || m.match("", "kagent.oauth2-proxy.config.clientID") == 0 || m.match("", "kagent.oauth2-proxy.configX") != 0 {
		t.Errorf("prefixes %v", m.prefixes)
	}
	if m := newMatcher(definitions.Dimension{Kind: definitions.KindExtras, Key: "secrets/kustomization.yaml resources"}); m.match("secrets/kustomization.yaml", "resources[0]") == 0 {
		t.Error("a file key names the file under extras")
	}
	if m := newMatcher(definitions.Dimension{Kind: definitions.KindConfigMap, Key: "agent-platform secret-values.yaml.patch"}); m.match("installations/x/apps/agent-platform/secret-values.yaml.patch", "a") == 0 || m.match(testPlatformPatch, "a") != 0 {
		t.Errorf("a file word names the file: %v", m.prefixes)
	}
	if m := newMatcher(definitions.Dimension{Kind: definitions.KindConfigMap, Key: "the patch's top-level keys"}); len(m.prefixes) != 0 {
		t.Errorf("prose is a catch-all, got %v", m.prefixes)
	}
	if m := newMatcher(definitions.Dimension{Kind: definitions.KindDexSecret, Key: "oidc.* other than staticClients"}); len(m.prefixes) != 0 {
		t.Errorf("a glob is a catch-all, got %v", m.prefixes)
	}
}

// Files flatten to their leaves, a list's entries keyed by identity; a file
// that is not YAML is one leaf.
func TestFlattenAndDiff(t *testing.T) {
	a := flattenYAML("a:\n  b: 1\n  c: [x, y]\nd: {}\n")
	b := flattenYAML("a:\n  b: 2\n  c: [x]\nd: {}\n")
	if got := diffPaths(a, b); len(got) != 2 || got[0] != "a.b" || got[1] != "a.c[y]" {
		t.Errorf("diff %v", got)
	}
	if got := flattenYAML("not: [yaml"); len(got) != 1 || got[""] == "" {
		t.Errorf("not YAML: %v", got)
	}
}

// A list the plan merges or sorts compares as a set: a reordered list of
// scalars, of clients by id or of tunnels by name is the same leaves, and an
// entry added is its own; entries without an identity, or two sharing one,
// keep their positions.
func TestFlattenKeysListsByIdentity(t *testing.T) {
	if got := diffPaths(flattenYAML("resources:\n- a.yaml\n- b.yaml\n"), flattenYAML("resources:\n- b.yaml\n- a.yaml\n")); len(got) != 0 {
		t.Errorf("a reordered list differs: %v", got)
	}
	if got := diffPaths(flattenYAML("resources:\n- a.yaml\n"), flattenYAML("resources:\n- b.yaml\n- a.yaml\n")); len(got) != 1 || got[0] != "resources[b.yaml]" {
		t.Errorf("an added entry is its own leaf: %v", got)
	}
	clients := "oidc:\n  extraStaticClients:\n  - id: one\n    name: One\n  - id: two\n    name: Two\n"
	swapped := "oidc:\n  extraStaticClients:\n  - id: two\n    name: Two\n  - id: one\n    name: Other\n"
	if got := diffPaths(flattenYAML(clients), flattenYAML(swapped)); len(got) != 1 || got[0] != "oidc.extraStaticClients[one].name" {
		t.Errorf("clients are keyed by id: %v", got)
	}
	tunnels := "tunnels:\n- name: t1\n  port: 1\n- name: t2\n  port: 2\n"
	if got := flattenYAML(tunnels); got["tunnels[t2].port"] != "2" {
		t.Errorf("tunnels are keyed by name: %v", got)
	}
	servers := "agent-platform-mcps:\n  mcpServers:\n  - cluster: a\n    url: http://a.svc:8080/mcp\n    timeout: 30\n  - cluster: b\n    url: http://b.svc:8080/mcp\n    timeout: 30\n"
	reordered := "agent-platform-mcps:\n  mcpServers:\n  - cluster: b\n    url: http://b.svc:8080/mcp\n    timeout: 30\n  - cluster: a\n    url: http://a.svc:8080/mcp\n    timeout: 30\n"
	if got := diffPaths(flattenYAML(servers), flattenYAML(reordered)); len(got) != 0 {
		t.Errorf("MCP servers are keyed by url, reordered they are the same leaves: %v", got)
	}
	changed := strings.Replace(reordered, "cluster: b\n    url: http://b.svc:8080/mcp\n    timeout: 30", "cluster: b\n    url: http://b.svc:8080/mcp\n    timeout: 60", 1)
	if got := diffPaths(flattenYAML(servers), flattenYAML(changed)); len(got) != 1 || got[0] != "agent-platform-mcps.mcpServers[http://b.svc:8080/mcp].timeout" {
		t.Errorf("a changed server differs at its leaves only: %v", got)
	}
	if got := flattenYAML("patches:\n- path: a\n- path: b\n"); got["patches[0].path"] != "a" || got["patches[1].path"] != "b" {
		t.Errorf("no identity keeps the positions: %v", got)
	}
	if got := flattenYAML("l:\n- name: x\n- name: x\n"); got["l[0].name"] != "x" || got["l[1].name"] != "x" {
		t.Errorf("a shared identity keeps the positions: %v", got)
	}
}

// A file of several documents keys each by kind/namespace/name: reordered,
// it is the same leaves; a document added is its leaves.
func TestFlattenKeysDocumentsByObject(t *testing.T) {
	a := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: one\n  namespace: ns\ndata:\n  k: v\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: two\n  namespace: ns\n"
	b := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: two\n  namespace: ns\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: one\n  namespace: ns\ndata:\n  k: v\n"
	if got := diffPaths(flattenYAML(a), flattenYAML(b)); len(got) != 0 {
		t.Errorf("a reordered file differs: %v", got)
	}
	c := b + "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: three\n  namespace: ns\n"
	if got := diffPaths(flattenYAML(a), flattenYAML(c)); len(got) != 4 || got[0] != "[ConfigMap/ns/three].apiVersion" {
		t.Errorf("an added document is its leaves: %v", got)
	}
	if got := flattenYAML("a: 1\n---\nb: 2\n"); got["[doc0].a"] != "1" || got["[doc1].b"] != "2" {
		t.Errorf("documents without an object keep their positions: %v", got)
	}
}

// The differences of a file the plan writes: a value the commit fills in or
// the record holds encrypted is never one; in an encrypted file SOPS's block
// takes no part and the values are redacted.
func TestDifferencesFollowTheSkeleton(t *testing.T) {
	want := flattenYAML("apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  token: GENERATED(token)\n  extra: plain\n")
	current := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  token: ENC[AES256_GCM,data:x,type:str]\n  extra: ENC[AES256_GCM,data:y,type:str]\nhandEdited: true\nsops:\n  version: 3.9.0\n  age: []\n"
	got := differences("r:p", want, current, map[string]string{"r:p#handEdited": "x.y"})
	if len(got) != 1 || got[0].Path != "handEdited" || got[0].Rendered != "" || got[0].Current != Redacted || got[0].Input != "x.y" {
		t.Errorf("encrypted: %+v", got)
	}
	got = differences("r:p", flattenYAML("a: 1\n"), current, nil)
	if len(got) != 7 {
		t.Errorf("an encrypted file whose skeleton differs: %+v", got)
	}
	for _, d := range got {
		if d.Path == "a" && (d.Rendered != Redacted || d.Current != "") || d.Path != "a" && d.Current != Redacted || strings.HasPrefix(d.Path, "sops") {
			t.Errorf("redacted: %+v", d)
		}
	}
	plain := "a: 1\nb: SUPPLIED(b)\nc: 3\n"
	got = differences("r:p", flattenYAML(plain), "a: 1\nb: secret\nc: 4\n", nil)
	if len(got) != 1 || got[0].Path != "c" || got[0].Rendered != "3" || got[0].Current != "4" {
		t.Errorf("plain: %+v", got)
	}
	if got := differences("r:p", flattenYAML(plain), "", nil); len(got) != 3 || got[1].Rendered != "SUPPLIED(b)" {
		t.Errorf("created: %+v", got)
	}
}

// The reads: every file once as the caller, and the perturbed plans read
// what was read, a file not read being absent.
func TestReadsOnce(t *testing.T) {
	n := 0
	rs := &reads{got: map[string]read{}, read: func(_ context.Context, repository, path string) (string, error) {
		n++
		if path == "absent" {
			return "", gh.ErrNotFound
		}
		return repository + "/" + path, nil
	}}
	ctx := context.Background()
	for range 2 {
		if got, err := rs.reader(ctx, "r", "p"); got != "r/p" || err != nil {
			t.Errorf("read %q %v", got, err)
		}
		if _, err := rs.reader(ctx, "r", "absent"); !errors.Is(err, gh.ErrNotFound) {
			t.Errorf("absent %v", err)
		}
	}
	if n != 2 {
		t.Errorf("read %d times", n)
	}
	if got, err := rs.recorded(ctx, "r", "p"); got != "r/p" || err != nil {
		t.Errorf("recorded %q %v", got, err)
	}
	if _, err := rs.recorded(ctx, "r", "never"); !errors.Is(err, gh.ErrNotFound) || n != 2 {
		t.Errorf("a file not read: %v after %d reads", err, n)
	}
}
