package agentplatform

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The governance releases the tests take as released.
const (
	testGovernanceChart  = "4.190.0"
	testGovernanceMuster = "5.70.0"
	keyGovernance        = "governance"
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
		{"governed", map[string]any{"mode": GovernanceGoverned}, "(governance.mode) needs the meta chart release that renders muster's governance configuration, and none is released yet"},
		{"a server override", map[string]any{"governedServers": map[string]any{"giantswarm-platform-manager": map[string]any{"safe": []any{"list_actions"}}}}, "(governance.governedServers) needs the meta chart release"},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := governanceInput(t, c.governance)
			_, err := Render(input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("got %v, want ErrInput naming %s", err, c.names)
			}
		})
	}
	input, secrets := governanceInput(t, map[string]any{"mode": GovernanceTrusted})
	mode, servers := governanceOf(t, renderedPatch(t, input, secrets))
	if mode != nil || servers != nil {
		t.Fatalf("trusted writes governance %v, governedServers %v", mode, servers)
	}
}

// With the release cut, governed renders the meta chart's top-level
// governance and the overrides its governedServers by source, in key
// order, a dropped source as null, an entry's role and safe list as typed;
// what is rendered reads back as the same input and renders the same patch
// again (a reconcile keeps it). Trusted is the chart's default and is not
// written.
func TestGovernanceRendersAndSurvivesAReconcile(t *testing.T) {
	withGovernanceReleases(t)
	governance := map[string]any{"mode": GovernanceGoverned, "governedServers": map[string]any{
		"giantswarm-platform-manager": map[string]any{"safe": []any{"get_info", "list_actions"}},
		"mcp-kubernetes":              nil,
		"agent-platform/pro":          map[string]any{"role": "infrastructure", "safe": []any{}},
	}}
	input, secrets := governanceInput(t, governance)
	patch := renderedPatch(t, input, secrets)
	for _, want := range []string{
		"\ngovernance: governed\n",
		"\ngovernedServers:\n  agent-platform/pro:\n    role: infrastructure\n    safe: []\n  giantswarm-platform-manager:\n    safe:\n      - get_info\n      - list_actions\n  mcp-kubernetes: null\n",
		"Changed only through an Action a second person approves",
	} {
		if !strings.Contains(patch, want) {
			t.Fatalf("the patch carries no %q:\n%s", want, patch)
		}
	}
	mode, servers := governanceOf(t, patch)
	again, secrets := governanceInput(t, map[string]any{"mode": mode, "governedServers": servers})
	if got := renderedPatch(t, again, secrets); got != patch {
		t.Fatalf("read back and rendered again:\n%s\nwant\n%s", got, patch)
	}

	trusted, secrets := governanceInput(t, map[string]any{"mode": GovernanceTrusted})
	if mode, _ := governanceOf(t, renderedPatch(t, trusted, secrets)); mode != nil {
		t.Fatalf("trusted writes governance: %v", mode)
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
		return map[string]any{"governedServers": map[string]any{source: entry}}
	}
	for _, c := range []struct {
		name       string
		governance map[string]any
		mutate     func(m map[string]any)
		names      string
	}{
		{"an unknown role", server("agent-manager", map[string]any{"role": "admin"}), nil, "/governance/governedServers/agent-manager/role"},
		{"a typed role's floor on the safe list", server("agent-platform/pm", map[string]any{"role": "platformManager", "safe": []any{"list_actions", "approve_action"}}), nil, "safe entry 1, approve_action, is a floor of role platformManager"},
		{"the chart's role's floor on the safe list", server("agent-manager", map[string]any{"safe": []any{"set_agent_approval"}}), nil, "safe entry 0, set_agent_approval, is a floor of role agentManager"},
		{"an empty safe entry", server("skill-manager", map[string]any{"safe": []any{" "}}), nil, "safe entry 0 is empty"},
		{"a repeated safe entry", server("skill-manager", map[string]any{"safe": []any{"get_skill", "get_skill"}}), nil, "safe entry 1, get_skill, is listed twice"},
		{"neither role nor safe", server("skill-manager", map[string]any{}), nil, "names neither role nor safe"},
		{"an unknown key", server("skill-manager", map[string]any{"tools": []any{"x"}}), nil, "governedServers"},
		{"an unknown mode", map[string]any{"mode": "strict"}, nil, "governance/mode"},
		{"a source key no server has", server("Agent_Manager", map[string]any{"role": "agentManager"}), nil, "Agent_Manager: is no source or muster server key"},
		{"the 3 line", map[string]any{"mode": GovernanceGoverned}, func(m map[string]any) {
			delete(m, keyOrganizations)
			delete(m, keyViewers)
			delete(m, keyTenants)
			m["installation"].(map[string]any)["customer"] = testOrganisation
			m["installation"].(map[string]any)["chartLine"] = lineThree
		}, "(governance.mode) is the 4 chart line's"},
		{"a chart hold before the release", map[string]any{"mode": GovernanceGoverned}, func(m map[string]any) {
			m["versions"] = map[string]any{keyChart: "4.189.0"}
		}, "(governance.mode) needs the meta chart " + testGovernanceChart},
		{"governed under a muster hold before its release", map[string]any{"mode": GovernanceGoverned}, func(m map[string]any) {
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
		{"a hold on the release", map[string]any{"mode": GovernanceGoverned}, func(m map[string]any) { m["versions"] = map[string]any{keyChart: testGovernanceChart} }},
		{"a muster hold on its release", map[string]any{"mode": GovernanceGoverned}, func(m map[string]any) {
			m["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=" + testGovernanceMuster + " <6.0.0"}}
		}},
		{"a server override on trusted under an old muster", server("giantswarm-platform-manager", map[string]any{"safe": []any{"list_actions"}}), func(m map[string]any) {
			m["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=5.60.0 <6.0.0"}}
		}},
		{"the floor of another role", server("agent-manager", map[string]any{"safe": []any{"approve_action"}}), nil},
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
