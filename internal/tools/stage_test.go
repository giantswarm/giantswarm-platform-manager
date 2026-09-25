package tools

import (
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/approvals"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

const (
	stAcme  = "acme"
	stAlice = "alice"
)

// A test installation is the hub's customer's and not the hub: the wave's
// first stage. The hub and every customer's installation are production.
func TestTestInstallationIsTheWavesFirstStage(t *testing.T) {
	hub := installations.Installation{Name: rtHub, Customer: rtOwnCustomer}
	for _, tc := range []struct {
		name, customer string
		want           bool
	}{
		{rtGarm, rtOwnCustomer, true},
		{rtHub, rtOwnCustomer, false},
		{rowan, stAcme, false},
	} {
		if got := testInstallation(tc.name, tc.customer, hub); got != tc.want {
			t.Errorf("testInstallation(%s of %s) = %v, want %v", tc.name, tc.customer, got, tc.want)
		}
	}
	// The wave's order puts the test installations first, by the same rule.
	order := waveOrder([]installations.Report{{Installation: installations.Installation{Name: rowan, Customer: stAcme}}, {Installation: installations.Installation{Name: rtHub, Customer: rtOwnCustomer}}, {Installation: installations.Installation{Name: rtGarm, Customer: rtOwnCustomer}}}, hub)
	if order[0].Name != rtGarm || order[1].Name != rtHub || order[2].Name != rowan {
		t.Fatalf("wave order: %s, %s, %s", order[0].Name, order[1].Name, order[2].Name)
	}
}

// An action that needs no review may be merged; one pending the team's
// decision may not; the standup sentence says who did what where.
func TestNoReviewDecidesAndTellsOneSentence(t *testing.T) {
	a := &actions.Action{Spec: actions.Spec{Kind: actions.KindReconcile, Capability: "agent-platform"}, Status: actions.Status{Approval: &actions.Approval{Decision: actions.DecisionNotRequired}}}
	if !decided(a) || !noReview(a) {
		t.Fatal("an action that needs no review is not decided")
	}
	if got, want := standupText(a, stageMerge{by: stAlice, stage: rtGarm}), "*alice* reconciled *agent-platform* on *garm*."; got != want {
		t.Fatalf("standup text %q, want %q", got, want)
	}
	a.Spec.Kind = actions.KindEnable
	if got, want := standupText(a, stageMerge{by: stAlice, stage: rtGarm}), "*alice* enabled *agent-platform* on *garm*."; got != want {
		t.Fatalf("standup text %q, want %q", got, want)
	}
	pending := &actions.Action{Status: actions.Status{Approval: &actions.Approval{ReviewID: "rev-1"}}}
	if decided(pending) || noReview(pending) {
		t.Fatal("an action pending the team's decision is decided")
	}
}

// A commit on test installations alone is refused while no standup channel
// is configured; one that keeps the review is not.
func TestStandupChannelIsRequiredForTestInstallations(t *testing.T) {
	without := &Tools{approvals: approvals.New(approvals.Config{GatewayURL: "http://gateway", Team: "t", Channel: "C1"}, nil)}
	if err := without.standupRefusal(ToolReconcileCapability, true); err == nil {
		t.Fatal("a commit on a test installation without a standup channel was accepted")
	}
	if err := without.standupRefusal(ToolReconcileCapability, false); err != nil {
		t.Fatalf("a commit that keeps the review: %v", err)
	}
	with := &Tools{approvals: approvals.New(approvals.Config{GatewayURL: "http://gateway", Team: "t", Channel: "C1", StandupChannel: "C2"}, nil)}
	if err := with.standupRefusal(ToolReconcileCapability, true); err != nil {
		t.Fatalf("with a standup channel: %v", err)
	}
}
