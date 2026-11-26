package plan

import (
	"slices"
	"strings"
	"testing"
)

const govPatch = "giantswarm/acme-configs/installations/jackal/apps/agent-platform/configmap-values.yaml.patch"

// govPlan is an installation whose agent-platform values move from current
// to next.
func govPlan(current, next string) Installation {
	change := ChangeUpdate
	if current == "" {
		change = ChangeCreate
	}
	return govPlanOf(change, current, next)
}

func govPlanOf(change Change, current, next string) Installation {
	return Installation{Name: "jackal", Files: []File{{Path: govPatch, Change: change, Current: current, Content: next}}}
}

// The governance a plan changes reads old → new with its values, and every
// change that loosens it — governed to trusted (or to the chart's default,
// trusted), a source dropped or put back to the chart's entry, a role
// changed or left to the chart's, a safe entry added (one named like a
// write marked) — is named apart; a change that only tightens loosens
// nothing.
func TestGovernanceChangeReadsOldToNewAndWhatLoosens(t *testing.T) {
	for _, c := range []struct {
		name           string
		current, next  string
		lines, loosens []string
	}{
		{"trusted to governed tightens", "global: {}\n", "global: {}\ngovernance: governed\n",
			[]string{"mode trusted (the chart's default) → governed"}, nil},
		{"governed to the chart's default loosens", "governance: governed\n", "global: {}\n",
			[]string{"mode governed → trusted (the chart's default)"}, []string{"mode governed → trusted (the chart's default)"}},
		{"a safe entry added and one removed", "governedServers:\n  giantswarm-platform-manager:\n    safe: [get_info, list_actions]\n",
			"governedServers:\n  giantswarm-platform-manager:\n    safe: [get_info, create_thing, list_installations]\n",
			[]string{"governedServers.giantswarm-platform-manager.safe +create_thing (named like a write) +list_installations −list_actions"},
			[]string{"governedServers.giantswarm-platform-manager.safe +create_thing (named like a write) +list_installations"}},
		{"only a safe entry removed", "governedServers:\n  agent-manager:\n    safe: [get_info, list_agents]\n", "governedServers:\n  agent-manager:\n    safe: [get_info]\n",
			[]string{"governedServers.agent-manager.safe −list_agents"}, nil},
		{"a source dropped", "governance: governed\n", "governance: governed\ngovernedServers:\n  mcp-kubernetes: null\n",
			[]string{"governedServers.mcp-kubernetes the chart's entry → null (not governed)"}, []string{"governedServers.mcp-kubernetes the chart's entry → null (not governed)"}},
		{"a dropped source governed again", "governedServers:\n  mcp-kubernetes: null\n", "global: {}\n",
			[]string{"governedServers.mcp-kubernetes null (not governed) → the chart's entry"}, nil},
		{"an override over the chart's entry", "global: {}\n", "governedServers:\n  kagent:\n    role: sessionManager\n    safe: [list_sessions]\n",
			[]string{"governedServers.kagent the chart's entry → {role: sessionManager, safe: [list_sessions]}"},
			[]string{"governedServers.kagent the chart's entry → {role: sessionManager, safe: [list_sessions]}"}},
		{"a role changed", "governedServers:\n  x:\n    role: platformManager\n", "governedServers:\n  x:\n    role: infrastructure\n",
			[]string{"governedServers.x.role platformManager → infrastructure"}, []string{"governedServers.x.role platformManager → infrastructure"}},
		{"a safe list back to the chart's", "governedServers:\n  x:\n    role: infrastructure\n    safe: [a]\n", "governedServers:\n  x:\n    role: infrastructure\n",
			[]string{"governedServers.x.safe [a] → the chart's list"}, []string{"governedServers.x.safe [a] → the chart's list"}},
		{"a fresh enable with governed", "", "governance: governed\n",
			[]string{"mode trusted (the chart's default) → governed"}, nil},
		{"values that do not parse are a change that loosens", "governance: [\n", "governance: governed\n",
			[]string{governanceUnreadable}, []string{governanceUnreadable}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, ok := GovernanceOf(govPlan(c.current, c.next))
			if !ok {
				t.Fatal("no governance change")
			}
			if !slices.Equal(g.Lines, c.lines) || !slices.Equal(g.Loosens, c.loosens) {
				t.Fatalf("lines %q loosens %q, want %q and %q", g.Lines, g.Loosens, c.lines, c.loosens)
			}
		})
	}
	// An update whose side on record the plan does not carry (an answer of
	// paths only) is no file without governance: it counts, as loosening.
	for _, p := range []Installation{govPlanOf(ChangeUpdate, "", "global: {}\n"), govPlanOf(ChangeUpdate, "governance: governed\n", "")} {
		if g, ok := GovernanceOf(p); !ok || !slices.Equal(g.Loosens, []string{governanceUnreadable}) {
			t.Fatalf("an update without a side: %v %+v", ok, g)
		}
	}
}

// A plan that leaves the governance as it is, or does not touch the
// agent-platform values, changes no governance, whatever else it changes.
func TestNoGovernanceChange(t *testing.T) {
	for _, p := range []Installation{
		govPlan("governance: governed\nglobal: {domain: a}\n", "governance: governed\nglobal: {domain: b}\n"),
		govPlan("", "global: {}\n"),
		{Files: []File{{Path: govPatch, Change: ChangeUnchanged}}},
		{Files: []File{{Path: "giantswarm/acme-configs/installations/jackal/apps/dex-app/configmap-values.yaml.patch", Change: ChangeUpdate, Current: "a: 1\n", Content: "governance: governed\n"}}},
	} {
		if g, ok := GovernanceOf(p); ok {
			t.Fatalf("a governance change: %+v", g)
		}
	}
}

// The summary reads the governance with its values on a line of its own,
// first after the switch-off warning, and does not name its keys again
// among the agent-platform values.
func TestSummaryReadsTheGovernanceWithItsValues(t *testing.T) {
	got := Summary(govPlan("global:\n  domain: a\n", "global:\n  domain: b\ngovernance: governed\ngovernedServers:\n  x:\n    safe: [get_info]\n"))
	want := []string{
		"governance (a second person approves): mode trusted (the chart's default) → governed; governedServers.x the chart's entry → {safe: [get_info]} — loosens: governedServers.x the chart's entry → {safe: [get_info]}",
		"agent-platform: values global.domain",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
