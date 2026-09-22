package verify

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// grafanaDomainPath is the app-config's leaf that holds the Grafana choice.
const grafanaDomainPath = "grafana.domain"

// A leaf of the render that carries a choice not on record (a Missing
// marker) is never a difference: differences leaves it out whatever the
// record holds, missingLeaves names the fields it carries, and the
// dimension it is observed under is not checked with the reason naming the
// choice where nothing else in it is off; drift beside it still shows.
func TestMissingChoiceIsNotCheckedNeverADifference(t *testing.T) {
	const field = "plugins.grafana.domain"
	rendered := "a: 1\ngrafana:\n  domain: " + render.Missing(field) + "\n"
	if got, _ := differences("r:p", rendered, "a: 1\n", nil); len(got) != 0 {
		t.Errorf("the record without the leaf: %+v", got)
	}
	if got, _ := differences("r:p", rendered, "a: 1\ngrafana:\n  domain: https://g\n", nil); len(got) != 0 {
		t.Errorf("the record with another value: %+v", got)
	}
	if got, _ := differences("r:p", rendered, "a: 2\n", nil); len(got) != 1 || got[0].Path != "a" {
		t.Errorf("drift beside the leaf: %+v", got)
	}
	if got := missingLeaves(flattenYAML(rendered)); !reflect.DeepEqual(got, map[string][]string{grafanaDomainPath: {field}}) {
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
		{ID: pluginsDimension, Kind: definitions.KindBackstage, Key: "app-config.yaml grafana.domain / grafana.other"},
		{ID: "rest", Kind: definitions.KindBackstage, CatchAll: true, Key: "everything else of the app-config"},
	}}}
	fd := &fileDiff{key: "r:" + appConfig, path: appConfig, kind: definitions.KindBackstage, missing: map[string][]string{grafanaDomainPath: {field}}}
	dims, _ := assign(&comparison{files: map[string]*fileDiff{fd.key: fd}}, feats, "", nil)
	if d := dims[pluginsDimension]; d.Mark != NotChecked || d.Reason != ReasonMissingChoice+": "+field || len(d.Differences) != 0 {
		t.Errorf("the choice's dimension: %+v", *d)
	}
	if d := dims["rest"]; d.Mark != AsDefined || d.Reason != "" {
		t.Errorf("the dimension beside it: %+v", *d)
	}
	fd.diffs = []Difference{{File: fd.key, Path: "grafana.other", Rendered: "x", Current: "y"}}
	dims, _ = assign(&comparison{files: map[string]*fileDiff{fd.key: fd}}, feats, "", nil)
	if d := dims[pluginsDimension]; d.Mark != Drifted || d.Reason != "" || len(d.Differences) != 1 {
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
		{Key: "backstage:app-config:app.extensions[*]", Kind: other, Reason: "B3"},
		{Key: "teleport:tunnels", Kind: "other", Reason: "ignored"},
	}, nil)
	if len(rms) != 10 {
		t.Fatalf("%d removals read, want 10 (a prefix of no file kind is left out)", len(rms))
	}
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	dexCM := &fileDiff{path: testDexPatch, kind: definitions.KindDexConfigMap}
	dexSecret := &fileDiff{path: testDexSecretPatch, kind: definitions.KindDexSecret}
	agents := &fileDiff{path: "management-clusters/x/extras/agents/kustomization.yaml", kind: definitions.KindExtras}
	kust := &fileDiff{path: "management-clusters/x/extras/agent-platform/kustomization.yaml", kind: definitions.KindExtras}
	appConfig := &fileDiff{path: "management-clusters/x/extras/backstage/backstage/app-config.yaml", kind: definitions.KindBackstage, documents: appConfigDocuments}
	secrets := &fileDiff{path: "management-clusters/x/extras/backstage/backstage/user-secrets.enc.yaml", kind: definitions.KindBackstage}
	fragment := &fileDiff{path: testComponentAppConfig, kind: definitions.KindBackstage, documents: map[string]bool{"data.app-config.agent-platform.yaml": true}}
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
		{"a backstage fileset's key inside the ConfigMap's text", appConfig, "data.values:backstage.appConfig:auth.providers", "B1"},
		{"not the platform Component's file of the same name", fragment, "data.app-config.agent-platform.yaml:auth.providers", ""},
		{"a backstage file", secrets, "", "B2"},
		{"another backstage file", appConfig, "", ""},
		{"[*] in a fileset's key is an inline list entry", appConfig, "data.values:backstage.appConfig:app.extensions[3]", "B3"},
		{"and every leaf of an entry with a colon in its key", appConfig, "data.values:backstage.appConfig:app.extensions[14].entity-card:catalog/labels", "B3"},
		{"not the list's $include", appConfig, "data.values:backstage.appConfig:app.extensions.$include", ""},
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
	}, nil)
	if len(migs) != 7 {
		t.Fatalf("%d migrations read, want 7 (a prefix of no file kind is left out)", len(migs))
	}
	rms := readRemovals([]definitions.Removal{{Key: "dex-configmap:oidc.extraStaticClients[*].redirectURIs", Kind: "template", Reason: "R1"}}, nil)
	dexCM := &fileDiff{path: testDexPatch, kind: definitions.KindDexConfigMap}
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	secret := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/dex-client-muster-secret.yaml", kind: definitions.KindExtras}
	exchange := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/dex-client-x-token-exchange-secret.yaml", kind: definitions.KindExtras}
	kust := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/kustomization.yaml", kind: definitions.KindExtras}
	mcpKust := &fileDiff{path: "management-clusters/x/extras/mcp-capi/kustomization.yaml", kind: definitions.KindExtras}
	component := &fileDiff{path: testComponentAppConfig, kind: definitions.KindBackstage}
	appConfig := &fileDiff{path: "management-clusters/x/extras/backstage/backstage/app-config.yaml", kind: definitions.KindBackstage}
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
// takes no part, the leaves under its encrypted_regex are redacted and every
// other leaf shows its values — every leaf when the block has no regex.
func TestDifferencesFollowTheSkeleton(t *testing.T) {
	rendered := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  token: GENERATED(token)\n  extra: plain\n"
	current := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  token: ENC[AES256_GCM,data:x,type:str]\n  extra: ENC[AES256_GCM,data:y,type:str]\nhandEdited: true\nsops:\n  version: 3.9.0\n  age: []\n"
	got, _ := differences("r:p", rendered, current, map[string]string{"r:p#handEdited": "x.y"})
	if len(got) != 1 || got[0].Path != "handEdited" || got[0].Rendered != "" || got[0].Current != Redacted || got[0].Input != "x.y" || got[0].Line != 0 || got[0].CurrentLine != 8 {
		t.Errorf("encrypted: %+v", got)
	}
	regexRendered := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: ns\ntype: Opaque\nstringData:\n  token: GENERATED(token)\n  VALKEY_PASSWORD: GENERATED(valkey)\n"
	regexCurrent := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\ntype: kubernetes.io/tls\nstringData:\n  token: ENC[AES256_GCM,data:x,type:str]\nsops:\n  encrypted_regex: ^(data|stringData)$\n  version: 3.9.0\n  age: []\n"
	got, _ = differences("r:p", regexRendered, regexCurrent, nil)
	shown := func(i int, path, rendered, current string, line, currentLine int) bool {
		return got[i].Path == path && got[i].Rendered == rendered && got[i].Current == current && got[i].Line == line && got[i].CurrentLine == currentLine
	}
	if len(got) != 3 || !shown(0, "metadata.namespace", "ns", "", 5, 0) || !shown(1, "stringData.VALKEY_PASSWORD", Redacted, "", 9, 0) || !shown(2, "type", "Opaque", "kubernetes.io/tls", 6, 5) {
		t.Errorf("under encrypted_regex redacted, every other leaf shown, each on its lines: %+v", got)
	}
	got, _ = differences("r:p", "a: 1\n", current, nil)
	if len(got) != 7 {
		t.Errorf("an encrypted file whose skeleton differs: %+v", got)
	}
	for _, d := range got {
		if d.Path == "a" && (d.Rendered != Redacted || d.Current != "") || d.Path != "a" && d.Current != Redacted || strings.HasPrefix(d.Path, "sops") {
			t.Errorf("redacted: %+v", d)
		}
	}
	plain := "a: 1\nb: SUPPLIED(b)\nc: 3\n"
	got, _ = differences("r:p", plain, "a: 1\nb: secret\nc: 4\n", nil)
	if len(got) != 1 || got[0].Path != "c" || got[0].Rendered != "3" || got[0].Current != "4" || got[0].Line != 3 || got[0].CurrentLine != 3 {
		t.Errorf("plain: %+v", got)
	}
	if got, _ := differences("r:p", plain, "", nil); len(got) != 3 || got[1].Rendered != "SUPPLIED(b)" || got[1].Line != 2 || got[1].CurrentLine != 0 {
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

// A dimension not checked says what it lacks: the files of its kind the plan
// could not compare, each with the answer (GitHub's names the file, another
// reader's is prefixed with it), or the kind of file the definition renders
// none of. A dimension of the kind with a difference keeps its mark.
func TestNotCheckedReasonsNameWhatIsMissing(t *testing.T) {
	const patch = "installations/x/apps/dex-app/configmap-values.yaml.patch"
	feats := []definitions.Feature{{ID: "clients", Dimensions: []definitions.Dimension{
		{ID: "extra-clients", Kind: definitions.KindDexConfigMap, Key: "oidc.extraStaticClients"},
		{ID: "peers", Kind: definitions.KindDexConfigMap, Key: "oidc.staticClients"},
		{ID: "broker", Kind: definitions.KindBackstage, Key: "app-config.yaml gs.clusterTokenBroker"},
	}}}
	refused := &fileDiff{key: "r:" + patch, path: patch, kind: definitions.KindDexConfigMap, unreadable: "github: r:" + patch + ": 403 Forbidden"}
	dims, _ := assign(&comparison{files: map[string]*fileDiff{refused.key: refused}}, feats, "", nil)
	want := ReasonUnreadable + ": github: r:" + patch + ": 403 Forbidden"
	for _, id := range []string{"extra-clients", "peers"} {
		if d := dims[id]; d.Mark != NotChecked || d.Reason != want {
			t.Errorf("%s: %+v, want %q", id, *d, want)
		}
	}
	if d := dims["broker"]; d.Mark != NotChecked || d.Reason != ReasonNoFile+": "+definitions.KindBackstage {
		t.Errorf("no file of the kind: %+v", *d)
	}

	// An answer that does not name the file is prefixed with it, and a
	// dimension of the kind that still has a difference keeps its mark.
	refused.unreadable = "is on record but takes no entry: not a YAML mapping"
	refused.diffs = []Difference{{File: refused.key, Path: "oidc.extraStaticClients[kagent].id", Rendered: "kagent"}}
	dims, _ = assign(&comparison{files: map[string]*fileDiff{refused.key: refused}}, feats, "", nil)
	if d := dims["peers"]; d.Reason != ReasonUnreadable+": r:"+patch+": is on record but takes no entry: not a YAML mapping" {
		t.Errorf("prefixed answer: %+v", *d)
	}
	if d := dims["extra-clients"]; d.Mark != Drifted || d.Reason != "" {
		t.Errorf("the dimension with a difference: %+v", *d)
	}
}

// The leaves inside the text a ConfigMap holds compare like the file's own:
// each difference at its path through the text, on its line of each side,
// attributed to the input that drives it, planned where a removal names the
// key inside the document; a choice not on record inside the text is no
// difference and is named by its path. A leaf inside text the record holds
// encrypted takes no part, the field's value standing; of a Secret the record
// lacks every leaf inside the text differs, like every leaf of a created
// file. Only a ConfigMap's data and a Secret's stringData hold documents: a
// kustomization's patch is one leaf.
func TestDifferencesInsideText(t *testing.T) {
	const appConfig = "management-clusters/x/extras/backstage/backstage/app-config.yaml"
	head := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app-config-backstage\ndata:\n  values: |\n    backstage:\n      appConfig: |\n        app:\n"
	rendered := head + "          title: Dev Portal\n          baseUrl: https://portal.new\n        grafana:\n          domain: " + render.Missing("plugins.grafana.domain") + "\n"
	current := head + "          title: Old Portal\n          baseUrl: https://portal.old\n        muster:\n          installations: []\n        grafana:\n          domain: https://g\n"
	const doc = "data.values:backstage.appConfig:"
	got, docs := differences("r:"+appConfig, rendered, current, map[string]string{"r:" + appConfig + "#" + doc + "app.baseUrl": "portal.domain"})
	want := []Difference{
		{File: "r:" + appConfig, Path: doc + "app.baseUrl", Rendered: "https://portal.new", Current: "https://portal.old", Line: 11, CurrentLine: 11, Input: "portal.domain"},
		{File: "r:" + appConfig, Path: doc + "app.title", Rendered: "Dev Portal", Current: "Old Portal", Line: 10, CurrentLine: 10},
		{File: "r:" + appConfig, Path: doc + "muster.installations", Rendered: "", Current: "[]", Line: 0, CurrentLine: 13},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("differences inside the text:\n%+v\nwant\n%+v", got, want)
	}
	if missing := missingLeaves(flattenYAML(rendered)); !reflect.DeepEqual(missing, map[string][]string{doc + grafanaDomainPath: {"plugins.grafana.domain"}}) {
		t.Errorf("the choice not on record by its path through the text: %v", missing)
	}
	if !reflect.DeepEqual(docs, appConfigDocuments) {
		t.Errorf("the documents of both sides: %v", docs)
	}
	fd := &fileDiff{key: "r:" + appConfig, path: appConfig, kind: definitions.KindBackstage, documents: docs}
	rms := readRemovals([]definitions.Removal{{Key: "backstage:app-config:muster", Kind: "other-definition", Reason: "Moved: muster"}, {Key: "backstage:app-config:scaffolder", Kind: "not-rendered", Reason: "Removed: scaffolder"}}, nil)
	if reason := rms.reason(fd, doc+"muster.installations"); reason != "Moved: muster" {
		t.Errorf("a removal names the key inside the document: %q", reason)
	}
	if reason := rms.reason(fd, doc+"app.title"); reason != "" {
		t.Errorf("a key beside it: %q", reason)
	}
	var paths []string
	// A key's own colon is no step into a document: an extension entry
	// page:scaffolder is not the scaffolder block, and its leaves keep their
	// path inside the app-config.
	extensions := head + "          extensions:\n            - entity-card:catalog/labels: false\n            - page:scaffolder:\n                config:\n                  title: Create\n        scaffolder: {}\n"
	got, docs = differences("r:"+appConfig, rendered, extensions, nil)
	paths = nil
	for _, d := range got {
		paths = append(paths, d.Path)
	}
	if !slices.Equal(paths, []string{doc + "app.baseUrl", doc + "app.extensions[0].entity-card:catalog/labels", doc + "app.extensions[1].page:scaffolder.config.title", doc + "app.title", doc + "scaffolder"}) {
		t.Errorf("leaves with a colon in the key: %v", paths)
	}
	fd.documents = docs
	if innerPath(fd.documents, got[1].Path) != "app.extensions[0].entity-card:catalog/labels" || rms.reason(fd, got[2].Path) != "" || rms.reason(fd, got[4].Path) != "Removed: scaffolder" {
		t.Errorf("the scaffolder removal names the block, not the extension entry: %q / %q", rms.reason(fd, got[2].Path), rms.reason(fd, got[4].Path))
	}

	secret := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  values: |\n    authSessionSecret: " + render.Placeholder("session") + "\n    dexAuthCredentials:\n      maple:\n        clientId: backstage\n"
	encrypted := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\nstringData:\n  values: ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]\nsops:\n  encrypted_regex: ^(data|stringData)$\n  version: 3.9.0\n  age: []\n"
	if got, _ := differences("r:s", secret, encrypted, nil); len(got) != 0 {
		t.Errorf("the record's encrypted text stands: %+v", got)
	}
	created, _ := differences("r:s", secret, "", nil)
	paths = paths[:0]
	for _, d := range created {
		paths = append(paths, d.Path)
	}
	if !reflect.DeepEqual(paths, []string{apiVersionPath, kindPath, "metadata.name", "stringData.values:authSessionSecret", "stringData.values:dexAuthCredentials.maple.clientId"}) || created[3].Rendered != render.Placeholder("session") || created[4].Line != 10 || created[4].Rendered != "backstage" {
		t.Errorf("a created Secret differs at every leaf inside its text, like a created file: %+v", created)
	}
	if got, _ := differences("r:k", "patches:\n- patch: |\n    spec:\n      a: 1\n", "patches:\n- patch: |\n    spec:\n      a: 2\n", nil); len(got) != 1 || got[0].Path != "patches[0].patch" {
		t.Errorf("a kustomization's patch is one leaf whatever it holds: %+v", got)
	}
}

// An empty mapping or list on one side where the other side holds entries
// under the key is the key without its entries: the entries differ, the
// key itself does not — the record's grafana: {} against the render's
// grafana.domain is one difference, at the domain; an empty mapping against
// none is still one.
func TestDifferencesReportTheEntriesOfAnEmptiedKey(t *testing.T) {
	if got, _ := differences("r:p", "grafana:\n  domain: x\n", "grafana: {}\n", nil); len(got) != 1 || got[0].Path != grafanaDomainPath || got[0].Current != "" {
		t.Errorf("the record's empty mapping: %+v", got)
	}
	if got, _ := differences("r:p", "list: []\n", "list:\n- a\n", nil); len(got) != 1 || got[0].Path != "list[a]" || got[0].Rendered != "" {
		t.Errorf("the render's empty list: %+v", got)
	}
	if got, _ := differences("r:p", "data:\n  values: |\n    grafana:\n      domain: x\n", "data:\n  values: |\n    grafana: {}\n", nil); len(got) != 1 || got[0].Path != "data.values:grafana.domain" {
		t.Errorf("inside a document: %+v", got)
	}
	if got, _ := differences("r:p", "labels: {}\n", "", nil); len(got) != 1 || got[0].Path != "labels" || got[0].Rendered != "{}" {
		t.Errorf("an empty mapping against none: %+v", got)
	}
}

// Every difference and every choice not on record of a comparison lands on
// a dimension — a leaf no key names on the kind's declared catch-all, else
// on the kind's dimension of OtherFeature, a leaf of the file itself and one
// inside the text a ConfigMap holds alike; a dimension declared on the
// dex-app's configmap observes the dex-app's patch. Over both definitions'
// features with a file of every kind each renders.
func TestAssignDropsNoLeaf(t *testing.T) {
	const (
		dexPatch     = "installations/x/apps/dex-app/configmap-values.yaml.patch"
		appConfig    = "management-clusters/x/extras/backstage/backstage/app-config.yaml"
		userValues   = "management-clusters/x/extras/backstage/backstage/user-values.yaml"
		fragment     = testComponentAppConfig
		platformKust = "management-clusters/x/extras/agent-platform/kustomization.yaml"
	)
	files := map[string][]string{
		installations.CustomerPortal: {dexPatch, appConfig, userValues},
		installations.AgentPlatform:  {testPlatformPatch, dexPatch, testDexSecretPatch, platformKust, fragment},
	}
	for capability, paths := range files {
		feats, err := definitions.Features(capability)
		if err != nil {
			t.Fatal(err)
		}
		c := &comparison{files: map[string]*fileDiff{}}
		wantDiffs, wantMissing := 0, map[string]bool{}
		for _, p := range paths {
			fd := &fileDiff{key: "r:" + p, path: p, kind: kindOf(p), missing: map[string][]string{}}
			for i, yp := range []string{"", "nobody.names.this", "data.values:backstage.appConfig:nobody.names.this[x].y"} {
				fd.diffs = append(fd.diffs, Difference{File: fd.key, Path: yp, Rendered: "a", Current: "b"})
				field := p + "#" + strconv.Itoa(i)
				fd.missing[yp] = []string{field}
				wantMissing[field] = true
			}
			wantDiffs += len(fd.diffs)
			c.files[fd.key] = fd
		}
		dims, others := assign(c, feats, "", nil)
		gotDiffs, gotMissing := 0, map[string]bool{}
		for _, d := range append(slices.Collect(maps.Values(dims)), others...) {
			gotDiffs += len(d.Differences)
			for f := range d.missing {
				gotMissing[f] = true
			}
		}
		if gotDiffs != wantDiffs {
			t.Errorf("%s: %d of %d differences reach a dimension", capability, gotDiffs, wantDiffs)
		}
		for f := range wantMissing {
			if !gotMissing[f] {
				t.Errorf("%s: the choice not on record %s reaches no dimension", capability, f)
			}
		}
	}
	dims, _ := assign(&comparison{files: map[string]*fileDiff{"r:" + dexPatch: {key: "r:" + dexPatch, path: dexPatch, kind: kindOf(dexPatch)}}}, must(definitions.Features(installations.CustomerPortal)), "", nil)
	if d := dims["dex-client-entry"]; d.Mark != AsDefined || len(d.Files) != 1 {
		t.Errorf("the dex-configmap dimension observes the dex-app's patch: %+v", *d)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// The inputs the attribution perturbs and the leaves a synthetic render
// derives from them.
const (
	baseDomainKey   = "baseDomain"
	servingInput    = "modelServing.enabled"
	baseDomainInput = "installation." + baseDomainKey
	customerInput   = "installation.customer"
	hostLeaf        = "r:values#host"
	repoLeaf        = "r:values#repo"
	servingLeaf     = "r:values#serving"
	titleLeaf       = "r:values#title"
	enabledKey      = "enabled"
)

// doc is an inputs document of two facts and the serving choice.
func doc(domain, customer string, serving bool) map[string]any {
	return map[string]any{"installation": map[string]any{baseDomainKey: domain, "customer": customer}, "modelServing": map[string]any{enabledKey: serving}}
}

// The inputs the attribution perturbs are the person's: the schema's
// x-source person leaves and every leaf typed for the call, a fact among
// them. The record's facts are not among them.
func TestDrivenInputsAreThePersonsAndTheTyped(t *testing.T) {
	def, ok := installations.FindCapability(installations.AgentPlatform)
	if !ok {
		t.Fatal("no agent-platform definition")
	}
	got, err := drivenInputs(def, Inputs{Values: doc("a.test", "acme", true)})
	if err != nil || !reflect.DeepEqual(got, []string{servingInput}) {
		t.Errorf("from the record: %v %v", got, err)
	}
	got, err = drivenInputs(def, Inputs{Values: doc("a.test", "acme", true), Typed: doc("b.test", "acme", false)})
	if err != nil || !reflect.DeepEqual(got, []string{baseDomainInput, customerInput, servingInput}) {
		t.Errorf("with facts typed: %v %v", got, err)
	}
}

// A rendered leaf is attributed to the input whose perturbation moves it,
// among the inputs named: a leaf the base domain derives is nobody's unless
// the base domain was typed for the call; a leaf several named inputs move
// is the most specific one's (the customer moves three leaves, the base
// domain two); an input the record holds no value for perturbs nothing.
func TestDrivenPathsPerturbsOnlyTheNamedInputs(t *testing.T) {
	values := doc("a.test", "acme", true)
	render := func(v map[string]any) (map[string]map[string]string, error) {
		inst, _ := v["installation"].(map[string]any)
		serving, _ := v["modelServing"].(map[string]any)
		domain, _ := inst[baseDomainKey].(string)
		customer, _ := inst["customer"].(string)
		return map[string]map[string]string{"r:values": {
			"host": "muster." + domain, "repo": customer + "-management-clusters", "org": customer,
			"serving": strconv.FormatBool(serving[enabledKey] == true), "title": customer + " on " + domain,
		}}, nil
	}
	base, _ := render(values)

	got := drivenPaths(values, []string{servingInput, "plugins.absent"}, base, render)
	if want := map[string]string{servingLeaf: servingInput}; !reflect.DeepEqual(got, want) {
		t.Errorf("the person's choice: %v", got)
	}
	got = drivenPaths(values, []string{baseDomainInput, servingInput}, base, render)
	if want := map[string]string{hostLeaf: baseDomainInput, titleLeaf: baseDomainInput, servingLeaf: servingInput}; !reflect.DeepEqual(got, want) {
		t.Errorf("the base domain typed: %v", got)
	}
	got = drivenPaths(values, []string{"installation"}, base, render)
	if want := map[string]string{hostLeaf: baseDomainInput, titleLeaf: baseDomainInput, repoLeaf: customerInput, "r:values#org": customerInput}; !reflect.DeepEqual(got, want) {
		t.Errorf("a mapping's leaves: %v", got)
	}
	if got := drivenPaths(values, nil, base, render); len(got) != 0 {
		t.Errorf("no input named: %v", got)
	}
}

// The plan fetches its files from several goroutines at once; the cache
// answers each of them once and stays consistent under the race detector.
func TestReadsAreSafeAtOnce(t *testing.T) {
	rs := &reads{got: map[string]read{}, read: func(_ context.Context, repository, path string) (string, error) {
		return repository + "/" + path, nil
	}}
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			path := "p" + strconv.Itoa(i)
			if got, err := rs.reader(ctx, "r", path); got != "r/"+path || err != nil {
				t.Errorf("read %q %v", got, err)
			}
		})
	}
	wg.Wait()
	if got, err := rs.recorded(ctx, "r", "p7"); got != "r/p7" || err != nil {
		t.Errorf("recorded %q %v", got, err)
	}
}
