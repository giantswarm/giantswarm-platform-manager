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
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// Governing tool calls (giantswarm/giantswarm#38902): one person never
// changes an installation's governance alone. A commit whose plan changes
// the governance (governanceChange) records the change on the Action
// (spec.governance) and asks the team's review even where every target is a
// test installation; approve_action refuses the actor and anyone who is not
// an active member of the governance reviewers' team (approvals.governanceTeam),
// and merge_action waits for that decision. What the team reviewed is kept
// per installation in a reviewed-governance record (actions.GovernanceRecord,
// decision #99), written only when merge_action merges an approved
// governance change: every plan compares the configs repository's
// governance with it, never with the Actions' order or states, so an edit
// outside the manager, a revert by hand or an action merged out of order is
// an unreviewed change the next commit asks the team to confirm or restore.
// Each step is audited (auditGovernance). Agents may propose such a change
// and never approve or merge it: muster asks a person before an agent's
// approve_action and merge_action (a floor of the platformManager role).

// reviewedGovernance is installation's reviewed-governance record: the
// governance the team last reviewed there, and the record. found is false
// without one, or when it cannot be read or parsed (logged): fail closed,
// the governance in play then asks the review.
func (t *Tools) reviewedGovernance(ctx context.Context, installation string) (plan.GovernanceSnapshot, *actions.GovernanceRecord, bool) {
	if t.d.Records == nil {
		return nil, nil, false
	}
	r, err := t.d.Records.GetRecord(ctx, installation)
	if err != nil {
		if !errors.Is(err, actions.ErrNotFound) {
			t.d.Log.Warn("governance_record_unread", identity.LogAttr(ctx), "audit", true, "installation", installation, "error", err.Error())
		}
		return nil, nil, false
	}
	snap, err := plan.ParseGovernanceSnapshot(r.Governance)
	if err != nil {
		t.d.Log.Warn("governance_record_unread", identity.LogAttr(ctx), "audit", true, "installation", installation, "error", err.Error())
		return nil, nil, false
	}
	return snap, r, true
}

// governanceOutside is the line of a plan whose configs repository carries
// another governance than the team reviewed: the change is shown against
// the reviewed one.
func governanceOutside(r *actions.GovernanceRecord) string {
	return "the governance on record differs from what the team reviewed in action " + r.Action + " (changed outside the manager, reverted by hand or merged out of order); the change is shown against what was reviewed"
}

// governanceUnrecorded is the line of a plan whose governance is in play
// on an installation without a reviewed-governance record: the team review
// confirms it (fail closed; installations from before the record get one
// with their first reviewed commit).
func governanceUnrecorded(installation string) string {
	return "no reviewed governance is on record for " + installation + ": the team review confirms the governance this commit leaves"
}

// against compares the configs repository's governance (current) and what
// a commit leaves (next) with the reviewed record: a difference between the
// record and current is logged as an unreviewed change and leads the
// change, which is shown against the record; without a record, governance
// in play leads with governanceUnrecorded. The lines from base to next
// follow, base the record or current.
func (t *Tools) against(ctx context.Context, installation string, current, next plan.GovernanceSnapshot) (plan.GovernanceChange, plan.GovernanceSnapshot) {
	lead := ""
	base := current
	if reviewed, r, found := t.reviewedGovernance(ctx, installation); found {
		if outside := plan.GovernanceBetween(reviewed, current); len(outside.Lines) > 0 {
			t.d.Log.Warn("governance_on_record_changed_outside", identity.LogAttr(ctx), "audit", true, "installation", installation, "action", r.Action, "governance", strings.Join(outside.Lines, "; "))
			lead, base = governanceOutside(r), reviewed
		}
	} else if current.InPlay() || next.InPlay() {
		t.d.Log.Warn("governance_unrecorded", identity.LogAttr(ctx), "audit", true, "installation", installation)
		lead = governanceUnrecorded(installation)
	}
	g := plan.GovernanceBetween(base, next)
	if lead != "" {
		g.Lines = append([]string{lead}, g.Lines...)
		g.Loosens = append([]string{lead}, g.Loosens...)
	}
	return g, base
}

// governanceChange is the governance a commit of p changes, against the
// reviewed record (against), and the snapshot the commit leaves; nil for a
// plan that changes none or writes no agent-platform values.
func (t *Tools) governanceChange(ctx context.Context, p plan.Installation) (*plan.GovernanceChange, string) {
	current, next, ok, unreadable := plan.GovernanceFile(p)
	if !ok {
		return nil, ""
	}
	if unreadable {
		g := plan.GovernanceUnreadable()
		return &g, ""
	}
	g, _ := t.against(ctx, p.Name, current, next)
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

// governanceOfDisable is the governance a disable removes with the values
// (against the reviewed record), and the snapshot the disable records as
// what it leaves: the governance before it (the record where the values
// differ from it), so a later enable is compared with that once the
// reviewed disable is merged.
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
		lead, base := t.against(ctx, d.Name, current, current)
		g, changes := plan.GovernanceDeleted(base)
		if len(lead.Lines) > 0 {
			g.Lines = append(slices.Clone(lead.Lines), g.Lines...)
			g.Loosens = append(slices.Clone(lead.Loosens), g.Loosens...)
			changes = true
		}
		if !changes {
			return nil, base.JSON(), nil
		}
		return &g, base.JSON(), nil
	}
	return nil, "", nil
}

