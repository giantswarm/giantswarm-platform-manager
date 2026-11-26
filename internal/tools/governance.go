package tools

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// Governing tool calls (giantswarm/giantswarm#38902): one person never
// changes an installation's governance alone. A commit whose plan changes
// the governance (governanceChange) records the change on the Action
// (spec.governance) and asks the team's review even where every target is a
// test installation; approve_action refuses the actor and anyone who is not
// an active member of the governance reviewers' team (approvals.governanceTeam),
// and merge_action waits for that decision. Every Action records the
// governance its commit leaves (spec.governanceAfter): the next plan compares
// against it, so a disable followed by an enable, or a change made in the
// configs repository directly, is a governance change again and needs the
// review. Each step is audited (auditGovernance). Agents may propose such a
// change and never approve or merge it: muster asks a person before an
// agent's approve_action and merge_action (a floor of the platformManager
// role).

// baselineStates are the states of an Action whose stage is on record: its
// pull requests merged, not taken back.
var baselineStates = []string{actions.StateRollingOut, actions.StateWaitingForCustomer, actions.StateEnabled, actions.StateDrifted, actions.StateDisabled, actions.StateFailed}

// governanceBaseline is the governance the newest reviewed action left on
// installation: an agent-platform action whose stage there is merged and on
// record, approved by the team or needing no review, with a recorded
// governance. An action merged without the team's decision is skipped, so
// what it left counts as changed outside the manager. None (false) before
// the first action that records one, or without the records.
func (t *Tools) governanceBaseline(ctx context.Context, installation string) (plan.GovernanceSnapshot, string, bool) {
	if t.d.Actions == nil {
		return nil, "", false
	}
	list, err := t.d.Actions.List(ctx, actions.Filter{Installation: installation})
	if err != nil {
		t.d.Log.Warn("governance_baseline_unread", identity.LogAttr(ctx), "installation", installation, "error", err.Error())
		return nil, "", false
	}
	for _, a := range list {
		if a.CapabilityOf(installation) != installations.AgentPlatform || !slices.Contains(baselineStates, a.Status.State) || !allMerged(&a, installation) || len(a.StagePullRequests(installation)) == 0 {
			continue
		}
		if a.Status.Approval == nil || (a.Status.Approval.Decision != actions.DecisionApproved && a.Status.Approval.Decision != actions.DecisionNotRequired) {
			continue
		}
		raw, ok := a.Spec.GovernanceAfter[installation]
		if !ok {
			return nil, "", false
		}
		snap, err := plan.ParseGovernanceSnapshot(raw)
		if err != nil {
			return nil, "", false
		}
		return snap, a.Name, true
	}
	return nil, "", false
}

// governanceOutside is the line of a plan whose record differs from what
// the last reviewed action left: the change is shown against that action.
func governanceOutside(action string) string {
	return "the governance on record differs from what action " + action + " left (changed outside the manager); the change is shown against " + action
}

// governanceChange is the governance a commit of p changes, against what
// the last reviewed action left where it is known, else against the record,
// and the snapshot the commit leaves; nil for a plan that changes none or
// writes no agent-platform values. A record that differs from the last
// reviewed action's is logged (governance_on_record_changed_outside) and
// shown, as a loosening line, ahead of the change.
func (t *Tools) governanceChange(ctx context.Context, p plan.Installation) (*plan.GovernanceChange, string) {
	current, next, ok, unreadable := plan.GovernanceFile(p)
	if !ok {
		return nil, ""
	}
	if unreadable {
		g := plan.GovernanceUnreadable()
		return &g, ""
	}
	g := plan.GovernanceBetween(current, next)
	if base, from, has := t.governanceBaseline(ctx, p.Name); has {
		if outside := plan.GovernanceBetween(base, current); len(outside.Lines) > 0 {
			t.d.Log.Warn("governance_on_record_changed_outside", identity.LogAttr(ctx), "audit", true, "installation", p.Name, "action", from, "governance", strings.Join(outside.Lines, "; "))
			g = plan.GovernanceBetween(base, next)
			line := governanceOutside(from)
			g.Lines = append([]string{line}, g.Lines...)
			g.Loosens = append([]string{line}, g.Loosens...)
		}
	}
	if len(g.Lines) == 0 {
		return nil, next.JSON()
	}
	return &g, next.JSON()
}

// governanceAfter is the snapshot a commit of p leaves: the agent-platform
// values it writes; empty where it writes none or they cannot be read.
func governanceAfter(p plan.Installation) string {
	_, next, ok, unreadable := plan.GovernanceFile(p)
	if !ok || unreadable {
		return ""
	}
	return next.JSON()
}

