package verify

import (
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// A live path walks maps, greedy over keys that contain dots, indexes lists,
// crosses into a YAML document stored in a string field, and picks an
// argument by its prefix.
func TestResolveWalksLiveObjects(t *testing.T) {
	obj := map[string]any{
		"data": map[string]any{"config.yaml": "aggregator:\n  oauth:\n    server:\n      trustedAudiences: [a, b]\n      dex: {clientId: muster-x}\n"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"args": []any{"--provider=oidc", "--oidc-extra-audience=x,y"}},
		}}}, "values": map[string]any{"providers": map[string]any{"anthropic": map[string]any{"apiKeySecretRef": "k"}}}},
	}
	cases := []struct {
		path, prefix string
		want         any
		ok           bool
	}{
		{"data.config.yaml:aggregator.oauth.server.dex.clientId", "", "muster-x", true},
		{"data.config.yaml:aggregator.oauth.server.trustedAudiences", "", []any{"a", "b"}, true},
		{"spec.template.spec.containers[0].args[args]", "--oidc-extra-audience=", "x,y", true},
		{"spec.template.spec.containers[0].args[args]", "--missing=", nil, false},
		{"spec.values.providers.anthropic.apiKeySecretRef", "", "k", true},
		{"spec.values.nothing", "", nil, false},
		{"data.config.yaml:aggregator.nothing", "", nil, false},
		{"spec.template.spec.containers[3]", "", nil, false},
	}
	for _, c := range cases {
		got, ok := resolve(obj, c.path, c.prefix)
		if ok != c.ok {
			t.Errorf("%s: ok %v, want %v (%v)", c.path, ok, c.ok, got)
			continue
		}
		if !ok {
			continue
		}
		flatGot, flatWant := map[string]string{}, map[string]string{}
		flatten(got, "", flatGot)
		flatten(c.want, "", flatWant)
		if len(diffPaths(flatWant, flatGot)) > 0 {
			t.Errorf("%s: got %v, want %v", c.path, got, c.want)
		}
	}
}

func TestSubtreeIsTheLeavesUnderAPath(t *testing.T) {
	flat := map[string]string{"a.b": "1", "a.c[0]": "2", "a.c[1]": "3", "ab": "x", "d": "4"}
	got := subtree(flat, "a")
	if len(got) != 3 || got["b"] != "1" || got["c[0]"] != "2" || got["c[1]"] != "3" {
		t.Errorf("subtree a: %v", got)
	}
	if got := subtree(flat, "d"); len(got) != 1 || got[""] != "4" {
		t.Errorf("subtree d: %v", got)
	}
	if got := subtree(flat, "zz"); len(got) != 0 {
		t.Errorf("subtree zz: %v", got)
	}
}

// Merge takes, per dimension, the side that checked it; a live dimension is
// the live side's whatever it says; drifted and waiting for the customer
// come from either side.
func TestMergeTakesTheCheckedSide(t *testing.T) {
	repo := Result{State: installations.StateEnabled, Caller: "alice", Features: []Feature{{ID: "f", Dimensions: []Dimension{
		{ID: "file", Kind: definitions.KindConfigMap, Mark: AsDefined},
		{ID: "live-a", Kind: definitions.KindLive, Mark: NotChecked, Reason: ReasonAuthority},
		{ID: "live-b", Kind: definitions.KindLive, Mark: NotChecked, Reason: ReasonAuthority},
		{ID: "probe", Kind: definitions.KindProbe, Mark: Drifted},
	}}}}
	live := Result{State: installations.StateWaitingForCustomer, Caller: "alice@example.test", Features: []Feature{{ID: "f", Dimensions: []Dimension{
		{ID: "file", Kind: definitions.KindConfigMap, Mark: NotChecked, Reason: ReasonRepositorySide},
		{ID: "live-a", Kind: definitions.KindLive, Mark: Drifted},
		{ID: "live-b", Kind: definitions.KindLive, Mark: NotChecked, Reason: ReasonNoProbe},
		{ID: "probe", Kind: definitions.KindProbe, Mark: NotChecked, Reason: ReasonRepositorySide},
	}}}}
	out := Merge(repo, live)
	dims := map[string]Dimension{}
	for _, d := range out.Features[0].Dimensions {
		dims[d.ID] = d
	}
	if dims["file"].Mark != AsDefined || dims["probe"].Mark != Drifted {
		t.Errorf("the repository side's checked dimensions stand: %+v %+v", dims["file"], dims["probe"])
	}
	if dims["live-a"].Mark != Drifted || dims["live-b"].Reason != ReasonNoProbe {
		t.Errorf("the live side's word on live dimensions: %+v %+v", dims["live-a"], dims["live-b"])
	}
	if out.Features[0].Mark != Drifted || out.Summary[Drifted] != 2 || out.Summary[NotChecked] != 1 {
		t.Errorf("roll-up %q summary %v", out.Features[0].Mark, out.Summary)
	}
	if out.State != installations.StateWaitingForCustomer || out.Caller != "alice" || out.LiveCaller != "alice@example.test" {
		t.Errorf("state %q caller %q live caller %q", out.State, out.Caller, out.LiveCaller)
	}
	live.State = installations.StateDrifted
	repo.State = installations.StateWaitingForCustomer
	if out := Merge(repo, live); out.State != installations.StateDrifted {
		t.Errorf("drifted wins over waiting: %q", out.State)
	}
}

func TestDeepMergeReplacesEverythingButMaps(t *testing.T) {
	base := map[string]any{"a": map[string]any{"x": 1, "y": []any{1}}, "b": "old"}
	deepMerge(base, map[string]any{"a": map[string]any{"y": []any{2, 3}, "z": true}, "b": map[string]any{"n": 1}})
	flat := map[string]string{}
	flatten(base, "", flat)
	want := map[string]string{"a.x": "1", "a.y[0]": "2", "a.y[1]": "3", "a.z": "true", "b.n": "1"}
	if d := diffPaths(want, flat); len(d) > 0 {
		t.Errorf("merged %v, differs at %v", flat, d)
	}
}