// recordReviewed writes installation's reviewed-governance record once
// merge_action merged the stage of an approved governance change there: the
// governance the action's commit leaves, the approver and the configs pull
// request's merged head. Never for an action the team did not approve, or
// one that changes no governance there. A write that fails is logged and
// answered: the next plan then asks the review again (fail closed).
func (t *Tools) recordReviewed(ctx context.Context, a *actions.Action, installation string) string {
	if t.d.Records == nil || a.Status.Approval == nil || a.Status.Approval.Decision != actions.DecisionApproved {
		return ""
	}
	if _, changes := actions.GovernanceOf(a)[installation]; !changes {
		return ""
	}
	after, ok := a.Spec.GovernanceAfter[installation]
	if !ok {
		return ""
	}
	head := ""
	for _, k := range a.StagePullRequests(installation) {
		if pr := a.Status.PullRequests[k]; strings.HasSuffix(pr.Repository, "-configs") {
			head = pr.HeadSHA
		}
	}
	r := actions.GovernanceRecord{Installation: installation, Governance: after, Action: a.Name, ApprovedBy: a.Status.Approval.DecidedBy, HeadSHA: head}
	if err := t.d.Records.PutRecord(ctx, r); err != nil {
		t.d.Log.Warn("governance_record_not_written", identity.LogAttr(ctx), "audit", true, "action", a.Name, "installation", installation, "error", err.Error())
		return fmt.Sprintf("(The reviewed governance of %s could not be recorded: %v; the next commit there asks the team's review again.)", installation, err)
	}
	t.d.Log.Info("governance_record_written", identity.LogAttr(ctx), "audit", true, "action", a.Name, "installation", installation, "approvedBy", r.ApprovedBy, "headSHA", head)
	return ""
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

// governanceMoved leads the change found when the values on the default
// branch moved between the plan and the pull requests.
const governanceMoved = "the governance on the default branch changed after the plan read it, and the pull requests write over it: "

// recordMoved logs governance_record_moved and records what moved as a
// loosening governance change of a on installation (status.governance).
func (t *Tools) recordMoved(ctx context.Context, a *actions.Action, installation, what string) (*actions.Action, error) {
	t.d.Log.Warn("governance_record_moved", identity.LogAttr(ctx), "audit", true, "action", a.Name, "installation", installation, "governance", what)
	status := a.Status
	status.Governance = maps.Clone(status.Governance)
	if status.Governance == nil {
		status.Governance = map[string]plan.GovernanceChange{}
	}
	line := governanceMoved + what
	status.Governance[installation] = plan.GovernanceChange{Lines: []string{line}, Loosens: []string{line}}
	out, err := t.d.Actions.UpdateStatus(ctx, a, status)
	if err != nil {
		return a, fmt.Errorf("the governance that moved under the plan could not be recorded on action %s: %w", a.Name, err)
	}
	return out, nil
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
		if g := actions.GovernanceOf(a)[n]; len(g.Loosens) > 0 {
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
	if len(actions.GovernanceOf(a)) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n*Governance:* this changes the platform's governance; a second person approves it, on test installations too.")
	b.WriteString(governanceLoosens(a))
	cut := 0
	for _, n := range governedInstallations(a) {
		line := "\n• " + n + ": " + strings.Join(actions.GovernanceOf(a)[n].Lines, "; ")
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
		if _, ok := actions.GovernanceOf(a)[n]; ok {
			out = append(out, n)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(actions.GovernanceOf(a))) {
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
		g := actions.GovernanceOf(a)[n]
		args := []any{identity.LogAttr(ctx), "audit", true, "action", a.Name, "installation", n, "actor", a.Spec.Actor.Login,
			"decision", decision, "decidedBy", decidedBy, "governance", strings.Join(g.Lines, "; "), "loosens", len(g.Loosens) > 0}
		t.d.Log.Info(event, append(args, extra...)...)
	}
}

// auditGovernanceStage logs event for one installation of a, with the pull
// requests of its stage and their heads: a merge is audited per stage.
func (t *Tools) auditGovernanceStage(ctx context.Context, event string, a *actions.Action, installation string, prs []actions.PullRequest) {
	g, ok := actions.GovernanceOf(a)[installation]
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
