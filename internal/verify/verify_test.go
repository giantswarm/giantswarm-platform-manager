package verify

import (
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
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

// Files flatten to their leaves; a file that is not YAML is one leaf.
func TestFlattenAndDiff(t *testing.T) {
	a := flattenYAML("a:\n  b: 1\n  c: [x, y]\nd: {}\n")
	b := flattenYAML("a:\n  b: 2\n  c: [x]\nd: {}\n")
	if got := diffPaths(a, b); len(got) != 2 || got[0] != "a.b" || got[1] != "a.c[1]" {
		t.Errorf("diff %v", got)
	}
	if got := flattenYAML("not: [yaml"); len(got) != 1 || got[""] == "" {
		t.Errorf("not YAML: %v", got)
	}
}
