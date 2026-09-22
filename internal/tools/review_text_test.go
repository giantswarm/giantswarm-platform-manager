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
	actor := actions.Actor{Login: "alice"}
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