// governanceOfDisable is the governance a disable removes with the values:
// what the record carries (against the last reviewed action's where they
// differ), and the snapshot the disable records as what it leaves — the
// governance before it, so a later enable is compared with that.
func (t *Tools) governanceOfDisable(ctx context.Context, c *gh.Client, d plan.Disablement) (*plan.GovernanceChange, string, error) {
	for _, f := range d.Files {
		if f.Change != plan.ChangeDelete || !strings.HasSuffix(f.Path, "/apps/agent-platform/configmap-values.yaml.patch") {
			continue
		}
		owner, repo, err := gh.SplitRepo(f.Repository)
		if err != nil {
			return nil, "", err
		}
		content, err := gh.ReadFile(ctx, c, owner, repo, f.Path)
		if err != nil {
			return nil, "", fmt.Errorf("the agent-platform values on record could not be read as you to see the governance they carry: %w", err)
		}
		current, err := plan.GovernanceSnapshotOf(content)
		if err != nil {
			g := plan.GovernanceUnreadable()
			return &g, "", nil
		}
		old, outside := current, ""
		if base, from, has := t.governanceBaseline(ctx, d.Name); has && len(plan.GovernanceBetween(base, current).Lines) > 0 {
			t.d.Log.Warn("governance_on_record_changed_outside", identity.LogAttr(ctx), "audit", true, "installation", d.Name, "action", from)
			old, outside = base, governanceOutside(from)
		}
		g, changes := plan.GovernanceDeleted(old)
		if outside != "" {
			g.Lines = append([]string{outside}, g.Lines...)
			g.Loosens = append([]string{outside}, g.Loosens...)
			changes = true
		}
		if !changes {
			return nil, old.JSON(), nil
		}
		return &g, old.JSON(), nil
	}
	return nil, "", nil
}

// governanceRecordMoved says whether the agent-platform values on the
// default branch carry another governance than p read (someone merged one
// between the plan and the branch): read again past the cache, as the
// caller with a client of its own (a call's client keeps the tree it read
// for the plan). An error counts as moved (fail closed).
func (t *Tools) governanceRecordMoved(ctx context.Context, p plan.Installation) (bool, string) {
	token, _ := identity.TokenFromContext(ctx)
	c, err := t.person(token)
	if err != nil {
		return true, err.Error()
	}
	for _, f := range p.Files {
		if !strings.HasSuffix(f.Path, "/apps/agent-platform/configmap-values.yaml.patch") || (f.Change != plan.ChangeCreate && f.Change != plan.ChangeUpdate) {
			continue
		}
		read, err := plan.GovernanceSnapshotOf(f.Current)
		if err != nil {
			return true, "the values the plan read do not parse"
		}
		owner, repo, err := gh.SplitRepo(f.Repository)
		if err != nil {
			return true, err.Error()
		}
		t.d.Files.Invalidate(f.Repository)
		now, err := gh.ReadFile(ctx, c, owner, repo, f.Path)
		switch {
		case errors.Is(err, gh.ErrNotFound) && f.Change == plan.ChangeCreate:
			return false, ""
		case err != nil:
			return true, "the values on the default branch could not be read again: " + err.Error()
		}
		snap, err := plan.GovernanceSnapshotOf(now)
		if err != nil {
			return true, "the values on the default branch do not parse"
		}
		if moved := plan.GovernanceBetween(read, snap); len(moved.Lines) > 0 {
			return true, strings.Join(moved.Lines, "; ")
		}
	}
	return false, ""
}

// governanceReviewer refuses an approver of a governance change who is not
// an active member of the governance reviewers' team (approvals.governanceTeam,
// org/team), or not a person (a GitHub account of type User), read as the
// approver: fail closed when the team is unset or the membership cannot be
// read. It answers how the membership was established, for the audit line.
func (t *Tools) governanceReviewer(ctx context.Context, id *identity.Identity) (string, error) {
	team := ""
	if t.approvals != nil {
		team = t.approvals.Config().GovernanceTeam
	}
	org, slug, ok := strings.Cut(team, "/")
	if !ok || org == "" || slug == "" {
		return "", fmt.Errorf("%s: the action changes the platform's governance, and no governance reviewers' team is configured (chart approvals.governanceTeam, org/team): nobody can approve it", ToolApproveAction)
	}
	token, _ := identity.TokenFromContext(ctx)
	c, err := t.person(token)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ToolApproveAction, err)
	}
	m, _, err := c.Teams.GetTeamMembershipBySlug(ctx, org, slug, id.Login)
	if err != nil {
		return "", fmt.Errorf("%s: the action changes the platform's governance, which a member of %s approves, and your membership could not be read as you (%v): nothing is approved", ToolApproveAction, team, err)
	}
	if m.GetState() != "active" {
		return "", fmt.Errorf("%s: the action changes the platform's governance, which an active member of %s approves, and %s is %q there", ToolApproveAction, team, id.Login, m.GetState())
	}
	u, _, err := c.Users.Get(ctx, id.Login)
	if err != nil {
		return "", fmt.Errorf("%s: %s could not be read as you (%v): nothing is approved", ToolApproveAction, id.Login, err)
	}
	if u.GetType() != "User" {
		return "", fmt.Errorf("%s: the action changes the platform's governance, which a person approves, and %s is a GitHub %s", ToolApproveAction, id.Login, u.GetType())
	}
	return "active member of " + team + ", read as the approver", nil
}

// governanceReviewMax bounds the review's governance block: Slack shows a
// section of 3000 characters, and the header, the reason and what changes
// follow. governanceLoosensMax bounds its first part, what loosens, which is
// never cut: a commit whose loosening lines do not fit is refused.
const (
	governanceReviewMax  = 1400
	governanceLoosensMax = 1000
)

