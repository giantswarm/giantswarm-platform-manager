// Package format writes the manager's answers for a terminal. It decides
// nothing: every value it prints is a field of the tool's answer, and
// `--output json` prints that answer as it is instead.
package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// JSON writes the tool's document re-indented, as the manager answered it.
func JSON(w io.Writer, raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("the answer is not a JSON document: %w", err)
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

// AuthRequired prints muster's sign-in for the manager and the CLI way to it.
func AuthRequired(w io.Writer, e *muster.AuthRequired) error {
	p := &printer{w: w}
	p.f("Sign in required: %s is not connected for you in muster.\nOpen this URL to sign in, then run the command again:\n\n  %s\n\nOr: muster auth login --server %s\n", e.Server, e.URL, e.Server)
	return p.err
}

// Installations is list_installations as a table: one row per installation,
// one column per capability with its state.
func Installations(w io.Writer, r tools.ListInstallationsResult) error {
	p := &printer{w: w}
	p.f("Caller: %s   Hub: %s   Registry: %s\n\n", dash(r.Caller), dash(r.Hub), r.Registry.Catalog.String())
	header := []string{"NAME", "CUSTOMER", "PROVIDER", "HUB"}
	for _, c := range r.Capabilities {
		header = append(header, strings.ToUpper(c))
	}
	header = append(header, "LAST ACTION")
	rows := [][]string{header}
	for _, inst := range r.Installations {
		row := []string{inst.Name, dash(inst.Customer), dash(inst.Provider), yesNo(inst.Hub)}
		for _, c := range r.Capabilities {
			row = append(row, string(capabilityState(inst, c)))
		}
		rows = append(rows, append(row, lastAction(inst)))
	}
	p.table(0, rows)
	if len(r.Unreadable) > 0 {
		p.f("\nUnreadable as you: %s\n", strings.Join(r.Unreadable, ", "))
	}
	var lines []string
	for _, inst := range r.Installations {
		for _, e := range inst.Errors {
			lines = append(lines, "  "+inst.Name+": "+e)
		}
	}
	if len(lines) > 0 {
		p.f("\nErrors:\n%s\n", strings.Join(lines, "\n"))
	}
	return p.err
}

func capabilityState(inst installations.Report, capability string) installations.State {
	for _, c := range inst.Capabilities {
		if c.Name == capability {
			return c.State
		}
	}
	return installations.StateUnknown
}

func lastAction(inst installations.Report) string {
	for _, c := range inst.Capabilities {
		if c.LastAction != nil {
			if c.LastAction.Result != "" {
				return c.LastAction.Name + " (" + c.LastAction.Result + ")"
			}
			return c.LastAction.Name
		}
	}
	return "-"
}

// Plan is a dry run of enable_capability or reconcile_capability: every
// installation with its files and what a commit would carry, the pull requests
// in order, the installations skipped, and what the commit step says.
func Plan(w io.Writer, r tools.CapabilityResult, content bool) error {
	p := &printer{w: w}
	p.f("%s dry run: %s on hub %s, as %s\n", r.Tool, r.Capability, dash(r.Hub), dash(r.Caller))
	if len(r.Order) > 0 {
		p.f("Order: %s\n", strings.Join(r.Order, ", "))
	}
	for _, inst := range r.Installations {
		p.installation(inst.Installation, content)
		if len(inst.Summary) > 0 {
			p.f("  Comparison: %s\n", marks(inst.Summary))
		}
	}
	if len(r.PullRequests) > 0 {
		p.f("\nPull requests, in order:\n")
		for _, pr := range r.PullRequests {
			p.f("  %d. %s: %s for %s\n", pr.Order, pr.Repository, plural(pr.Changes, "change"), strings.Join(pr.Installations, ", "))
			if after := pr.AfterClause(); after != "" {
				p.f("     %s\n", after)
			}
			if len(pr.GeneratedSecrets) > 0 {
				p.f("     generated secrets: %s\n", strings.Join(pr.GeneratedSecrets, ", "))
			}
		}
	}
	if len(r.Skipped) > 0 {
		p.f("\nSkipped:\n")
		for _, s := range r.Skipped {
			p.f("  %s: %s\n", s.Name, s.Reason)
			for _, e := range s.Errors {
				p.f("     %s\n", e)
			}
		}
	}
	if r.Commit != "" {
		p.f("\nCommit: %s\n", r.Commit)
	}
	return p.err
}

func (p *printer) installation(inst plan.Installation, content bool) {
	p.f("\n%s: %s\n", inst.Name, inst.State)
	if inst.Refused != "" {
		p.f("  Refused: %s\n", inst.Refused)
	}
	if len(inst.MissingInputs) > 0 {
		p.f("  Choices not on record: %s\n", strings.Join(inst.MissingInputs, ", "))
	}
	if inst.CommitRefused != "" && inst.CommitRefused != inst.Refused {
		p.f("  A commit would be refused: %s\n", inst.CommitRefused)
	}
	if len(inst.Files) > 0 {
		p.f("  Files (%s):\n", diff(inst.Diff))
		rows := [][]string{{"CHANGE", "REPOSITORY", "PATH"}}
		for _, f := range inst.Files {
			path := f.Path
			if f.Error != "" {
				path += "  (" + f.Error + ")"
			}
			rows = append(rows, []string{string(f.Change), f.Repository, path})
		}
		p.table(4, rows)
		for _, f := range inst.Files {
			if len(f.Unseen) > 0 {
				p.f("    %s holds encrypted, not compared: %s\n", f.Path, unseen(f.Unseen))
			}
			if len(f.Dropped) > 0 {
				p.f("    %s drops what the record holds encrypted and no input renders: %s\n", f.Path, strings.Join(f.Dropped, ", "))
			}
			if len(f.Replaced) > 0 {
				p.f("    %s replaces the encrypted values on record whole (%s): a key they hold that the definition does not render is lost — supply it at commit where the plan asks for it, or keep the file by hand\n", f.Path, strings.Join(f.Replaced, ", "))
			}
		}
	}
	for _, inc := range inst.Includes {
		p.f("  Includes: %s:%s %s %s (%s)\n", inc.Repository, inc.Path, inc.List, inc.Resource, inc.Change)
	}
	if len(inst.GeneratedSecrets) > 0 {
		p.f("  Generated at commit:\n")
		for _, g := range inst.GeneratedSecrets {
			p.f("    %s (%s, %d): %s\n", g.Name, g.Kind, g.Length, strings.Join(g.Files, ", "))
			switch {
			case g.Refusal != "":
				p.f("      refused: %s\n", g.Refusal)
			case g.Rotates && g.ForcedBy == plan.ForcedByRequest:
				p.f("      rotates on request: %s — a new value replaces the one on record in %s; both sides roll, the client is unusable between the two rollouts\n", g.Name, strings.Join(g.FrozenIn, ", "))
			case g.Rotates:
				p.f("      rotates: %s (forced by %s) — a new value replaces the one on record in %s; both sides roll, the client is unusable between the two rollouts\n", g.Name, g.ForcedBy, strings.Join(g.FrozenIn, ", "))
			case g.Kept:
				p.f("      kept: the value on record in %s stands, nothing is written\n", strings.Join(g.FrozenIn, ", "))
			}
		}
	}
	if len(inst.SuppliedSecrets) > 0 {
		p.f("  You supply at commit: %s\n", strings.Join(inst.SuppliedSecrets, ", "))
	}
	if len(inst.SuppliedOnRecord) > 0 {
		p.f("  Supplied values on record: %s — their files stand, nothing to supply\n", strings.Join(inst.SuppliedOnRecord, ", "))
	}
	if len(inst.DexClients) > 0 {
		p.f("  Dex clients:\n")
		for _, c := range inst.DexClients {
			p.f("    %s%s\n", c.ID, dexClient(c))
		}
	}
	if len(inst.CustomerActions) > 0 {
		p.f("  Customer actions:\n")
		for _, a := range inst.CustomerActions {
			p.f("    %s: %s (%s)\n", a.Installation, a.Action, a.Why)
		}
	}
	if len(inst.Probes) > 0 {
		p.f("  Probes:\n")
		for _, pr := range inst.Probes {
			p.f("    %s: %s %s\n", pr.ID, pr.Feature, pr.Key)
		}
	}
	if !content {
		return
	}
	for _, f := range inst.Files {
		if f.Content == "" {
			continue
		}
		p.f("\n--- %s/%s (%s)\n%s", f.Repository, f.Path, f.Change, f.Content)
		if !strings.HasSuffix(f.Content, "\n") {
			p.f("\n")
		}
	}
}

func dexClient(c plan.DexClient) string {
	var parts []string
	if c.Name != "" {
		parts = append(parts, c.Name)
	}
	if c.Client != "" {
		parts = append(parts, "client "+c.Client)
	}
	if c.Public {
		parts = append(parts, "public")
	}
	if c.SecretRef != "" {
		parts = append(parts, "secretRef "+c.SecretRef)
	}
	if len(c.RedirectURIs) > 0 {
		parts = append(parts, "redirect URIs "+strings.Join(c.RedirectURIs, " "))
	}
	if len(c.TrustedPeers) > 0 {
		parts = append(parts, "trusted peers "+strings.Join(c.TrustedPeers, " "))
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, "; ")
}

func diff(d map[plan.Change]int) string {
	var parts []string
	for _, c := range []plan.Change{plan.ChangeCreate, plan.ChangeUpdate, plan.ChangeUnchanged, plan.ChangeUnknown} {
		if n := d[c]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, c))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// Action is one Action record in full.
func Action(w io.Writer, a actions.Action) error {
	p := &printer{w: w}
	p.f("Action %s in %s, created %s\n", a.Name, dash(a.Namespace), a.CreatedAt.Format(time.RFC3339))
	p.f("Kind: %s   Capability: %s   Actor: %s\n", a.Spec.Kind, a.Spec.Capability, dash(a.Spec.Actor.Login))
	p.f("Installations: %s\n", strings.Join(a.Spec.Installations, ", "))
	p.f("State: %s\n", dash(a.Status.State))
	if len(a.Status.PullRequests) > 0 {
		p.f("Pull requests:\n")
		for _, pr := range a.Status.PullRequests {
			p.f("  %s#%d %s %s%s\n", pr.Repository, pr.Number, dash(pr.State), pr.URL, mergeOf(pr))
		}
	}
	if ap := a.Status.Approval; ap != nil {
		p.f("Approval: %s by %s%s in %s%s\n", dash(ap.Decision), dash(ap.DecidedBy), at(ap.At), dash(ap.Channel), reason(ap.Reason))
	}
	if ro := a.Status.Rollout; ro != nil {
		p.f("Rollout: started%s, finished%s\n", at(ro.StartedAt), at(ro.FinishedAt))
		for _, i := range ro.Installations {
			p.f("  %s: %s%s\n", i.Name, dash(i.State), reason(i.Message))
		}
	}
	if len(a.Status.Probes) > 0 {
		p.f("Probes:\n")
		for _, pr := range a.Status.Probes {
			where := ""
			if pr.Installation != "" {
				where = " on " + pr.Installation
			}
			p.f("  %s%s: %s%s%s\n", pr.ID, where, dash(pr.Result), reason(pr.Message), at(pr.At))
		}
	}
	if res := a.Status.Result; res != nil {
		p.f("Result: %s%s%s\n", dash(res.State), reason(res.Message), at(res.At))
	}
	if wd := a.Status.Withdrawal; wd != nil {
		p.f("Withdrawn by %s%s%s\n", dash(wd.By), at(wd.At), reason(wd.Reason))
	}
	if a.Status.SyncedAt != nil {
		p.f("Synced with GitHub%s as %s\n", at(a.Status.SyncedAt), dash(a.Status.SyncedBy))
	}
	if len(a.Status.Orphans) > 0 {
		p.f("Orphans (the Kustomization over the tree does not prune; delete by hand):\n")
		for _, o := range a.Status.Orphans {
			name := o.Name
			if o.Namespace != "" {
				name = o.Namespace + "/" + name
			}
			p.f("  %s: %s %s\n", o.Installation, o.Kind, name)
		}
	}
	if len(a.Spec.Inputs) > 0 {
		if b, err := json.MarshalIndent(a.Spec.Inputs, "  ", "  "); err == nil {
			p.f("Inputs:\n  %s\n", b)
		}
	}
	return p.err
}

// mergeOf is a pull request's merge or close as recorded: who merged it and
// when, with the merge commit and the revert that took it back, or when it
// was closed unmerged.
func mergeOf(pr actions.PullRequest) string {
	switch pr.State {
	case actions.PullRequestMerged:
		s := " merged"
		if pr.MergedBy != "" {
			s += " by " + pr.MergedBy
		}
		s += at(pr.MergedAt)
		if pr.MergeCommit != "" {
			s += " (" + pr.MergeCommit + ")"
		}
		if rv := pr.Revert; rv != nil {
			s += ", reverted by " + rv.Commit
			if rv.PullRequest > 0 {
				s += fmt.Sprintf(" (%s#%d %s)", pr.Repository, rv.PullRequest, rv.PullRequestURL)
			}
		}
		return s
	case actions.PullRequestClosed:
		if pr.ClosedAt != nil {
			return " closed" + at(pr.ClosedAt)
		}
	}
	return ""
}

// Actions is list_actions as a table, newest first as the tool answers.
func Actions(w io.Writer, r tools.ListActionsResult) error {
	p := &printer{w: w}
	if len(r.Actions) == 0 {
		p.f("No actions in %s%s.\n", dash(r.Namespace), filter(r.Filter))
		return p.err
	}
	rows := [][]string{{"NAME", "KIND", "CAPABILITY", "ACTOR", "STATE", "INSTALLATIONS", "AGE"}}
	for _, a := range r.Actions {
		rows = append(rows, []string{a.Name, a.Spec.Kind, a.Spec.Capability, dash(a.Spec.Actor.Login), dash(a.Status.State), strings.Join(a.Spec.Installations, ","), age(a.CreatedAt)})
	}
	p.table(0, rows)
	return p.err
}

func filter(f actions.Filter) string {
	var parts []string
	if f.Installation != "" {
		parts = append(parts, "installation "+f.Installation)
	}
	if f.Capability != "" {
		parts = append(parts, "capability "+f.Capability)
	}
	if len(parts) == 0 {
		return ""
	}
	return " for " + strings.Join(parts, ", ")
}

// Commit is mode commit of enable_capability or reconcile_capability: the
// Action started, its pull requests in order, the plan they carry (markers,
// never values) and what follows.
func Commit(w io.Writer, r tools.CommitResult, content bool) error {
	p := &printer{w: w}
	p.f("%s commit: %s on %s (hub %s), as %s\n", r.Tool, r.Capability, r.Installation, dash(r.Hub), dash(r.Caller))
	if a := r.Action; a != nil {
		p.f("Action: %s (%s)\n", a.Name, dash(a.Status.State))
	}
	if len(r.PullRequests) > 0 {
		p.f("Pull requests, in order:\n")
		for i, pr := range r.PullRequests {
			p.f("  %d. %s#%d %s\n", i+1, pr.Repository, pr.Number, pr.URL)
		}
	}
	if len(r.UnchangedRepositories) > 0 {
		p.f("Nothing to commit in: %s\n", strings.Join(r.UnchangedRepositories, ", "))
	}
	p.installation(r.Plan, content)
	if r.Next != "" {
		p.f("\nNext: %s\n", r.Next)
	}
	return p.err
}

// Wave is mode commit of reconcile_capability over a set: the one Action, the
// rollout order, the installations skipped and unchanged, the pull requests
// per stage and what follows.
func Wave(w io.Writer, r tools.WaveResult) error {
	p := &printer{w: w}
	p.f("%s commit: %s wave on hub %s, as %s\n", r.Tool, r.Capability, dash(r.Hub), dash(r.Caller))
	if a := r.Action; a != nil {
		p.f("Action: %s (%s)\n", a.Name, dash(a.Status.State))
	}
	if len(r.Order) > 0 {
		p.f("Order: %s\n", strings.Join(r.Order, ", "))
	}
	if len(r.Skipped) > 0 {
		p.f("Skipped:\n")
		for _, s := range r.Skipped {
			p.f("  %s: %s\n", s.Name, s.Reason)
		}
	}
	if len(r.Unchanged) > 0 {
		p.f("Unchanged: %s\n", strings.Join(r.Unchanged, ", "))
	}
	if len(r.PullRequests) > 0 {
		p.f("Pull requests, in order:\n")
		for i, pr := range r.PullRequests {
			p.f("  %d. %s: %s#%d %s\n", i+1, dash(pr.Installation), pr.Repository, pr.Number, pr.URL)
		}
	}
	if r.Next != "" {
		p.f("\nNext: %s\n", r.Next)
	}
	return p.err
}

// Verify is the verify of one installation — verify_capability's repository
// comparison merged with verify_installation's live one: the state, the
// inputs on record, every feature of the definition with its mark and, under
// it, its dimensions with theirs — a difference names the file or the live
// object, the path and the input that drives it or drift; a probe its
// requests, a live dimension its checks. liveErr says why there is no live
// side when verify_installation did not answer.
func Verify(w io.Writer, r verify.Result, liveErr error) error {
	p := &printer{w: w}
	p.f("verify %s on %s (hub %s), as %s\n", r.Capability, r.Installation, dash(r.Hub), dash(r.Caller))
	if liveErr != nil {
		p.f("Live: not checked — %s\n", liveErr.Error())
	} else if r.LiveCaller != "" {
		p.f("Live: read as %s\n", r.LiveCaller)
	}
	p.f("State: %s   Inputs: %s\n", dash(string(r.State)), dash(r.Inputs.Source))
	p.f("Summary: %s\n", marks(r.Summary))
	if r.Refused != "" {
		p.f("Refused: %s\n", r.Refused)
	}
	if len(r.Inputs.Unset) > 0 {
		p.f("Choices not on record: %s\n", strings.Join(r.Inputs.Unset, ", "))
	}
	if r.CommitRefused != "" && r.CommitRefused != r.Refused {
		p.f("A commit would be refused: %s\n", r.CommitRefused)
	}
	if len(r.Files) > 0 {
		p.f("Files: %s\n", diff(r.Diff))
	}
	for _, pr := range r.PullRequests {
		p.f("Pull request %d: %s, %d change(s)", pr.Order, pr.Repository, pr.Changes)
		if after := pr.AfterClause(); after != "" {
			p.f(", %s", after)
		}
		p.f("\n")
	}
	for _, f := range r.Features {
		p.f("\n%s: %s (%s)\n", f.Title, f.Mark, marks(f.Marks))
		for _, d := range f.Dimensions {
			p.dimension(d)
		}
	}
	return p.err
}

func (p *printer) dimension(d verify.Dimension) {
	p.f("  [%s] %s (%s: %s)%s\n", d.Mark, d.ID, d.Kind, d.Key, reason(d.Reason))
	if len(d.Files) > 0 {
		p.f("      in %s\n", strings.Join(d.Files, ", "))
	}
	for _, diff := range d.Differences {
		where := diff.File
		if diff.Object != "" {
			where = diff.Object
		}
		if diff.Path != "" {
			where += " " + diff.Path
		}
		cause := "drift"
		switch {
		case diff.Planned != "":
			cause = "planned: " + diff.Planned
		case diff.Input != "":
			cause = "input " + diff.Input
		}
		p.f("      %s: rendered %q, current %q (%s)\n", where, diff.Rendered, diff.Current, cause)
	}
	if pr := d.Probe; pr != nil {
		p.f("      expect %s\n", statuses(pr.Expect))
		for _, req := range pr.Requests {
			p.f("      %s %s%s%s\n", probeMark(req.OK), req.URL, client(req.Client), outcome(req))
		}
	}
	if lv := d.Live; lv != nil {
		if lv.AuthRequired != nil {
			p.f("      muster: %s\n", strings.TrimSpace(lv.AuthRequired.Message))
		}
		for _, c := range lv.Checks {
			p.f("      [%s] %s %s%s%s\n", c.Mark, c.Kind, target(c), message(c.Message), note(c.Note))
		}
	}
}

// target names what a live check looked at.
func target(c verify.Check) string {
	if c.URL != "" {
		return c.URL
	}
	if c.Namespace != "" {
		return c.Resource + " " + c.Namespace + "/" + c.Name
	}
	return c.Resource + " " + c.Name
}

func message(m string) string {
	if m == "" {
		return ""
	}
	return ": " + m
}

func note(n string) string {
	if n == "" {
		return ""
	}
	return " — " + n
}

// marks counts the marks in their severity order, the way the result rolls up.
func marks(m map[verify.Mark]int) string {
	var parts []string
	for _, mark := range []verify.Mark{verify.Drifted, verify.DiffersByInput, verify.Planned, verify.AsDefined, verify.NotChecked} {
		if n := m[mark]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, mark))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func statuses(codes []int) string {
	parts := make([]string, 0, len(codes))
	for _, c := range codes {
		parts = append(parts, fmt.Sprint(c))
	}
	return strings.Join(parts, "|")
}

func probeMark(ok bool) string {
	if ok {
		return "ok  "
	}
	return "FAIL"
}

func client(c string) string {
	if c == "" {
		return ""
	}
	return " (" + c + ")"
}

func outcome(req verify.Request) string {
	if req.Error != "" {
		return " — " + req.Error
	}
	return " → " + req.Answer()
}

// Decision is approve_action or deny_action: the manager's message, then the
// Action as it stands.
func Decision(w io.Writer, d tools.Decision) error {
	p := &printer{w: w}
	p.f("%s\n", d.Message)
	return p.action(d.Action)
}

// Merge is merge_action: the manager's message, the pull requests this call
// merged, where it stopped when it did, then the Action as it stands.
func Merge(w io.Writer, r tools.MergeResult) error {
	p := &printer{w: w}
	p.f("%s\n", r.Message)
	if len(r.Merged) > 0 {
		p.f("Merged by this call, in order:\n")
		for _, pr := range r.Merged {
			p.f("  %s#%d %s\n", pr.Repository, pr.Number, pr.URL)
		}
	}
	if r.Waiting != "" {
		p.f("Waiting: %s\n", r.Waiting)
	}
	return p.action(r.Action)
}

// Watch is watch_action: the manager's message, the rollout picture object
// by object, the dimensions that decided, what follows, then the Action.
func Watch(w io.Writer, r tools.WatchResult) error {
	p := &printer{w: w}
	p.f("%s\n", r.Message)
	if len(r.Objects) > 0 {
		p.f("Rollout of %s (ready: %s):\n", dash(r.Installation), yesNo(r.Ready))
		rows := make([][]string, 0, len(r.Objects))
		for _, o := range r.Objects {
			rows = append(rows, []string{o.Kind + " " + o.Namespace + "/" + o.Name, "Ready=" + dash(o.Ready), dash(o.Revision), o.Message})
		}
		p.table(2, rows)
	}
	if len(r.Red) > 0 {
		p.f("Red:\n")
		for _, d := range r.Red {
			p.f("  %s\n", d)
		}
	}
	if len(r.Planned) > 0 {
		p.f("Planned:\n")
		for _, d := range r.Planned {
			p.f("  %s\n", d)
		}
	}
	if r.Verify != nil {
		p.f("Probes: %s\n", marks(r.Verify.Summary))
	}
	if r.Report != "" {
		p.f("Report:\n  %s\n", strings.ReplaceAll(r.Report, "\n", "\n  "))
	}
	if r.Next != "" {
		p.f("Next: %s\n", r.Next)
	}
	return p.action(r.Action)
}

// action appends the Action an answer carries, when it carries one.
func (p *printer) action(a *actions.Action) error {
	if p.err != nil || a == nil {
		return p.err
	}
	p.f("\n")
	if p.err != nil {
		return p.err
	}
	return Action(p.w, *a)
}

// printer writes to w and keeps the first error; every function of this
// package returns it, so a closed pipe is reported once instead of on every
// line.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) f(format string, a ...any) {
	if p.err != nil {
		return
	}
	_, p.err = fmt.Fprintf(p.w, format, a...)
}

// table aligns rows in columns, every line indented by indent spaces.
func (p *printer) table(indent int, rows [][]string) {
	if p.err != nil {
		return
	}
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	prefix := strings.Repeat(" ", indent)
	for _, row := range rows {
		if _, err := fmt.Fprintln(tw, prefix+strings.Join(row, "\t")); err != nil {
			p.err = err
			return
		}
	}
	if err := tw.Flush(); err != nil {
		p.err = err
		return
	}
	_, p.err = p.w.Write(buf.Bytes())
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func reason(s string) string {
	if s == "" {
		return ""
	}
	return " — " + s
}

func at(t *time.Time) string {
	if t == nil {
		return ""
	}
	return " at " + t.Format(time.RFC3339)
}

// age is the time since t in the largest whole unit, the way kubectl prints it.
func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// unseen are a kept file's literals the record holds encrypted, "path: value" each.
func unseen(list []plan.Unseen) string {
	parts := make([]string, 0, len(list))
	for _, u := range list {
		parts = append(parts, u.Path+": "+u.Value)
	}
	return strings.Join(parts, ", ")
}
