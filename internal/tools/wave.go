package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// ArgOrder is the wave's own argument: the targets in the order to roll out,
// when the default (the test installations, the hub, the customers) is not
// the one wanted. It names every target exactly once.
const ArgOrder = "order"

// WaveResult is the answer of reconcile_capability in mode commit over a set:
// one Action for the whole wave, its pull requests per installation in the
// rollout order, and the installations left out with the reason.
type WaveResult struct {
	Caller     string          `json:"caller"`
	Tool       string          `json:"tool"`
	Capability string          `json:"capability"`
	Hub        string          `json:"hub"`
	DryRun     bool            `json:"dryRun"`
	Action     *actions.Action `json:"action,omitempty"`
	// Order is the rollout order: the targets, each with pull requests.
	Order []string `json:"order"`
	// Skipped are the installations of the set that are not targets.
	Skipped []actions.Skipped `json:"skipped"`
	// Unchanged are the opted-in installations whose files are on record as
	// the definition renders them: nothing to merge, not a stage.
	Unchanged    []string              `json:"unchanged"`
	PullRequests []actions.PullRequest `json:"pullRequests"`
	Next         string                `json:"next"`
}

// The message the wave's queued stages carry.
const stageQueued = "queued: its pull requests are merged once the installation before it is verified"

// capabilityWave is reconcile_capability in mode commit over a set: one dry
// run, one Action in pending approval carrying every installation's state,
// one review listing the targets and the skipped, the pull requests per
// installation on their own branches. Nothing is written before every target
// passed its checks; the rollout is merge_action's, one stage per call.
func (t *Tools) capabilityWave(ctx context.Context, tool string, args map[string]any) (any, error) {
	id, _ := identity.FromContext(ctx)
	token, _ := identity.TokenFromContext(ctx)
	if args[ArgSecrets] != nil {
		return nil, fmt.Errorf("%s: a wave carries no supplied secret values (%s): a reconcile over a set leaves every secret file on record alone; an installation whose secret files are not on record yet is enabled alone, with %s and %s", tool, ArgSecrets, ArgInstallation, ArgSecrets)
	}
	args[ArgContent] = true
	out, env, err := t.capabilityPlan(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	typed, _ := args[ArgInputs].(map[string]any)
	res := WaveResult{Caller: identity.Caller(ctx), Tool: tool, Capability: out.Capability, Hub: out.Hub, Order: []string{}, Skipped: []actions.Skipped{}, Unchanged: []string{}, PullRequests: []actions.PullRequest{}}
	for _, s := range out.Skipped {
		res.Skipped = append(res.Skipped, actions.Skipped{Name: s.Name, Reason: s.Reason})
	}
	// Every target is checked before anything is written: a wave that is
	// refused for one installation is refused whole, with nothing recorded.
	var targets []plan.Installation
	for _, p := range out.Installations {
		switch {
		case p.Refused != "":
			return nil, fmt.Errorf("%s: the definition refuses the inputs on record for %s: %s — narrow the set (%s) or fix the record; nothing is committed", tool, p.Name, p.Refused, ArgInstallations)
		case p.Diff[plan.ChangeUnknown] > 0:
			return nil, fmt.Errorf("%s: %d file(s) of %s could not be compared against the repository as you (%s); nothing is committed blind", tool, p.Diff[plan.ChangeUnknown], p.Name, unknownFiles(p))
		case len(p.Files)-p.Diff[plan.ChangeUnchanged] == 0:
			res.Unchanged = append(res.Unchanged, p.Name)
			continue
		}
		if files := suppliedFilesToCreate(p); len(files) > 0 {
			return nil, fmt.Errorf("%s: %s needs the supplied value(s) of %s — %s not on record; enable %s alone with %s and %s, or narrow the set; nothing is committed", tool, p.Name, strings.Join(p.SuppliedSecrets, ", "), strings.Join(files, ", "), p.Name, ArgInstallation, ArgSecrets)
		}
		targets = append(targets, p)
		res.Order = append(res.Order, p.Name)
	}
	if len(targets) == 0 {
		res.Next = "no installation of the set has a change to commit: nothing to merge, no action recorded"
		return res, nil
	}

	spec := actions.Spec{Actor: actions.Actor{Login: id.Login, ID: id.ID, Email: id.Email}, Capability: out.Capability, Installations: res.Order, Inputs: typed, Kind: actions.KindReconcile, Skipped: res.Skipped,
		InputsByInstallation: map[string]map[string]any{}}
	for _, p := range targets {
		spec.InputsByInstallation[p.Name] = p.Inputs
	}
	changes := make([]string, 0, len(targets))
	rollout := &actions.Rollout{Installations: make([]actions.InstallationRollout, 0, len(targets))}
	for i, p := range targets {
		if env.byName[p.Name].Customer != env.hub.Customer {
			spec.Customer = true
		}
		changes = append(changes, p.Name+": "+changeSummary(p))
		rollout.Installations = append(rollout.Installations, actions.InstallationRollout{Name: p.Name, State: actions.StatePendingApproval, Message: fmt.Sprintf("stage %d of %d", i+1, len(targets))})
	}
	spec.Change = strings.Join(changes, "; ")
	def, ok := installations.FindCapability(out.Capability)
	if !ok {
		return nil, fmt.Errorf("%s: %q is not a capability definition", tool, out.Capability)
	}
	name, err := actions.NewName(actions.KindReconcile, "wave")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	a, err := t.d.Actions.Create(ctx, actions.Action{Name: name, Spec: spec, Status: actions.Status{State: actions.StatePendingApproval, Rollout: rollout}})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	remote, err := t.d.Remote(token)
	if err != nil {
		return nil, t.fail(ctx, tool, a, nil, err)
	}
	prs := []actions.PullRequest{}
	for i, p := range targets {
		// The supplied fields render as markers: their files are on record
		// (checked above) and never generated again, so no marker leaves.
		markers := make(map[string]string, len(p.SuppliedSecrets))
		for _, field := range p.SuppliedSecrets {
			markers[field] = render.Supplied(field)
		}
		rendered, err := def.Render(env.inputs[p.Name], markers)
		if err != nil {
			return nil, t.fail(ctx, tool, a, prs, fmt.Errorf("%s: render: %w", p.Name, err))
		}
		planned := plan.PullRequests([]plan.Installation{p}, env.byName, env.hub)
		title := fmt.Sprintf("%s %s on %s (%s, stage %d of %d)", actions.KindReconcile, out.Capability, p.Name, a.Name, i+1, len(targets))
		opened, _, err := t.openPullRequests(ctx, env, a, p, planned, rendered.Files, remote, title, prBody(a, p, planned)+waveBody(res.Order, res.Skipped))
		prs = append(prs, opened...)
		if err != nil {
			return nil, t.fail(ctx, tool, a, prs, err)
		}
	}
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, actions.Status{State: actions.StatePendingApproval, PullRequests: prs, Rollout: rollout})
	if err != nil {
		return nil, fmt.Errorf("%s: the pull requests are open (%s) and the action could not record them: %w", tool, prList(prs), err)
	}
	t.d.Log.Info(tool, identity.LogAttr(ctx), "action", a.Name, "wave", strings.Join(res.Order, ","), "skipped", len(res.Skipped), "pullRequests", len(prs))
	a, err = t.askApproval(ctx, a, tool)
	if err != nil {
		return nil, fmt.Errorf("%w — the pull requests are open (%s) and the action pends approval; %s posts the review", err, prList(prs), ToolMergeAction)
	}
	res.Action = a
	res.PullRequests = prs
	res.Next = fmt.Sprintf("the wave waits for the team's approval (review %s in %s) over %s in this order%s; once approved and green you merge stage 1 (%s) as the actor with %s, and call it again once Flux has reconciled it — the installation's verify runs first and, green, the next stage is merged; a red probe stops the wave with the remaining pull requests open",
		a.Status.Approval.ReviewID, a.Status.Approval.Channel, strings.Join(res.Order, ", "), skippedClause(res.Skipped), res.Order[0], ToolMergeAction)
	return res, nil
}

