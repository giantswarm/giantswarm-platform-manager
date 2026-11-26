package plan

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// GovernanceChange is what a plan changes of the platform's governance
// (Governing tool calls, giantswarm/giantswarm#38902): the meta chart's
// top-level governance and governedServers in the installation's
// agent-platform values. Such a change is an Action a second person
// approves, on test installations too (internal/tools); its review reads
// these lines.
type GovernanceChange struct {
	// Lines are the changes, old → new with their values.
	Lines []string `json:"lines"`
	// Loosens are the lines that loosen the governance, or may: the mode
	// leaving governed, a source no longer governed or put back to the
	// chart's entry, a role changed or left to the chart's, a safe entry
	// added or a safe list replaced or put back to the chart's. The plan does
	// not know the chart's own entries, so a change against them counts.
	Loosens []string `json:"loosens,omitempty"`
}

// The values keys of the governance, top-level in the agent-platform values.
const (
	keyGovernance      = "governance"
	keyGovernedServers = "governedServers"
)

// governanceUnreadable is the line of a plan whose agent-platform values
// cannot be read on either side: counted as a change that loosens, so a
// file nobody can compare never passes without a second person.
const governanceUnreadable = "the agent-platform values could not be read: counted as a governance change that loosens"

// governanceFile says whether path is an installation's agent-platform
// values, the configmap patch that carries the governance.
func governanceFile(path string) bool {
	return strings.HasSuffix(path, "/apps/agent-platform/configmap-values.yaml.patch")
}

// GovernanceOf is the governance p changes, false when it changes none.
func GovernanceOf(p Installation) (GovernanceChange, bool) {
	for _, f := range p.Files {
		if !governanceFile(f.Path) || (f.Change != ChangeCreate && f.Change != ChangeUpdate) {
			continue
		}
		old, err1 := governanceValues(f.Current)
		next, err2 := governanceValues(f.Content)
		// A side the plan does not carry (an answer of paths only) is read as
		// unreadable, never as a file without governance.
		missing := f.Content == "" || (f.Change == ChangeUpdate && f.Current == "")
		if err1 != nil || err2 != nil || missing {
			return GovernanceChange{Lines: []string{governanceUnreadable}, Loosens: []string{governanceUnreadable}}, true
		}
		var g GovernanceChange
		g.mode(old[keyGovernance], next[keyGovernance])
		g.servers(old[keyGovernedServers], next[keyGovernedServers])
		if len(g.Lines) > 0 {
			return g, true
		}
	}
	return GovernanceChange{}, false
}

// governanceValues reads the governance keys of a values file; an empty
// file (a fresh enable's side on record) carries none.
func governanceValues(content string) (map[string]any, error) {
	docs, err := yamlDocs(content)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, d := range docs {
		for _, k := range []string{keyGovernance, keyGovernedServers} {
			if v, ok := d[k]; ok {
				out[k] = v
			}
		}
	}
	return out, nil
}

// Summary is the change as the review's line reads it.
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

// mode compares the mode; absent is the chart's default, trusted.
func (g *GovernanceChange) mode(old, next any) {
	if fmt.Sprint(old) == fmt.Sprint(next) {
		return
	}
	read := func(v any) string {
		if v == nil {
			return "trusted (the chart's default)"
		}
		return fmt.Sprint(v)
	}
	g.add(fmt.Sprintf("mode %s → %s", read(old), read(next)), fmt.Sprint(old) == "governed" && fmt.Sprint(next) != "governed")
}

// servers compares the governedServers overrides by source.
func (g *GovernanceChange) servers(oldV, nextV any) {
	old, _ := oldV.(map[string]any)
	next, _ := nextV.(map[string]any)
	for _, source := range sortedKeys(old, next) {
		o, inOld := old[source]
		n, inNext := next[source]
		at := keyGovernedServers + "." + source
		switch {
		case inOld && inNext && o != nil && n != nil:
			g.entry(at, o, n)
		case fmt.Sprint(inOld, o) != fmt.Sprint(inNext, n):
			// Anything but null → null-or-absent the other way round loosens:
			// the source leaves the governed servers or its typed entry gives
			// way to the chart's, which the plan does not know.
			tightens := inOld && o == nil
			g.add(fmt.Sprintf("%s %s → %s", at, serverEntry(inOld, o), serverEntry(inNext, n)), !tightens)
		}
	}
}

// entry compares one source's typed entries: its role and its safe list.
func (g *GovernanceChange) entry(at string, oldV, nextV any) {
	old, _ := oldV.(map[string]any)
	next, _ := nextV.(map[string]any)
	if fmt.Sprint(old["role"]) != fmt.Sprint(next["role"]) {
		g.add(fmt.Sprintf("%s.role %s → %s", at, orChart(old["role"], "role"), orChart(next["role"], "role")), true)
	}
	o, inOld := old["safe"]
	n, inNext := next["safe"]
	switch {
	case !inOld && !inNext:
	case !inOld || !inNext:
		g.add(fmt.Sprintf("%s.safe %s → %s", at, orChart(o, "list"), orChart(n, "list")), true)
	default:
		ol, nl := strs(asList(o)), strs(asList(n))
		var added, removed []string
		for _, v := range nl {
			if !slices.Contains(ol, v) {
				added = append(added, "+"+v+writeNote(v))
			}
		}
		for _, v := range ol {
			if !slices.Contains(nl, v) {
				removed = append(removed, "−"+v)
			}
		}
		if len(added)+len(removed) == 0 {
			return
		}
		g.Lines = append(g.Lines, fmt.Sprintf("%s.safe %s", at, strings.Join(slices.Concat(added, removed), " ")))
		if len(added) > 0 {
			g.Loosens = append(g.Loosens, fmt.Sprintf("%s.safe %s", at, strings.Join(added, " ")))
		}
	}
}

// serverEntry reads one source's state: the chart's entry (absent), not
// governed (null), or the typed entry.
func serverEntry(present bool, v any) string {
	switch {
	case !present:
		return "the chart's entry"
	case v == nil:
		return "null (not governed)"
	}
	m, _ := v.(map[string]any)
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		val := m[k]
		if l, ok := val.([]any); ok {
			val = "[" + strings.Join(strs(l), ", ") + "]"
		}
		parts = append(parts, fmt.Sprintf("%s: %v", k, val))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// orChart reads a role or safe list, absent as the chart's.
func orChart(v any, what string) string {
	if v == nil {
		return "the chart's " + what
	}
	if l, ok := v.([]any); ok {
		return "[" + strings.Join(strs(l), ", ") + "]"
	}
	return fmt.Sprint(v)
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

// writeVerbs are the leading words of a tool name that changes something:
// a safe entry named so is marked, so the second person looks twice.
var writeVerbs = []string{"add", "apply", "approve", "authorize", "cancel", "connect", "create", "delete", "deny", "deploy", "disable", "enable", "exec", "fork", "grant", "invoke", "kill", "merge", "patch", "push", "remove", "replace", "restart", "revoke", "rotate", "run", "scale", "set", "start", "stop", "sync", "update", "write"}

func writeNote(tool string) string {
	verb, _, _ := strings.Cut(tool, "_")
	if slices.Contains(writeVerbs, verb) {
		return " (named like a write)"
	}
	return ""
}
