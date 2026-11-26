package agentplatform

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The governance releases the tests take as released.
const (
	testGovernanceChart   = "4.190.0"
	testGovernanceMuster  = "5.70.0"
	keyGovernance         = "governance"
	keyMode               = "mode"
	keyGovernedServers    = "governedServers"
	keyRole               = "role"
	toolListActions       = "list_actions"
	keySafe               = "safe"
	sourcePlatformManager = "giantswarm-platform-manager"
	toolGetInfo           = "get_info"
	toolGetSkill          = "get_skill"
	toolApproveAction     = "approve_action"
	toolSetAgentApproval  = "set_agent_approval"
	roleInfrastructure    = "infrastructure"
	roleSkillManager      = "skillManager"
	roleAgentManager      = "agentManager"
	pathSkillSafe0        = "/governance/governedServers/skill-manager/safe/0"
)

// withGovernanceReleases takes the governance releases as cut for the test.
// The tests that call it are not parallel: they run before every parallel
// test of the package and restore the unset floors.
func withGovernanceReleases(t *testing.T) {
	t.Helper()
	chart, muster := GovernanceChart, GovernanceMuster
	GovernanceChart, GovernanceMuster = testGovernanceChart, testGovernanceMuster
	t.Cleanup(func() { GovernanceChart, GovernanceMuster = chart, muster })
}

// governanceInput is the retired platform tenant's input with governance.
func governanceInput(t *testing.T, governance map[string]any) (map[string]any, map[string]string) {
	t.Helper()
	input, secrets := loadInput(t, shapePlatformTenantRetired)
	input[keyGovernance] = governance
	return input, secrets
}

// governanceOf is the governance and governedServers of a rendered patch.
func governanceOf(t *testing.T, patch string) (any, any) {
	t.Helper()
	var values map[string]any
	if err := yaml.Unmarshal([]byte(patch), &values); err != nil {
		t.Fatal(err)
	}
	return values["governance"], values["governedServers"]
}

// While no meta chart release takes governance (GovernanceChart unset, the
// shipped state), every governance choice is refused, naming the input:
// fail closed, so no installation reads as governed while its muster is not.
// No choice, or the default typed, renders as before with no key written.
func TestGovernanceIsRefusedUntilItsReleaseExists(t *testing.T) {
	for _, c := range []struct {
		name       string
		governance map[string]any
		names      string
	}{
		{"governed", map[string]any{keyMode: GovernanceGoverned}, "(governance.mode) needs the meta chart release that renders muster's governance configuration, and none is released yet"},
		{"a server override", map[string]any{keyGovernedServers: map[string]any{componentSkillManager: map[string]any{keySafe: []any{toolGetSkill}}}}, "(governance.governedServers) needs the meta chart release"},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := governanceInput(t, c.governance)
			_, err := Render(input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("got %v, want ErrInput naming %s", err, c.names)
			}
		})
	}
	input, secrets := governanceInput(t, map[string]any{keyMode: GovernanceTrusted})
	mode, servers := governanceOf(t, renderedPatch(t, input, secrets))
	if mode != nil || servers != nil {
		t.Fatalf("trusted writes governance %v, governedServers %v", mode, servers)
	}
}

