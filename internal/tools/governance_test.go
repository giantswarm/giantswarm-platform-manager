package tools

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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
	g, after, _ := tl.governanceChange(context.Background(), p)
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

// fakeRecords is an in-memory RecordStore holding at most one record.
type fakeRecords struct {
	have *actions.GovernanceRecord
	put  []actions.GovernanceRecord
}

func (f *fakeRecords) GetRecord(context.Context, string) (*actions.GovernanceRecord, error) {
	if f.have == nil {
		return nil, actions.ErrNotFound
	}
	return f.have, nil
}

func (f *fakeRecords) PutRecord(_ context.Context, r actions.GovernanceRecord, expected string) error {
	if actions.RecordIdentity(f.have) != expected {
		return actions.ErrRecordMoved
	}
	f.put = append(f.put, r)
	return nil
}

// Decision #99: only a team-approved governance change writes the record;
// an approved action without one, or one without the team's approval,
// writes none.
func TestOnlyAnApprovedGovernanceChangeIsRecorded(t *testing.T) {
	for _, c := range []struct {
		name     string
		decision string
		governs  bool
		writes   bool
		noAfter  bool
		base     string
	}{
		{"approved with a governance change", actions.DecisionApproved, true, true, false, actions.RecordNone},
		{"approved without one", actions.DecisionApproved, false, false, false, actions.RecordNone},
		{"not required, with one", actions.DecisionNotRequired, true, false, false, actions.RecordNone},
		{"merged without approval, with one", actions.DecisionMergedWithoutApproval, true, false, false, actions.RecordNone},
		{"approved without the governance it leaves", actions.DecisionApproved, true, false, true, actions.RecordNone},
		{"approved over another record than its plan's", actions.DecisionApproved, true, false, false, "older@2026-11-26T00:00:00Z@1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			records := &fakeRecords{}
			tl := &Tools{d: Deps{Log: slog.New(slog.DiscardHandler), Records: records}}
			a := governedAction()
			a.Spec.Installations = []string{rowan}
			a.Spec.Governance = map[string]plan.GovernanceChange{rowan: a.Spec.Governance[rtGarm]}
			if !c.governs {
				a.Spec.Governance = nil
			}
			a.Spec.GovernanceAfter = map[string]string{rowan: `{"governance":{"mode":"governed"}}`}
			if c.noAfter {
				a.Spec.GovernanceAfter = nil
			}
			a.Spec.GovernanceBase = map[string]string{rowan: c.base}
			a.Status.Approval = &actions.Approval{Decision: c.decision, DecidedBy: "carol"}
			tl.recordReviewed(context.Background(), a, rowan)
			if (len(records.put) == 1) != c.writes {
				t.Fatalf("records written: %+v", records.put)
			}
		})
	}
}

// D-GO-31: decided reads the governance found after the commit too: a
// stage merged outside the manager decides nothing for such an action.
func TestMergedWithoutApprovalDecidesNoGovernanceFoundAfterTheCommit(t *testing.T) {
	a := &actions.Action{Status: actions.Status{Approval: &actions.Approval{Decision: actions.DecisionMergedWithoutApproval},
		Governance: map[string]plan.GovernanceChange{rowan: {Lines: []string{"moved"}, Loosens: []string{"moved"}}}}}
	if decided(a) {
		t.Fatal("a governance change found after the commit, merged outside the manager, counts as decided")
	}
}

// D-GO-34, D-GO-40: without a record (none, or one that does not parse),
// governance in play on either side leads with the unrecorded line and is
// shown whole against the chart's defaults, the platform's parts included.
func TestWithoutARecordTheReviewShowsTheWholeGovernance(t *testing.T) {
	const parts = "governance:\n  roleFloors:\n    platformManager: [approve_action, merge_action]\n"
	for _, c := range []struct {
		name          string
		have          *actions.GovernanceRecord
		current, next string
	}{
		{"no record", nil, parts, parts},
		{"a record that does not parse", &actions.GovernanceRecord{Action: "x", Governance: "{"}, parts, parts},
		{"only the values on record carry it", nil, parts, emptyValues},
	} {
		t.Run(c.name, func(t *testing.T) {
			tl := &Tools{d: Deps{Log: slog.New(slog.DiscardHandler), Records: &fakeRecords{have: c.have}}}
			p := plan.Installation{Name: rowan, Files: []plan.File{{Path: rowanValues, Change: plan.ChangeUpdate, Current: c.current, Content: c.next}}}
			g, _, record := tl.governanceChange(context.Background(), p)
			if g == nil || !strings.HasPrefix(g.Lines[0], "no reviewed governance is on record for rowan") || record != actions.RecordNone {
				t.Fatalf("got %+v, record %q", g, record)
			}
			if c.next == parts && !slices.Contains(g.Lines, "roleFloors.platformManager +approve_action +merge_action") {
				t.Fatalf("the governance confirmed is not shown whole: %+v", g)
			}
		})
	}
}
