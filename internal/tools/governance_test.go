package tools

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// rowan's agent-platform values and an empty values document.
const (
	rowanValues = "installations/rowan/apps/agent-platform/configmap-values.yaml.patch"
	emptyValues = "global: {}\n"
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
// second person approves it, then what loosens it and only then the change
// old → new; one that changes none says nothing of it.
func TestReviewTextLeadsWithTheGovernance(t *testing.T) {
	text := reviewText(governedAction())
	head := "\n*Governance:* this changes the platform's governance; a second person approves it, on test installations too." +
		"\n*It loosens governance:* garm: governedServers.mcp-kubernetes the chart's entry → null (not governed)" +
		"\n• garm: mode trusted (the chart's default) → governed; governedServers.mcp-kubernetes the chart's entry → null (not governed)"
	if !strings.Contains(text, head) {
		t.Fatalf("the review carries no %q:\n%s", head, text)
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

// S9-03: what loosens comes first and is never cut, within what Slack shows
// of a section, however long the rest of the change; the rest is cut with
// the configs pull request named. A change whose loosening lines alone do
// not fit is refused.
func TestReviewTextKeepsWhatLoosensWhole(t *testing.T) {
	var pad []string
	for i := range 130 {
		pad = append(pad, fmt.Sprintf("list_inventory_items_%03d", i))
	}
	current := "governance: governed\ngovernedServers:\n  agent-manager:\n    safe: [get_info]\n"
	next := "governance: governed\ngovernedServers:\n  a-inventory:\n    role: infrastructure\n    safe: [" + strings.Join(pad, ", ") + "]\n  agent-manager:\n    safe: [get_info, create_agent, update_agent_access]\n"
	p := plan.Installation{Name: rowan, Files: []plan.File{{Path: rowanValues, Change: plan.ChangeUpdate, Current: current, Content: next}}}
	g, ok := plan.GovernanceOf(p)
	if !ok {
		t.Fatal("no change")
	}
	a := &actions.Action{Spec: actions.Spec{Actor: actions.Actor{Login: stAlice}, Kind: actions.KindReconcile, Capability: installations.AgentPlatform, Installations: []string{rowan}, Reason: "inventory server",
		Governance: map[string]plan.GovernanceChange{rowan: g}, Changes: map[string][]string{rowan: plan.Summary(p)}},
		Status: actions.Status{PullRequests: []actions.PullRequest{{Repository: "giantswarm/acme-configs", Number: 7, URL: "https://github.com/giantswarm/acme-configs/pull/7"}}}}
	text := []rune("*Review for team*\n" + reviewText(a))
	shown := string(text[:min(len(text), 3000)])
	for _, want := range []string{"*It loosens governance:* rowan: governedServers.agent-manager.safe +create_agent (named like a write) +update_agent_access (named like a write)", "in full in the configs pull request: <https://github.com/giantswarm/acme-configs/pull/7|giantswarm/acme-configs#7>"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("what Slack shows carries no %q:\n%s", want, shown)
		}
	}
	if err := governanceTooLarge(ToolReconcileCapability, a); err != nil {
		t.Fatalf("a change whose loosening lines fit: %v", err)
	}
	big := plan.GovernanceChange{Lines: []string{"x"}}
	for i := range 60 {
		big.Loosens = append(big.Loosens, fmt.Sprintf("governedServers.s%02d.safe +list_items", i))
	}
	if err := governanceTooLarge(ToolReconcileCapability, &actions.Action{Spec: actions.Spec{Governance: map[string]plan.GovernanceChange{rowan: big}}}); err == nil || !strings.Contains(err.Error(), "split it") {
		t.Fatalf("a change whose loosening lines do not fit: %v", err)
	}
}

// S9-07: a stage merged outside the manager decides nothing for a
// governance change; for any other change it carries the stages after it.
func TestMergedWithoutApprovalDecidesNoGovernanceChange(t *testing.T) {
	a := governedAction()
	a.Status.Approval = &actions.Approval{Decision: actions.DecisionMergedWithoutApproval}
	if decided(a) {
		t.Fatal("a governance change merged outside the manager counts as decided")
	}
	a.Spec.Governance = nil
	if !decided(a) {
		t.Fatal("a change merged outside the manager no longer carries its later stages")
	}
}

// S9-15: an Action the hub stored without the governance it was created
// with is refused.
func TestGovernanceStoredOrRefused(t *testing.T) {
	want := governedAction().Spec.Governance
	if err := governanceStored(ToolEnableCapability, want, &actions.Action{}); err == nil || !strings.Contains(err.Error(), "lacks spec.governance") {
		t.Fatalf("an action stored without its governance: %v", err)
	}
	if err := governanceStored(ToolEnableCapability, want, governedAction()); err != nil {
		t.Fatal(err)
	}
	if err := governanceStored(ToolEnableCapability, nil, &actions.Action{}); err != nil {
		t.Fatal(err)
	}
}

// D-GO-11: agent-platform values that do not parse are a loosening
// governance change on the path commits use, never none.
func TestGovernanceChangeOfUnreadableValues(t *testing.T) {
	tl := &Tools{d: Deps{Log: slog.New(slog.DiscardHandler)}}
	p := plan.Installation{Name: rowan, Files: []plan.File{{Path: rowanValues, Change: plan.ChangeUpdate, Current: "governance: [\n", Content: emptyValues}}}
	g, after := tl.governanceChange(context.Background(), p)
	if g == nil || len(g.Loosens) != 1 || after != "" {
		t.Fatalf("unreadable values: %+v %q", g, after)
	}
}

// D-GO-11: values on the default branch that cannot be read again count as
// moved (fail closed).
func TestGovernanceRecordMovedWhenTheValuesCannotBeRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(srv.Close)
	tl := &Tools{d: Deps{Log: slog.New(slog.DiscardHandler), GitHubAPIURL: srv.URL + "/api/v3", Files: gh.NewFiles(0)}}
	p := plan.Installation{Name: rowan, Files: []plan.File{{Repository: "giantswarm/acme-configs", Path: rowanValues, Change: plan.ChangeUpdate, Current: emptyValues, Content: emptyValues}}}
	ctx := identity.ContextWithToken(context.Background(), "token")
	if moved, what := tl.governanceRecordMoved(ctx, p); !moved || !strings.Contains(what, "could not be read again") {
		t.Fatalf("an unreadable default branch: %v %q", moved, what)
	}
}
