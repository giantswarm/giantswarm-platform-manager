package tools

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// suppliedByTarget: a bare field reaches every target that asks for it, an
// <installation>/<field> key that target alone and before the bare one; a
// field a target misses and a key no target asks for are refused by name,
// never by value.
func TestSuppliedByTarget(t *testing.T) {
	const (
		modelKey = "kagent.modelKey"
		pairing  = "muster-token-exchange-oak-hazel-client-secret"
		value    = "PLACEHOLDER-VALUE"
	)
	oak := plan.Installation{Name: peerSide, SuppliedSecrets: []string{modelKey, pairing}}
	hazel := plan.Installation{Name: hubSide, SuppliedSecrets: []string{modelKey, pairing}}
	birch := plan.Installation{Name: "birch"}

	got, err := suppliedByTarget(map[string]string{
		pairing:                  value + "-pair",
		modelKey:                 value + "-shared",
		hubSide + "/" + modelKey: value + "-hazel",
	}, []plan.Installation{oak, hazel, birch})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		peerSide: {modelKey: value + "-shared", pairing: value + "-pair"},
		hubSide:  {modelKey: value + "-hazel", pairing: value + "-pair"},
		"birch":  {},
	}
	for name, fields := range want {
		if len(got[name]) != len(fields) {
			t.Errorf("%s: %d value(s), want %d", name, len(got[name]), len(fields))
		}
		for f, v := range fields {
			if got[name][f] != v {
				t.Errorf("%s.%s: got %q, want %q", name, f, got[name][f], v)
			}
		}
	}

	for _, c := range []struct {
		secrets map[string]string
		want    string
	}{
		{map[string]string{pairing: value, peerSide + "/" + modelKey: value}, "misses the value(s) of " + hubSide + ": " + modelKey},
		{map[string]string{}, "misses the value(s) of " + peerSide + ": " + modelKey + ", " + pairing + "; " + hubSide + ": " + modelKey + ", " + pairing},
		{map[string]string{pairing: value, modelKey: value, "birch/" + modelKey: value}, "names birch/" + modelKey + ", which the plan does not ask for"},
		{map[string]string{pairing: value, modelKey: value, "slack.token": value}, "names slack.token"},
	} {
		_, err := suppliedByTarget(c.secrets, []plan.Installation{oak, hazel, birch})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: error %v lacks %q", keys(c.secrets), err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), value) {
			t.Errorf("the refusal names a value: %v", err)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
