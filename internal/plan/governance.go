package plan

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// GovernanceChange is what a plan changes of the platform's governance
// (Governing tool calls, giantswarm/giantswarm#38902): in the installation's
// agent-platform values the meta chart's top-level governance and
// governedServers, muster's own muster.muster.governance, and a muster hold
// below the release that enforces it. Such a change is an Action a second
// person approves, on test installations too (internal/tools); its review
// reads these lines.
type GovernanceChange struct {
	// Lines are the changes, old → new with their values, the effective
	// governance on both sides: a source left to the meta chart reads as the
	// chart's entry (definitions/agent-platform/governance.yaml).
	Lines []string `json:"lines"`
	// Loosens are the lines that loosen the governance, or may: the mode
	// leaving governed, a source no longer governed, a role changed, a safe
	// entry added, muster's own governance changed, a muster hold below the
	// release that enforces it, values that cannot be read.
	Loosens []string `json:"loosens,omitempty"`
}

// The values keys that carry the governance, and the snapshot's keys.
const (
	keyGovernance       = "governance"
	keyGovernedServers  = "governedServers"
	keyMusterGovernance = "musterGovernance"
	keyMusterHold       = "musterHold"
	modeGoverned        = "governed"
	modeTrusted         = "trusted"
)

// governanceUnreadable is the line of a plan whose agent-platform values
// cannot be read on either side: counted as a change that loosens, so a
// file nobody can compare never passes without a second person.
const governanceUnreadable = "the agent-platform values could not be read: counted as a governance change that loosens"

// governanceDeleted leads the change of a disable that deletes the values.
const governanceDeleted = "the agent-platform values are deleted with the platform, and the governance on record with them"

// governanceFile says whether path is an installation's agent-platform
// values, the configmap patch that carries the governance.
func governanceFile(path string) bool {
	return strings.HasSuffix(path, "/apps/agent-platform/configmap-values.yaml.patch")
}

// GovernanceSnapshot is the governance an installation's agent-platform
// values carry, by the snapshot's keys; absent keys are the chart's
// defaults. It is what an Action records as the governance its commit
// leaves (spec.governanceAfter), so the next plan compares against it.
type GovernanceSnapshot map[string]any

// GovernanceSnapshotOf reads the governance of a values file; an empty file
// (a fresh enable's side on record) carries none.
func GovernanceSnapshotOf(content string) (GovernanceSnapshot, error) {
	docs, err := yamlDocs(content)
	if err != nil {
		return nil, err
	}
	out := GovernanceSnapshot{}
	for _, d := range docs {
		if v, ok := d[keyGovernance]; ok {
			out[keyGovernance] = v
		}
		if v, ok := d[keyGovernedServers]; ok {
			out[keyGovernedServers] = v
		}
		if v := dig(d, "muster", "muster", "governance"); v != nil {
			out[keyMusterGovernance] = v
		}
		if v := dig(d, "components", "muster", "versionRange"); v != nil {
			out[keyMusterHold] = v
		}
	}
	return out, nil
}

// ParseGovernanceSnapshot reads a snapshot an Action recorded.
func ParseGovernanceSnapshot(s string) (GovernanceSnapshot, error) {
	var out GovernanceSnapshot
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = GovernanceSnapshot{}
	}
	return out, nil
}

// JSON is the snapshot as an Action records it, keys sorted.
func (s GovernanceSnapshot) JSON() string {
	b, _ := json.Marshal(map[string]any(s))
	return string(b)
}

func dig(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = mm[k]
	}
	return v
}

// GovernanceFile is p's agent-platform values on record and as rendered,
// false when p does not create or update them. unreadable: a side does not
// parse or the plan does not carry it (an answer of paths only), which is
// never read as a file without governance.
func GovernanceFile(p Installation) (current, next GovernanceSnapshot, ok, unreadable bool) {
	for _, f := range p.Files {
		if !governanceFile(f.Path) || (f.Change != ChangeCreate && f.Change != ChangeUpdate) {
			continue
		}
		cur, err1 := GovernanceSnapshotOf(f.Current)
		nxt, err2 := GovernanceSnapshotOf(f.Content)
		missing := f.Content == "" || (f.Change == ChangeUpdate && f.Current == "")
		return cur, nxt, true, err1 != nil || err2 != nil || missing
	}
	return nil, nil, false, false
}

// GovernanceOf is the governance p changes against the record, false when
// it changes none.
func GovernanceOf(p Installation) (GovernanceChange, bool) {
	current, next, ok, unreadable := GovernanceFile(p)
	switch {
	case !ok:
		return GovernanceChange{}, false
	case unreadable:
		return GovernanceUnreadable(), true
	}
	g := GovernanceBetween(current, next)
	return g, len(g.Lines) > 0
}