// governanceLoosens is the review's loosening part of a: per installation
// the lines that loosen, in full.
func governanceLoosens(a *actions.Action) string {
	var loosens []string
	for _, n := range governedInstallations(a) {
		if g := a.Spec.Governance[n]; len(g.Loosens) > 0 {
			loosens = append(loosens, n+": "+strings.Join(g.Loosens, "; "))
		}
	}
	if len(loosens) == 0 {
		return ""
	}
	return "\n*It loosens governance:* " + strings.Join(loosens, " — ")
}

// governanceTooLarge refuses a governance change whose loosening lines do
// not fit the review: they are never cut, so the change is split.
func governanceTooLarge(tool string, a *actions.Action) error {
	if n := utf8.RuneCountInString(governanceLoosens(a)); n > governanceLoosensMax {
		return fmt.Errorf("%s: what the change loosens in the platform's governance reads %d characters, more than the review shows in full (%d): split it into smaller changes; nothing is committed", tool, n, governanceLoosensMax)
	}
	return nil
}

// governanceText is the review's governance block, first after the header:
// that the action changes the governance and a second person approves it,
// what loosens it (never cut), then each installation's change old → new,
// cut at governanceReviewMax with the configs pull request named for the
// rest. Empty for an action that changes none.
func governanceText(a *actions.Action) string {
	if len(a.Spec.Governance) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n*Governance:* this changes the platform's governance; a second person approves it, on test installations too.")
	b.WriteString(governanceLoosens(a))
	cut := 0
	for _, n := range governedInstallations(a) {
		line := "\n• " + n + ": " + strings.Join(a.Spec.Governance[n].Lines, "; ")
		if utf8.RuneCountInString(b.String())+utf8.RuneCountInString(line) > governanceReviewMax {
			cut++
			continue
		}
		b.WriteString(line)
	}
	if cut > 0 {
		fmt.Fprintf(&b, "\n• … the change on %d more installation(s) is in full in the configs pull request%s", cut, configsLinks(a))
	}
	return b.String()
}

// configsLinks names the action's configs pull requests, which carry the
// agent-platform values in full.
func configsLinks(a *actions.Action) string {
	var links []string
	for _, pr := range a.Status.PullRequests {
		if strings.HasSuffix(pr.Repository, "-configs") {
			links = append(links, fmt.Sprintf("<%s|%s#%d>", pr.URL, pr.Repository, pr.Number))
		}
	}
	if len(links) == 0 {
		return ""
	}
	return ": " + strings.Join(links, ", ")
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
// did, the change and whether it loosens, and extra attributes. Nothing for
// an action that changes none.
func (t *Tools) auditGovernance(ctx context.Context, event string, a *actions.Action, extra ...any) {
	decidedBy, decision := "", ""
	if a.Status.Approval != nil {
		decidedBy, decision = a.Status.Approval.DecidedBy, a.Status.Approval.Decision
	}
	for _, n := range governedInstallations(a) {
		g := a.Spec.Governance[n]
		args := []any{identity.LogAttr(ctx), "audit", true, "action", a.Name, "installation", n, "actor", a.Spec.Actor.Login,
			"decision", decision, "decidedBy", decidedBy, "governance", strings.Join(g.Lines, "; "), "loosens", len(g.Loosens) > 0}
		t.d.Log.Info(event, append(args, extra...)...)
	}
}

// auditGovernanceStage logs event for one installation of a, with the pull
// requests of its stage and their heads: a merge is audited per stage.
func (t *Tools) auditGovernanceStage(ctx context.Context, event string, a *actions.Action, installation string, prs []actions.PullRequest) {
	g, ok := a.Spec.Governance[installation]
	if !ok {
		return
	}
	refs := make([]string, 0, len(prs))
	for _, pr := range prs {
		refs = append(refs, fmt.Sprintf("%s#%d@%s", pr.Repository, pr.Number, pr.HeadSHA))
	}
	decidedBy, decision := "", ""
	if a.Status.Approval != nil {
		decidedBy, decision = a.Status.Approval.DecidedBy, a.Status.Approval.Decision
	}
	t.d.Log.Info(event, identity.LogAttr(ctx), "audit", true, "action", a.Name, "installation", installation, "actor", a.Spec.Actor.Login,
		"decision", decision, "decidedBy", decidedBy, "pullRequests", strings.Join(refs, ","), "governance", strings.Join(g.Lines, "; "), "loosens", len(g.Loosens) > 0)
}

// governanceStored refuses an Action the hub stored without the governance
// it was created with: a CRD older than spec.governance prunes the field,
// and the review would then show no change and nothing would be audited.
func governanceStored(tool string, want map[string]plan.GovernanceChange, a *actions.Action) error {
	if len(want) > 0 && len(a.Spec.Governance) != len(want) {
		return fmt.Errorf("%s: action %s was stored without the governance it changes (the hub's Action CRD lacks spec.governance; upgrade the manager's chart): nothing is committed", tool, a.Name)
	}
	return nil
}
