package tools

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
)

// Governing tool calls (giantswarm/giantswarm#38902): one person never
// changes an installation's governance alone. A commit whose plan changes
// the governance (plan.GovernanceOf) records the change on the Action
// (spec.governance) and asks the team's review even where every target is a
// test installation; approve_action refuses the actor as for any action, so
// a second person of the team decides, and merge_action waits for that
// decision. Each step is audited (auditGovernance). Agents may propose such
// a change and never approve or merge it: muster asks a person before an
// agent's approve_action and merge_action (a floor of the platformManager
// role).

// governanceText is the review's governance block: that the action changes
// the governance and a second person approves it, each installation's
// change old → new, and what loosens it, so the approver reads it before
// anything else. Empty for an action that changes none.
func governanceText(a *actions.Action) string {
	if len(a.Spec.Governance) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n*Governance:* this changes the platform's governance; a second person approves it, on test installations too.")
	var loosens []string
	for _, n := range governedInstallations(a) {
		g := a.Spec.Governance[n]
		b.WriteString("\n• " + n + ": " + strings.Join(g.Lines, "; "))
		if len(g.Loosens) > 0 {
			loosens = append(loosens, n+": "+strings.Join(g.Loosens, "; "))
		}
	}
	if len(loosens) > 0 {
		b.WriteString("\n*It loosens governance:* " + strings.Join(loosens, " — "))
	}
	return b.String()
}

// governedInstallations are the installations whose governance a changes,
// in the action's order, then any other on record by name.
func governedInstallations(a *actions.Action) []string {
	var out []string
	for _, n := range a.Spec.Installations {
		if _, ok := a.Spec.Governance[n]; ok {
			out = append(out, n)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(a.Spec.Governance)) {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// auditGovernance logs event once per installation whose governance a
// changes: the action, the actor, the caller, who decided where someone
// did, the change and whether it loosens. Nothing for an action that
// changes none.
func (t *Tools) auditGovernance(ctx context.Context, event string, a *actions.Action) {
	decidedBy, decision := "", ""
	if a.Status.Approval != nil {
		decidedBy, decision = a.Status.Approval.DecidedBy, a.Status.Approval.Decision
	}
	for _, n := range governedInstallations(a) {
		g := a.Spec.Governance[n]
		t.d.Log.Info(event, identity.LogAttr(ctx), "audit", true, "action", a.Name, "installation", n, "actor", a.Spec.Actor.Login,
			"decision", decision, "decidedBy", decidedBy, "governance", strings.Join(g.Lines, "; "), "loosens", len(g.Loosens) > 0)
	}
}
