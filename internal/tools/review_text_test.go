package tools

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

const (
	rtHub         = "gazelle"
	rtGarm        = "garm"
	rtMaple       = "maple"
	rtAda         = "Ada Example"
	rtNoneMaple   = "none on record for " + rtMaple
	rtOwnCustomer = "giantswarm"
)

// The review names the account engineer of a customer installation as the
// catalog records it, a gap as such, and for a wave every customer's once, in
// the rollout order; an installation of the hub's customer names none.
func TestAccountEngineersOfTheTargets(t *testing.T) {
	env := &planned{
		hub: installations.Installation{Name: rtHub, Customer: rtOwnCustomer},
		byName: map[string]installations.Installation{
			rtHub:   {Name: rtHub, Customer: rtOwnCustomer},
			rtGarm:  {Name: rtGarm, Customer: rtOwnCustomer, AccountEngineer: "Team Phoenix"},
			rowan:   {Name: rowan, Customer: "acme", AccountEngineer: rtAda},
			birch:   {Name: birch, Customer: "acme", AccountEngineer: rtAda},
			rtMaple: {Name: rtMaple, Customer: "globex"},
		},
	}
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{rowan}, rtAda},
		{[]string{rtGarm}, ""},
		{[]string{rtMaple}, rtNoneMaple},
		{[]string{rtHub, rowan, birch, rtMaple}, rtAda + "," + rtNoneMaple},
	} {
		if got := strings.Join(accountEngineers(env, tc.names...), ","); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.names, got, tc.want)
		}
	}
}

func TestReviewTextNamesTheAccountEngineer(t *testing.T) {
	actor := actions.Actor{Login: stAlice}
	spec := func(kind string, customer bool, aes []string, names ...string) actions.Spec {
		return actions.Spec{Actor: actor, Kind: kind, Capability: installations.AgentPlatform, Installations: names, Customer: customer, AccountEngineers: aes}
	}
	for _, tc := range []struct {
		spec actions.Spec
		want string
	}{
		{spec(actions.KindEnable, false, nil, rtGarm),
			"*alice* asks to enable *agent-platform* on *garm*."},
		{spec(actions.KindEnable, true, []string{rtAda}, rowan),
			"*alice* asks to enable *agent-platform* on *rowan* (a customer installation; account engineer Ada Example)."},
		{spec(actions.KindEnable, true, []string{rtNoneMaple}, rtMaple),
			"*alice* asks to enable *agent-platform* on *maple* (a customer installation; account engineer none on record for maple)."},
		{spec(actions.KindReconcile, true, []string{rtAda, rtNoneMaple}, rtHub, rowan, rtMaple),
			"*alice* asks to reconcile *agent-platform* on *gazelle, rowan, maple* (customer installations among them; account engineers Ada Example, none on record for maple) (a wave, in this order)."},
	} {
		if got := reviewText(&actions.Action{Spec: tc.spec}); got != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
}

// The review says why and what changes: the reason quoted, a line per
// component; a wave whose installations change alike reads once "on each",
// else per installation; lines past whatMax are counted, never cut mid-line.
func TestReviewTextSaysWhyAndWhat(t *testing.T) {
	spec := actions.Spec{Actor: actions.Actor{Login: stAlice}, Kind: actions.KindReconcile, Capability: installations.ClusterMCPServers, Reason: "mcp-kubernetes 1.8.2 fixes the token refresh",
		Installations: []string{rowan, birch}, Changes: map[string][]string{rowan: {"mcp-kubernetes: HelmRelease mcp-kubernetes 1.8.1 → 1.8.2"}, birch: {"mcp-kubernetes: HelmRelease mcp-kubernetes 1.8.1 → 1.8.2"}}}
	want := "*alice* asks to reconcile *cluster-mcp-servers* on *rowan, birch* (a wave, in this order).\n>*Why:* mcp-kubernetes 1.8.2 fixes the token refresh\n*What changes on each*\n• mcp-kubernetes: HelmRelease mcp-kubernetes 1.8.1 → 1.8.2"
	if got := reviewText(&actions.Action{Spec: spec}); got != want {
		t.Errorf("alike:\ngot  %s\nwant %s", got, want)
	}
	spec.Changes[birch] = []string{"mcp-capi: new"}
	want = "*alice* asks to reconcile *cluster-mcp-servers* on *rowan, birch* (a wave, in this order).\n>*Why:* mcp-kubernetes 1.8.2 fixes the token refresh\n*What changes on rowan*\n• mcp-kubernetes: HelmRelease mcp-kubernetes 1.8.1 → 1.8.2\n*What changes on birch*\n• mcp-capi: new"
	if got := reviewText(&actions.Action{Spec: spec}); got != want {
		t.Errorf("different:\ngot  %s\nwant %s", got, want)
	}
	long := make([]string, 60)
	for i := range long {
		long[i] = "component-" + strings.Repeat("x", 60)
	}
	spec.Installations, spec.Changes = []string{rowan}, map[string][]string{rowan: long}
	got := reviewText(&actions.Action{Spec: spec})
	if len(got) > 3000 || !strings.HasSuffix(got, " more; the pull requests carry the full change") {
		t.Errorf("long (%d characters): …%s", len(got), got[len(got)-80:])
	}
}

// The Account Engineers' notice once applied: who, what, where, whose
// customer, why and what changed.
func TestAppliedTextNamesTheAccountEngineer(t *testing.T) {
	a := &actions.Action{Spec: actions.Spec{Actor: actions.Actor{Login: stAlice}, Kind: actions.KindReconcile, Capability: installations.ClusterMCPServers, Reason: "rotate after the leak",
		Installations: []string{rowan}, AccountEngineerOf: map[string]string{rowan: rtAda}, Changes: map[string][]string{rowan: {"mcp-kubernetes: rotates valkey-password (on request)"}}}}
	want := "*alice* reconciled *cluster-mcp-servers* on *rowan* (account engineer Ada Example): the change is applied and verified.\n>*Why:* rotate after the leak\n*What changed*\n• mcp-kubernetes: rotates valkey-password (on request)"
	if got := appliedText(a, rowan); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
