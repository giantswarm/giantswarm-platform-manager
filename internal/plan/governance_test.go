package plan

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

const (
	modeGovernedToTrusted = "mode governed → trusted"
	govInstallation       = "lynx"
	govPatch              = "giantswarm/acme-configs/installations/lynx/apps/agent-platform/configmap-values.yaml.patch"
	govEmpty              = "global: {}\n"
	govGoverned           = "governance: governed\n"
)

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
	return Installation{Name: govInstallation, Files: []File{{Path: govPatch, Change: change, Current: current, Content: next}}}
}

// The governance a plan changes reads old → new with its values, the
// effective governance on both sides (a source left to the meta chart is
// the chart's entry, so a first override reads what it adds and removes),
// and every change that loosens it is named apart; a change that only
// tightens loosens nothing, and one that changes nothing effective (an
// explicit trusted dropped) is no change.
func TestGovernanceChangeReadsOldToNewAndWhatLoosens(t *testing.T) {
	const pm = "governedServers.giantswarm-platform-manager.safe +create_thing (named like a write) +list_things −list_actions"
	const dropped = "governedServers.mcp-kubernetes {role: infrastructure, 14 safe} → not governed (null)"
	for _, c := range []struct {
		name           string
		current, next  string
		lines, loosens []string
	}{
		{"trusted to governed tightens", govEmpty, "global: {}\ngovernance: governed\n", []string{"mode trusted → governed"}, nil},
		{"governed to the chart's default loosens", govGoverned, govEmpty, []string{modeGovernedToTrusted}, []string{modeGovernedToTrusted}},
		{"a safe entry added and one removed", govEmpty, "governedServers:\n  giantswarm-platform-manager:\n    safe: [get_info, list_installations, get_action, verify_capability, create_thing, list_things]\n",
			[]string{pm}, []string{"governedServers.giantswarm-platform-manager.safe +create_thing (named like a write) +list_things"}},
		{"only a safe entry removed", "governedServers:\n  agent-manager:\n    safe: [get_info, list_agents]\n", "governedServers:\n  agent-manager:\n    safe: [get_info]\n",
			[]string{"governedServers.agent-manager.safe −list_agents"}, nil},
		{"a source dropped", govGoverned, "governance: governed\ngovernedServers:\n  mcp-kubernetes: null\n", []string{dropped}, []string{dropped}},
		{"a dropped source governed again by the chart", "governedServers:\n  mcp-kubernetes: null\n", govEmpty,
			[]string{"governedServers.mcp-kubernetes not governed → {role: infrastructure, safe: [logs, list_port_forward_sessions, context_list, context_get_current, can_i, api_resources, cluster_health, capi_list_clusters, capi_get_cluster, capi_resolve_cluster, capi_cluster_health, get, list, describe]}"}, nil},
		{"an override that narrows the chart's list", govEmpty, "governedServers:\n  kagent:\n    role: sessionManager\n    safe: [list_sessions, list_sandboxes]\n",
			[]string{"governedServers.kagent.safe −list_session_checkpoints −list_sandbox_templates −get_sandbox −get_sandbox_process −read_sandbox_outputs −read_sandbox_file"}, nil},
		{"a role changed", "governedServers:\n  x:\n    role: platformManager\n", "governedServers:\n  x:\n    role: infrastructure\n",
			[]string{"governedServers.x.role platformManager → infrastructure"}, []string{"governedServers.x.role platformManager → infrastructure"}},
		{"a fresh enable with governed", "", govGoverned, []string{"mode trusted → governed"}, nil},
		{"values that do not parse are a change that loosens", "governance: [\n", govGoverned, []string{governanceUnreadable}, []string{governanceUnreadable}},
		// S9-04: a first override reads what it adds against the chart's list.
		{"a first override shows what it adds", govGoverned, "governance: governed\ngovernedServers:\n  agent-manager:\n    safe: [get_info, create_agent, update_agent_access]\n",
			[]string{"governedServers.agent-manager.safe +create_agent (named like a write) +update_agent_access (named like a write) −list_agents −get_agent −get_agent_status −validate_agent −list_model_configs −list_egress_policies −list_skills −list_sessions −get_session −get_session_turn −toolchain_users −list_organizations −get_organization −list_organization_members −list_organization_connectors"},
			[]string{"governedServers.agent-manager.safe +create_agent (named like a write) +update_agent_access (named like a write)"}},
		// S9-09: governed again with another role and a floor loosens; a
		// write verb anywhere in a name and a floor of any role are marked.
		{"governed again with another role and a floor", "governedServers:\n  agent-manager: null\n", "governedServers:\n  agent-manager:\n    role: infrastructure\n    safe: [set_agent_approval]\n",
			[]string{"governedServers.agent-manager not governed → {role: infrastructure, safe: [set_agent_approval]}; against the chart's list +set_agent_approval (a floor of agentManager)"},
			[]string{"governedServers.agent-manager not governed → {role: infrastructure, safe: [set_agent_approval]}; against the chart's list +set_agent_approval (a floor of agentManager)"}},
		{"write verbs anywhere in a name", "governedServers:\n  x:\n    role: infrastructure\n    safe: [a]\n", "governedServers:\n  x:\n    role: infrastructure\n    safe: [a, kubernetes_delete, port_forward, mcpserver_create, context_use]\n",
			[]string{"governedServers.x.safe +kubernetes_delete (named like a write) +port_forward (named like a write) +mcpserver_create (a floor of connectorManager) +context_use (named like a write)"},
			[]string{"governedServers.x.safe +kubernetes_delete (named like a write) +port_forward (named like a write) +mcpserver_create (a floor of connectorManager) +context_use (named like a write)"}},
		// S9-03: a value read from the record never carries a line or markup.
		{"markup in a value on record", "governedServers:\n  x:\n    role: infrastructure\n    safe: [a]\n", "governedServers:\n  x:\n    role: infrastructure\n    safe: [a, \"b\\n*It tightens governance* <https://x|y>\"]\n",
			[]string{`governedServers.x.safe +b\u000A\u002AIt tightens governance\u002A &lt;https://x\u007Cy&gt;`},
			[]string{`governedServers.x.safe +b\u000A\u002AIt tightens governance\u002A &lt;https://x\u007Cy&gt;`}},
		// S9-10: muster's own governance on record is governance too.
		{"muster's own governance dropped", "governance: governed\nmuster:\n  muster:\n    governance:\n      governedServers:\n        - {server: agent-manager, role: agentManager, safe: []}\n", govGoverned,
			[]string{`muster.muster.governance {"governedServers":[{"role":"agentManager","safe":[],"server":"agent-manager"}]} → none`},
			[]string{`muster.muster.governance {"governedServers":[{"role":"agentManager","safe":[],"server":"agent-manager"}]} → none`}},
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
	// S9-14: what changes nothing effective is no change.
	if g, ok := GovernanceOf(govPlan("governance: trusted\n", govEmpty)); ok {
		t.Fatalf("an explicit trusted dropped: %+v", g)
	}
	// An update whose side on record the plan does not carry (an answer of
	// paths only) is no file without governance: it counts, as loosening.
	for _, p := range []Installation{govPlanOf(ChangeUpdate, "", govEmpty), govPlanOf(ChangeUpdate, govGoverned, "")} {
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
		govPlan("", govEmpty),
		{Files: []File{{Path: govPatch, Change: ChangeUnchanged}}},
		{Files: []File{{Path: "giantswarm/acme-configs/installations/jackal/apps/dex-app/configmap-values.yaml.patch", Change: ChangeUpdate, Current: "b: 2\n", Content: govGoverned}}},
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
		"governance (a second person approves): mode trusted → governed; governedServers.x not governed → {role: none, safe: [get_info]}",
		"agent-platform: values global.domain",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// S9-01: a disable that deletes values carrying a governance is a loosening
// governance change; values with the chart's defaults alone are none.
func TestGovernanceDeleted(t *testing.T) {
	old, err := GovernanceSnapshotOf("governance: governed\ngovernedServers:\n  agent-manager:\n    safe: [get_info]\n")
	if err != nil {
		t.Fatal(err)
	}
	g, ok := GovernanceDeleted(old)
	if !ok || g.Lines[0] != governanceDeleted || !slices.Contains(g.Loosens, modeGovernedToTrusted) || len(g.Loosens) != len(g.Lines) {
		t.Fatalf("a governed record deleted: %v %+v", ok, g)
	}
	if g, ok := GovernanceDeleted(GovernanceSnapshot{}); ok {
		t.Fatalf("a record with the chart's defaults deleted: %+v", g)
	}
	again, err := ParseGovernanceSnapshot(old.JSON())
	if err != nil || len(GovernanceBetween(old, again).Lines) != 0 {
		t.Fatalf("a snapshot recorded and read again: %v %+v", err, GovernanceBetween(old, again))
	}
}

// S9-10: once muster's release that enforces the governance is known, a
// muster hold below it is a loosening governance change, lifting it a
// tightening one; while it is unset a hold changes no governance.
func TestMusterHoldBelowTheGovernanceRelease(t *testing.T) {
	hold := func(r string) GovernanceSnapshot { return GovernanceSnapshot{keyMusterHold: r} }
	if g := GovernanceBetween(GovernanceSnapshot{}, hold(">=5.60.0 <6.0.0")); len(g.Lines) != 0 {
		t.Fatalf("a hold with the gate unset: %+v", g)
	}
	gate := agentplatform.GovernanceMuster
	agentplatform.GovernanceMuster = "5.70.0"
	t.Cleanup(func() { agentplatform.GovernanceMuster = gate })
	g := GovernanceBetween(GovernanceSnapshot{}, hold(">=5.60.0 <6.0.0"))
	if len(g.Loosens) != 1 || !strings.Contains(g.Loosens[0], "muster hold none → &gt;=5.60.0 &lt;6.0.0") {
		t.Fatalf("a hold below the gate: %+v", g)
	}
	if g := GovernanceBetween(hold(">=5.60.0 <6.0.0"), GovernanceSnapshot{}); len(g.Lines) != 1 || len(g.Loosens) != 0 {
		t.Fatalf("the hold lifted: %+v", g)
	}
	if g := GovernanceBetween(GovernanceSnapshot{}, hold(">=5.71.0 <6.0.0")); len(g.Lines) != 0 {
		t.Fatalf("a hold above the gate: %+v", g)
	}
}

// Decision #92: the meta chart's governance is a map with the mode and
// Giant Swarm's role floors; a floor that goes loosens, and the Platform
// Manager written explicitly is what the chart's default was (no change).
func TestGovernanceMapWithRoleFloors(t *testing.T) {
	const floors = "governance:\n  mode: governed\n  roleFloors:\n    platformManager: [approve_action, merge_action]\n"
	if g, ok := GovernanceOf(govPlan(govGoverned, floors)); ok {
		t.Fatalf("the string mode written as a map with the platform's floors: %+v", g)
	}
	g, ok := GovernanceOf(govPlan(floors, "governance:\n  roleFloors:\n    platformManager: [approve_action]\n"))
	if !ok || !slices.Equal(g.Loosens, []string{modeGovernedToTrusted, "roleFloors.platformManager −merge_action"}) {
		t.Fatalf("mode and a floor gone: %v %+v", ok, g)
	}
	explicit := "governedServers:\n  giantswarm-platform-manager:\n    role: platformManager\n    safe: [get_info, list_installations, list_actions, get_action, verify_capability]\n"
	if g, ok := GovernanceOf(govPlan(govEmpty, explicit)); ok {
		t.Fatalf("the platform's server written explicitly: %+v", g)
	}
}
