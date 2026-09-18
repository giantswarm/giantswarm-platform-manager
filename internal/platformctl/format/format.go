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
	header := []string{"NAME", "CUSTOMER", "PROVIDER", "HUB", "OPT-IN"}
	for _, c := range r.Capabilities {
		header = append(header, strings.ToUpper(c))
	}
	header = append(header, "LAST ACTION")
	rows := [][]string{header}
	for _, inst := range r.Installations {
		row := []string{inst.Name, dash(inst.Customer), dash(inst.Provider), yesNo(inst.Hub), optInState(inst.OptIn)}
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

func optInState(o *installations.OptIn) string {
	if o == nil {
		return string(installations.OptInUnreadable)
	}
	return string(o.State)
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
		p.installation(inst, content)
	}
	if len(r.PullRequests) > 0 {
		p.f("\nPull requests, in order:\n")
		for _, pr := range r.PullRequests {
			p.f("  %d. %s: %s for %s\n", pr.Order, pr.Repository, plural(pr.Changes, "change"), strings.Join(pr.Installations, ", "))
			if len(pr.GeneratedSecrets) > 0 {
				p.f("     generated secrets: %s\n", strings.Join(pr.GeneratedSecrets, ", "))
			}
		}
	}
	if len(r.Skipped) > 0 {
		p.f("\nSkipped:\n")
		for _, s := range r.Skipped {
			p.f("  %s: %s\n", s.Name, s.Reason)
			if s.OptIn != nil && s.OptIn.HowToOptIn != "" {
				p.f("     how to opt in: %s\n", s.OptIn.HowToOptIn)
			}
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
	suffix := ""
	if inst.OptIn != nil {
		suffix = " (" + string(inst.OptIn.State) + ")"
	}
	p.f("\n%s: %s%s\n", inst.Name, inst.State, suffix)
	if inst.Refused != "" {
		p.f("  Refused: %s\n", inst.Refused)
	}
	if inst.CommitRefused != "" {
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
	}
	for _, inc := range inst.Includes {
		p.f("  Includes: %s:%s %s %s (%s)\n", inc.Repository, inc.Path, inc.List, inc.Resource, inc.Change)
	}
	if len(inst.GeneratedSecrets) > 0 {
		p.f("  Generated at commit:\n")
		for _, g := range inst.GeneratedSecrets {
			p.f("    %s (%s, %d): %s\n", g.Name, g.Kind, g.Length, strings.Join(g.Files, ", "))
		}
	}
	if len(inst.SuppliedSecrets) > 0 {
		p.f("  You supply at commit: %s\n", strings.Join(inst.SuppliedSecrets, ", "))
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
			p.f("  %s#%d %s %s\n", pr.Repository, pr.Number, dash(pr.State), pr.URL)
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
	if len(a.Spec.Inputs) > 0 {
		if b, err := json.MarshalIndent(a.Spec.Inputs, "  ", "  "); err == nil {
			p.f("Inputs:\n  %s\n", b)
		}
	}
	return p.err
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