// suppliedFilesToCreate names the files of p that carry a supplied value
// and are not on record: a wave cannot fill them. The placeholder names the
// file; whether it is encrypted is the commit step's decision from the
// repository's rules.
func suppliedFilesToCreate(p plan.Installation) []string {
	marker := strings.TrimSuffix(render.Supplied(""), ")")
	var files []string
	for _, f := range p.Files {
		if f.Change == plan.ChangeCreate && strings.Contains(f.Content, marker) {
			files = append(files, f.Repository+":"+f.Path)
		}
	}
	sort.Strings(files)
	return files
}

func waveBody(order []string, skipped []actions.Skipped) string {
	return fmt.Sprintf("\nThe wave rolls out %s in this order, one installation verified before the next%s.\n", strings.Join(order, ", "), skippedClause(skipped))
}

func skippedClause(skipped []actions.Skipped) string {
	if len(skipped) == 0 {
		return ""
	}
	names := make([]string, 0, len(skipped))
	for _, s := range skipped {
		names = append(names, s.Name+" ("+s.Reason+")")
	}
	return "; skipped: " + strings.Join(names, ", ")
}

// applyOrder reorders the rendered installations to order, which names every
// one of them exactly once (skipped names may appear and are ignored).
func applyOrder(out *CapabilityResult, order []string) error {
	if len(order) == 0 {
		return nil
	}
	byName := map[string]plan.Installation{}
	for _, p := range out.Installations {
		byName[p.Name] = p
	}
	seen := map[string]bool{}
	var names []string
	var plans []plan.Installation
	for _, n := range order {
		if seen[n] {
			return fmt.Errorf("%s names %s twice", ArgOrder, n)
		}
		seen[n] = true
		if p, ok := byName[n]; ok {
			names = append(names, n)
			plans = append(plans, p)
		}
	}
	for _, p := range out.Installations {
		if !seen[p.Name] {
			return fmt.Errorf("%s leaves %s out: it names every installation of the set that is rendered, in the order to roll out", ArgOrder, p.Name)
		}
	}
	out.Order, out.Installations = names, plans
	return nil
}
