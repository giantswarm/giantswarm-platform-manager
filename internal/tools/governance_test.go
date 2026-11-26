package tools

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// governedAction is an action on garm whose commit makes the platform
// governed and drops a source from the governed servers.
func governedAction() *actions.Action {
	return &actions.Action{Spec: actions.Spec{Kind: actions.KindReconcile, Capability: installations.AgentPlatform, Installations: []string{rtGarm}, Actor: actions.Actor{Login: stAlice},
		Governance: map[string]plan.GovernanceChange{rtGarm: {
			Lines:   []string{"mode trusted (the chart's default) → governed", "governedServers.mcp-kubernetes the chart's entry → null (not governed)"},
			Loosens: []string{"governedServers.mcp-kubernetes the chart's entry → null (not governed)"},
		}}}}
}

// The review of an action that changes the governance says so first, that a
// second person approves it, the change old → new and what loosens it; one
// that changes none says nothing of it.
func TestReviewTextLeadsWithTheGovernance(t *testing.T) {
	text := reviewText(governedAction())
	for _, want := range []string{
		"\n*Governance:* this changes the platform's governance; a second person approves it, on test installations too.",
		"\n• garm: mode trusted (the chart's default) → governed; governedServers.mcp-kubernetes the chart's entry → null (not governed)",
		"\n*It loosens governance:* garm: governedServers.mcp-kubernetes the chart's entry → null (not governed)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the review carries no %q:\n%s", want, text)
		}
	}
	if text := reviewText(&actions.Action{Spec: actions.Spec{Kind: actions.KindReconcile, Capability: installations.AgentPlatform, Installations: []string{rtGarm}}}); strings.Contains(text, "Governance") {
		t.Fatalf("a review without a governance change names it:\n%s", text)
	}
}

// Every step of a governance change is audited, one line per installation
// with the change, what loosens, the actor and, once decided, who approved.
func TestGovernanceChangeIsAudited(t *testing.T) {
	var buf bytes.Buffer
	tl := &Tools{d: Deps{Log: slog.New(slog.NewTextHandler(&buf, nil))}}
	a := governedAction()
	a.Name = "reconcile-x"
	a.Status.Approval = &actions.Approval{Decision: actions.DecisionApproved, DecidedBy: "carol"}
	tl.auditGovernance(context.Background(), "governance_change_approved", a)
	line := buf.String()
	for _, want := range []string{"msg=governance_change_approved", "action=reconcile-x", "installation=garm", "actor=alice", "decidedBy=carol", "loosens=true", "mode trusted (the chart's default) → governed"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the audit line carries no %q: %s", want, line)
		}
	}
	buf.Reset()
	tl.auditGovernance(context.Background(), "governance_change_approved", &actions.Action{})
	if buf.Len() != 0 {
		t.Fatalf("an action without a governance change is audited: %s", buf.String())
	}
}
