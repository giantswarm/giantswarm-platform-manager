package tools

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// The review names the account engineer of a customer installation as the
// catalog records it, a gap as such, and for a wave every customer's once, in
// the rollout order; an installation of the hub's customer names none.
func TestAccountEngineersOfTheTargets(t *testing.T) {
	env := &planned{
		hub: installations.Installation{Name: "gazelle", Customer: "giantswarm"},
		byName: map[string]installations.Installation{
			"gazelle": {Name: "gazelle", Customer: "giantswarm"},
			"garm":    {Name: "garm", Customer: "giantswarm", AccountEngineer: "Team Phoenix"},
			"rowan":   {Name: "rowan", Customer: "acme", AccountEngineer: "Ada Example"},
			"birch":   {Name: "birch", Customer: "acme", AccountEngineer: "Ada Example"},
			"maple":   {Name: "maple", Customer: "globex"},
		},
	}
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"rowan"}, "Ada Example"},
		{[]string{"garm"}, ""},
		{[]string{"maple"}, "none on record for maple"},
		{[]string{"gazelle", "rowan", "birch", "maple"}, "Ada Example,none on record for maple"},
	} {
		if got := strings.Join(accountEngineers(env, tc.names...), ","); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.names, got, tc.want)
		}
	}
}

func TestReviewTextNamesTheAccountEngineer(t *testing.T) {
	actor := actions.Actor{Login: "alice"}
	for _, tc := range []struct {
		spec actions.Spec
		want string
	}{
		{actions.Spec{Actor: actor, Kind: actions.KindEnable, Capability: "agent-platform", Installations: []string{"garm"}},
			"*alice* asks to enable *agent-platform* on *garm*."},
		{actions.Spec{Actor: actor, Kind: actions.KindEnable, Capability: "agent-platform", Installations: []string{"rowan"}, Customer: true, AccountEngineers: []string{"Ada Example"}},
			"*alice* asks to enable *agent-platform* on *rowan* (a customer installation; account engineer Ada Example)."},
		{actions.Spec{Actor: actor, Kind: actions.KindEnable, Capability: "agent-platform", Installations: []string{"maple"}, Customer: true, AccountEngineers: []string{"none on record for maple"}},
			"*alice* asks to enable *agent-platform* on *maple* (a customer installation; account engineer none on record for maple)."},
		{actions.Spec{Actor: actor, Kind: actions.KindReconcile, Capability: "agent-platform", Installations: []string{"gazelle", "rowan", "maple"}, Customer: true, AccountEngineers: []string{"Ada Example", "none on record for maple"}},
			"*alice* asks to reconcile *agent-platform* on *gazelle, rowan, maple* (customer installations among them; account engineers Ada Example, none on record for maple) (a wave, in this order)."},
	} {
		if got := reviewText(&actions.Action{Spec: tc.spec}); got != tc.want {
			t.Errorf("got  %s\nwant %s", got, tc.want)
		}
	}
}