// GovernanceUnreadable is the change of values that cannot be read.
func GovernanceUnreadable() GovernanceChange {
	return GovernanceChange{Lines: []string{governanceUnreadable}, Loosens: []string{governanceUnreadable}}
}

// GovernanceDeleted is the change of a disable that deletes values carrying
// old: every governance the record held goes, a loosening; none when the
// record held only the chart's defaults.
func GovernanceDeleted(old GovernanceSnapshot) (GovernanceChange, bool) {
	g := GovernanceBetween(old, GovernanceSnapshot{})
	if len(g.Lines) == 0 {
		return g, false
	}
	g.Lines = append([]string{governanceDeleted}, g.Lines...)
	g.Loosens = append([]string{governanceDeleted}, g.Loosens...)
	return g, true
}

// Summary is the change as the summary's line reads it.
func (g GovernanceChange) Summary() string {
	s := "governance (a second person approves): " + strings.Join(g.Lines, "; ")
	if len(g.Loosens) > 0 {
		s += " — loosens: " + strings.Join(g.Loosens, "; ")
	}
	return s
}

func (g *GovernanceChange) add(line string, loosens bool) {
	g.Lines = append(g.Lines, line)
	if loosens {
		g.Loosens = append(g.Loosens, line)
	}
}

// GovernanceBetween compares the effective governance of two snapshots: the
// mode (absent is trusted), each server as muster governs it (the meta
// chart's entry where the values leave a source alone), muster's own
// governance and the muster hold.
func GovernanceBetween(old, next GovernanceSnapshot) GovernanceChange {
	var g GovernanceChange
	om, nm := effectiveMode(old[keyGovernance]), effectiveMode(next[keyGovernance])
	if om != nm {
		g.add(fmt.Sprintf("mode %s → %s", om, nm), om == modeGoverned)
	}
	g.servers(effectiveServers(old[keyGovernedServers]), effectiveServers(next[keyGovernedServers]))
	if o, n := canonical(old[keyMusterGovernance]), canonical(next[keyMusterGovernance]); o != n {
		g.add(fmt.Sprintf("muster.muster.governance %s → %s", short(o), short(n)), true)
	}
	oh, nh := fmt.Sprint(orEmpty(old[keyMusterHold])), fmt.Sprint(orEmpty(next[keyMusterHold]))
	if oh != nh && (agentplatform.MusterHoldBelowGovernance(oh) || agentplatform.MusterHoldBelowGovernance(nh)) {
		g.add(fmt.Sprintf("muster hold %s → %s (below muster %s, which enforces the governance)", quoteOr(oh, "none"), quoteOr(nh, "none"), agentplatform.GovernanceMuster), agentplatform.MusterHoldBelowGovernance(nh))
	}
	return g
}

func effectiveMode(v any) string {
	if v == nil {
		return modeTrusted
	}
	return clean(fmt.Sprint(v))
}

// server is a source as muster governs it: its role and safe list.
type server struct {
	role string
	safe []string
	// odd: the values' entry is no map, nor null; read as loosening.
	odd string
}

// effectiveServers are the governed servers by source: the meta chart's,
// each override over its source's entry (Helm merges the maps, a list
// replaces), a null source not governed (nil).
func effectiveServers(v any) map[string]*server {
	out := map[string]*server{}
	for source, s := range definitions.Governance().Chart {
		out[source] = &server{role: s.Role, safe: slices.Clone(s.Safe)}
	}
	overrides, _ := v.(map[string]any)
	if v != nil && overrides == nil {
		out[""] = &server{odd: fmt.Sprint(v)}
	}
	for source, o := range overrides {
		if o == nil {
			out[source] = nil
			continue
		}
		m, ok := o.(map[string]any)
		if !ok {
			out[source] = &server{odd: fmt.Sprint(o)}
			continue
		}
		s := &server{}
		if base := out[source]; base != nil {
			*s = *base
		}
		if r, ok := m["role"]; ok {
			s.role = fmt.Sprint(r)
		}
		if l, ok := m["safe"]; ok {
			list, _ := l.([]any)
			s.safe = strs(list)
		}
		out[source] = s
	}
	return out
}

