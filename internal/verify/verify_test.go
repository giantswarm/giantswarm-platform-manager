package verify

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

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
		{"input over defined", []Mark{AsDefined, DiffersByInput, NotChecked}, DiffersByInput},
		{"drift over everything", []Mark{AsDefined, DiffersByInput, Drifted, NotChecked}, Drifted},
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

// A dimension's key names YAML paths or files (prefixes) or is prose (a catch-all).
func TestMatcherReadsKeys(t *testing.T) {
	m := newMatcher(definitions.Dimension{Kind: definitions.KindConfigMap, Key: "kagent.oauth2-proxy.config.clientID / clientSecret / cookieSecret"})
	if len(m.prefixes) != 1 || m.match("", "kagent.oauth2-proxy.config.clientID") == 0 || m.match("", "kagent.oauth2-proxy.configX") != 0 {
		t.Errorf("prefixes %v", m.prefixes)
	}
	if m := newMatcher(definitions.Dimension{Kind: definitions.KindExtras, Key: "secrets/kustomization.yaml resources"}); m.match("secrets/kustomization.yaml", "resources[0]") == 0 {
		t.Error("a file key names the file under extras")
	}
	if m := newMatcher(definitions.Dimension{Kind: definitions.KindConfigMap, Key: "agent-platform secret-values.yaml.patch"}); m.match("installations/x/apps/agent-platform/secret-values.yaml.patch", "a") == 0 || m.match("installations/x/apps/agent-platform/configmap-values.yaml.patch", "a") != 0 {
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