// With the release cut, every installation on the 4 line renders Giant
// Swarm's own parts (decision #92): the meta chart's governance.roleFloors
// with the Platform Manager's floors, and the Platform Manager as a governed
// server with its safe list; governed adds governance.mode and the overrides
// join governedServers by source, in key order, a dropped source as null.
// What is rendered reads back as the same input (the platform's own entry
// dropped from it) and renders the same patch again (a reconcile keeps it).
func TestGovernanceRendersAndSurvivesAReconcile(t *testing.T) {
	withGovernanceReleases(t)
	governance := map[string]any{keyMode: GovernanceGoverned, keyGovernedServers: map[string]any{
		componentSkillManager: map[string]any{keySafe: []any{toolGetSkill, "list_skills"}},
		"mcp-kubernetes":      nil,
		"agent-platform/pro":  map[string]any{keyRole: roleInfrastructure, keySafe: []any{}},
	}}
	input, secrets := governanceInput(t, governance)
	patch := renderedPatch(t, input, secrets)
	for _, want := range []string{
		"\ngovernance:\n  mode: governed\n  roleFloors:\n    platformManager:\n      - approve_action\n      - merge_action\n",
		"\ngovernedServers:\n  agent-platform/pro:\n    role: infrastructure\n    safe: []\n  giantswarm-platform-manager:\n    role: platformManager\n    safe:\n      - get_info\n      - list_installations\n      - list_actions\n      - get_action\n      - verify_capability\n  mcp-kubernetes: null\n  skill-manager:\n    safe:\n      - get_skill\n      - list_skills\n",
		"Changed only through an Action a second person approves",
	} {
		if !strings.Contains(patch, want) {
			t.Fatalf("the patch carries no %q:\n%s", want, patch)
		}
	}
	mode, servers := governanceOf(t, patch)
	again, secrets := governanceInput(t, map[string]any{keyMode: mode.(map[string]any)[keyMode], keyGovernedServers: servers})
	if got := renderedPatch(t, again, secrets); got != patch {
		t.Fatalf("read back and rendered again:\n%s\nwant\n%s", got, patch)
	}

	// Trusted and no choice: the platform's parts alone, no mode.
	trusted, secrets := governanceInput(t, map[string]any{keyMode: GovernanceTrusted})
	got := renderedPatch(t, trusted, secrets)
	mode, servers = governanceOf(t, got)
	if m := mode.(map[string]any); m[keyMode] != nil || m["roleFloors"] == nil {
		t.Fatalf("trusted writes governance %v", mode)
	}
	if s := servers.(map[string]any); len(s) != 1 || s[sourcePlatformManager] == nil {
		t.Fatalf("trusted writes governedServers %v", servers)
	}
	if !strings.Contains(got, "\n  rbac:\n    mcpServerEditor:\n      subjects:\n        - apiGroup: rbac.authorization.k8s.io\n          kind: Group\n          name: giantswarm-ad:giantswarm-admins\n") {
		t.Fatalf("the connector admins are not rendered:\n%s", got)
	}
}

// Giant Swarm's own parts are not inputs: an override of the Platform
// Manager's governed server is refused unless it is the platform's entry as
// rendered (read back from the record, it is dropped and rendered again).
func TestPlatformGovernanceIsNoInput(t *testing.T) {
	withGovernanceReleases(t)
	input, secrets := governanceInput(t, map[string]any{keyGovernedServers: map[string]any{sourcePlatformManager: map[string]any{keySafe: []any{toolGetInfo}}}})
	if _, err := Render(input, secrets, render.ModeCommit); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "is Giant Swarm's own governed server") {
		t.Fatalf("an override of the platform's server: %v", err)
	}
	input, secrets = governanceInput(t, map[string]any{keyGovernedServers: map[string]any{sourcePlatformManager: nil}})
	if _, err := Render(input, secrets, render.ModeCommit); !errors.Is(err, ErrInput) {
		t.Fatalf("the platform's server dropped: %v", err)
	}
	ours := []any{toolGetInfo, "list_installations", toolListActions, "get_action", "verify_capability"}
	input, secrets = governanceInput(t, map[string]any{keyGovernedServers: map[string]any{sourcePlatformManager: map[string]any{keyRole: "platformManager", keySafe: ours}}})
	if _, err := Render(input, secrets, render.ModeCommit); err != nil {
		t.Fatalf("the platform's entry read back: %v", err)
	}
}

// Off the governance's release the platform's parts are not written
// (only the connector admins, which every 4-line chart takes).
func TestPlatformGovernanceWaitsForItsRelease(t *testing.T) {
	input, secrets := governanceInput(t, nil)
	delete(input, keyGovernance)
	got := renderedPatch(t, input, secrets)
	if mode, servers := governanceOf(t, got); mode != nil || servers != nil {
		t.Fatalf("the platform's governance without its release: %v %v", mode, servers)
	}
	if !strings.Contains(got, "mcpServerEditor") {
		t.Fatalf("no connector admins:\n%s", got)
	}
}