// servers compares the governed servers by source.
func (g *GovernanceChange) servers(old, next map[string]*server) {
	chart := definitions.Governance().Chart
	for _, source := range sortedKeys(old, next) {
		o, n := old[source], next[source]
		at := keyGovernedServers + "." + clean(source)
		switch {
		case o == nil && n == nil:
		case n != nil && n.odd != "":
			if o == nil || o.odd != n.odd {
				g.add(fmt.Sprintf("%s is no {role, safe} entry: %s", at, short(n.odd)), true)
			}
		case o != nil && o.odd != "":
			g.add(fmt.Sprintf("%s was no {role, safe} entry: %s", at, short(o.odd)), true)
			if n != nil {
				g.add(fmt.Sprintf("%s → %s", at, n.describe()), false)
			}
		case n == nil:
			g.add(fmt.Sprintf("%s %s → not governed (null)", at, o.brief()), true)
		case o == nil:
			// Governed again, or newly: it loosens only against what the chart
			// governs the source as, a server key of its own tightens.
			base, known := chart[source]
			var added []string
			if known {
				added = notIn(n.safe, base.Safe)
			}
			line := fmt.Sprintf("%s not governed → %s", at, n.describe())
			if len(added) > 0 {
				line += "; against the chart's list " + strings.Join(marked(added), " ")
			}
			g.add(line, known && (n.role != base.Role || len(added) > 0) || floorsIn(n.safe))
		default:
			if o.role != n.role {
				g.add(fmt.Sprintf("%s.role %s → %s", at, quoteOr(o.role, "none"), quoteOr(n.role, "none")), true)
			}
			added, removed := notIn(n.safe, o.safe), notIn(o.safe, n.safe)
			if len(added)+len(removed) == 0 {
				continue
			}
			parts := marked(added)
			for _, r := range removed {
				parts = append(parts, "−"+clean(r))
			}
			g.Lines = append(g.Lines, fmt.Sprintf("%s.safe %s", at, strings.Join(parts, " ")))
			if len(added) > 0 {
				g.Loosens = append(g.Loosens, fmt.Sprintf("%s.safe %s", at, strings.Join(marked(added), " ")))
			}
		}
	}
}

// brief is a server that goes, as its role and the size of its safe list.
func (s *server) brief() string {
	if s.odd != "" {
		return short(s.odd)
	}
	return fmt.Sprintf("{role: %s, %d safe}", quoteOr(s.role, "none"), len(s.safe))
}

func (s *server) describe() string {
	if s.odd != "" {
		return s.odd
	}
	safe := make([]string, 0, len(s.safe))
	for _, t := range s.safe {
		safe = append(safe, clean(t))
	}
	return fmt.Sprintf("{role: %s, safe: [%s]}", quoteOr(clean(s.role), "none"), strings.Join(safe, ", "))
}

// notIn are the entries of a not in b, in a's order.
func notIn(a, b []string) []string {
	var out []string
	for _, v := range a {
		if !slices.Contains(b, v) {
			out = append(out, v)
		}
	}
	return out
}

// marked are added safe entries as the review reads them: +name, marked
// where it is a floor of any role or named like a write.
func marked(added []string) []string {
	out := make([]string, 0, len(added))
	for _, v := range added {
		out = append(out, "+"+clean(v)+entryNote(v))
	}
	return out
}

func floorsIn(safe []string) bool {
	return slices.ContainsFunc(safe, func(t string) bool { return definitions.Governance().FloorRole(t) != "" })
}

// writeVerbs are the words of a tool name that changes something: a safe
// entry with one anywhere in its name is marked, so the second person looks
// twice.
var writeVerbs = []string{"add", "apply", "approve", "authorize", "cancel", "connect", "create", "delete", "deny", "deploy", "disable", "enable", "exec", "fork", "forward", "grant", "invoke", "kill", "merge", "patch", "push", "remove", "replace", "restart", "revoke", "rotate", "run", "scale", "set", "start", "stop", "sync", "update", "use", "write"}

func entryNote(tool string) string {
	if role := definitions.Governance().FloorRole(tool); role != "" {
		return " (a floor of " + role + ")"
	}
	for _, word := range strings.FieldsFunc(strings.ToLower(tool), func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		if slices.Contains(writeVerbs, word) {
			return " (named like a write)"
		}
	}
	return ""
}

// clean keeps a value from the values safe to show in Slack's markup: a
// character outside a tool or key name's and a value's punctuation
// (letters, digits, _ . - / : @ space { } [ ] , " =), and < > & as
// Slack's escapes is written as its code point, so no value carries a line, markup
// or a link of its own into the review.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', strings.ContainsRune("_.-/:@ {}[],\"=", r):
			b.WriteRune(r)
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '&':
			b.WriteString("&amp;")
		default:
			fmt.Fprintf(&b, "\\u%04X", r)
		}
	}
	return b.String()
}

func canonical(v any) string {
	if v == nil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// short is a value cut for a line; the pull requests carry it in full.
func short(s string) string {
	if s == "" {
		return "none"
	}
	s = clean(s)
	if r := []rune(s); len(r) > 200 {
		return string(r[:200]) + "… (in full in the configs pull request)"
	}
	return s
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func quoteOr(s, none string) string {
	if s == "" {
		return none
	}
	return clean(s)
}