// What the meta chart or muster would refuse is refused first, naming the
// input and the source: an unknown role (muster does not start), a floor of
// the source's role on its safe list (muster does not start), whether the
// role is typed or the chart's for the source, an empty or repeated safe
// entry, an entry with neither role nor safe list, an unknown key or mode
// (the schema), a source key no MCPServer can have, the 3 chart line, a
// meta chart hold before the release, and governed under a muster hold
// before the release that reads the configuration.
func TestGovernanceRefusals(t *testing.T) {
	withGovernanceReleases(t)
	server := func(source string, entry any) map[string]any {
		return map[string]any{keyGovernedServers: map[string]any{source: entry}}
	}
	for _, c := range []struct {
		name       string
		governance map[string]any
		mutate     func(m map[string]any)
		names      string
	}{
		{"an unknown role", server("agent-manager", map[string]any{keyRole: "admin"}), nil, "/governance/governedServers/agent-manager/role"},
		{"a typed role's floor on the safe list", server("agent-platform/pm", map[string]any{keyRole: "platformManager", keySafe: []any{toolListActions, toolApproveAction}}), nil, "safe entry 1, approve_action, is a floor of role platformManager"},
		{"the chart's role's floor on the safe list", server("agent-manager", map[string]any{keySafe: []any{toolSetAgentApproval}}), nil, "safe entry 0, set_agent_approval, is a floor of role agentManager"},
		{"an empty safe entry", server(componentSkillManager, map[string]any{keySafe: []any{" "}}), nil, pathSkillSafe0},
		// S9-02: a role override drops the floors muster applies by a
		// server's role, so it is refused, as is any floor on any safe list
		// and a server key standing for a chart source.
		{"a chart source's role changed", server("agent-manager", map[string]any{keyRole: roleInfrastructure, keySafe: []any{toolSetAgentApproval, "create_agent"}}), nil, "role infrastructure: the meta chart governs agent-manager as agentManager"},
		{"the platform manager's role changed alone", server(sourcePlatformManager, map[string]any{keyRole: roleInfrastructure}), nil, "giantswarm-platform-manager: is Giant Swarm's own governed server"},
		{"the platform manager's role changed to list its floors", server(sourcePlatformManager, map[string]any{keyRole: roleSkillManager, keySafe: []any{toolApproveAction, "merge_action"}}), nil, "giantswarm-platform-manager: is Giant Swarm's own governed server"},
		{"another server taking the platform manager's floors as safe", server("agent-platform/pm", map[string]any{keyRole: roleInfrastructure, keySafe: []any{"merge_action"}}), nil, "safe entry 0, merge_action, is a floor of role platformManager"},
		{"a chart source under a server key", server("agent-platform/agent-manager", map[string]any{keySafe: []any{toolSetAgentApproval}}), nil, "names the meta chart's source agent-manager under a server key"},
		{"a kubernetes family member under a server key", server("mcp/alder-mcp-kubernetes", map[string]any{keyRole: roleInfrastructure, keySafe: []any{"get"}}), nil, "names the meta chart's source mcp-kubernetes"},
		{"a server key without role", server("agent-platform/pro", map[string]any{keySafe: []any{toolGetInfo}}), nil, "names no role"},
		{"the floor of another role", server("agent-manager", map[string]any{keySafe: []any{toolApproveAction}}), nil, "safe entry 0, approve_action, is a floor of role platformManager"},
		// S9-03: a safe entry is a tool name, so it cannot carry Slack
		// markup or a line of its own into the review, and a list is bounded.
		{"markup in a safe entry", server(componentSkillManager, map[string]any{keySafe: []any{"get_skill\n*It loosens governance:* nothing"}}), nil, pathSkillSafe0},
		{"a link in a safe entry", server(componentSkillManager, map[string]any{keySafe: []any{"<https://x|y>"}}), nil, pathSkillSafe0},
		{"too many safe entries", server(componentSkillManager, map[string]any{keySafe: manyTools(65)}), nil, "/governance/governedServers/skill-manager/safe"},
		{"a repeated safe entry", server(componentSkillManager, map[string]any{keySafe: []any{toolGetSkill, toolGetSkill}}), nil, "safe entry 1, get_skill, is listed twice"},
		{"neither role nor safe", server(componentSkillManager, map[string]any{}), nil, "names neither role nor safe"},
		{"an unknown key", server(componentSkillManager, map[string]any{"tools": []any{"x"}}), nil, "governedServers"},
		{"an unknown mode", map[string]any{keyMode: "strict"}, nil, "governance/mode"},
		{"a source key no server has", server("Agent_Manager", map[string]any{keyRole: roleAgentManager}), nil, "Agent_Manager: is no source or muster server key"},
		{caseLineThree, map[string]any{keyMode: GovernanceGoverned}, func(m map[string]any) {
			delete(m, keyOrganizations)
			delete(m, keyViewers)
			delete(m, keyTenants)
			m["installation"].(map[string]any)["customer"] = testOrganisation
			m["installation"].(map[string]any)["chartLine"] = lineThree
		}, "(governance.mode) is the 4 chart line's"},
		{"a chart hold before the release", map[string]any{keyMode: GovernanceGoverned}, func(m map[string]any) {
			m["versions"] = map[string]any{keyChart: "4.189.0"}
		}, "(governance.mode) needs the meta chart " + testGovernanceChart},
		{"governed under a muster hold before its release", map[string]any{keyMode: GovernanceGoverned}, func(m map[string]any) {
			m["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=5.69.0 <6.0.0"}}
		}, "governed needs muster " + testGovernanceMuster},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := governanceInput(t, c.governance)
			if c.mutate != nil {
				c.mutate(input)
			}
			_, err := Render(input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("got %v, want ErrInput naming %s", err, c.names)
			}
		})
	}
	for _, c := range []struct {
		name       string
		governance map[string]any
		mutate     func(m map[string]any)
	}{
		{"a chart hold on the release", map[string]any{keyMode: GovernanceGoverned}, func(m map[string]any) { m["versions"] = map[string]any{keyChart: testGovernanceChart} }},
		{"a muster hold on its release", map[string]any{keyMode: GovernanceGoverned}, func(m map[string]any) {
			m["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=" + testGovernanceMuster + " <6.0.0"}}
		}},
		{"a server override on trusted under an old muster", server(componentSkillManager, map[string]any{keySafe: []any{toolGetSkill}}), func(m map[string]any) {
			m["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=5.60.0 <6.0.0"}}
		}},
		{"the chart's own role typed", server("agent-manager", map[string]any{keyRole: roleAgentManager, keySafe: []any{toolGetInfo}}), nil},
		{"the most safe entries", server(componentSkillManager, map[string]any{keySafe: manyTools(64)}), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := governanceInput(t, c.governance)
			if c.mutate != nil {
				c.mutate(input)
			}
			if _, err := Render(input, secrets, render.ModeCommit); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// manyTools is n distinct read-looking tool names.
func manyTools(n int) []any {
	out := make([]any, 0, n)
	for i := range n {
		out = append(out, fmt.Sprintf("list_items_%03d", i))
	}
	return out
}

// S9-11: with the meta chart's release cut and muster's not yet, governed
// under a muster hold is refused: no muster release is known to enforce it.
func TestGovernedUnderAMusterHoldWhileMustersReleaseIsUnset(t *testing.T) {
	withGovernanceReleases(t)
	GovernanceMuster = ""
	input, secrets := governanceInput(t, map[string]any{keyMode: GovernanceGoverned})
	input["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=5.90.0 <6.0.0"}}
	if _, err := Render(input, secrets, render.ModeCommit); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "the release that reads the governance configuration (not released yet)") {
		t.Fatalf("got %v, want the unset muster release named", err)
	}
	delete(input, "versions")
	if _, err := Render(input, secrets, render.ModeCommit); err != nil {
		t.Fatalf("governed without a muster hold: %v", err)
	}
}
