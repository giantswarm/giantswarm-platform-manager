package agentplatform

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// keyOrganizations is the organizations' choices in the input document.
const keyOrganizations = "organizations"

// TestOrganizationsRefusals refuses the organizations' choices an
// installation cannot serve, naming the input: the switch off the 4 chart
// line, where no kagent runs or under a meta chart hold before the
// organizations' release; commit mode under a hold before the release that
// takes agent-manager.tenants.commit; the portal's pages where no portal
// carries the installation's section.
func TestOrganizationsRefusals(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(m map[string]any)
		names  string
	}{
		{"the switch on the 3 line", func(m map[string]any) {
			// An organisation the policy runs on no line of its own, so the record's 3 line stands.
			m["installation"].(map[string]any)["customer"] = testOrganisation
			m["installation"].(map[string]any)["chartLine"] = lineThree
		}, "organizations.enabled is the 4 chart line's"},
		{"the switch where no kagent runs", func(m map[string]any) { m["installation"].(map[string]any)["customer"] = customerWithoutKagent }, "runs no kagent"},
		{"the switch under a chart hold before the organizations", func(m map[string]any) {
			m["versions"] = map[string]any{keyChart: "4.176.5"}
		}, "versions.chart"},
		{"commit mode under a chart hold before the release that takes it", func(m map[string]any) {
			m[keyOrganizations].(map[string]any)["commit"] = map[string]any{keyEnabled: true}
			m["versions"] = map[string]any{keyChart: "4.177.0-rc.1"}
		}, "organizations.commit.enabled"},
		{"the portal's pages without a portal section", func(m map[string]any) {
			m[keyOrganizations] = map[string]any{"portal": map[string]any{keyEnabled: true}}
		}, "organizations.portal.enabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapeOrganizationsApply)
			c.mutate(input)
			_, err := Render(input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("got %v, want ErrInput naming %s", err, c.names)
			}
		})
	}
	// The API hostnames and commit mode read back without the switch render
	// nothing: a person turns the switch off alone.
	input, secrets := loadInput(t, shapeOrganizationsCommit)
	input[keyOrganizations].(map[string]any)[keyEnabled] = false
	input[keyOrganizations].(map[string]any)["apiHostnames"] = []any{"internal-api.garnet.example.io"}
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if patch := string(render.MustYAML(in.configmapPatch())); strings.Contains(patch, "agentPlatformOrganizations") || strings.Contains(patch, "tenants") || strings.Contains(patch, "agent-platform-operator") {
		t.Errorf("the switch off renders organizations' values:\n%s", patch)
	}
	if _, err := Render(input, secrets, render.ModeCommit); err != nil {
		t.Fatal(err)
	}
	// Commit mode under a hold on the release that takes it, or a range that
	// reaches it, stands. The release is written out: the constant would
	// pass with any value.
	for _, hold := range []string{"4.177.0", ">=4.170.0 <5.0.0"} {
		input, secrets := loadInput(t, shapeOrganizationsCommit)
		input["versions"] = map[string]any{keyChart: hold}
		if _, err := Render(input, secrets, render.ModeCommit); err != nil {
			t.Errorf("commit mode, hold %s: %v", hold, err)
		}
	}
	// A hold on the organizations' release or a range that reaches it stands.
	for _, hold := range []string{organizationsChart, ">=4.170.0 <5.0.0"} {
		input, secrets := loadInput(t, shapeOrganizationsApply)
		input["versions"] = map[string]any{keyChart: hold}
		if _, err := Render(input, secrets, render.ModeCommit); err != nil {
			t.Errorf("hold %s: %v", hold, err)
		}
	}
}

// TestOrganizationsPortalPagesStayWithoutTheSwitch keeps the portal's
// Organizations pages where the person's choice, read back from the
// fragment on record, carries them and the switch is off: a plan of another
// installation of the same portal keeps what the plan that turned them on
// wrote. Off both, the fragment carries no key.
func TestOrganizationsPortalPagesStayWithoutTheSwitch(t *testing.T) {
	for _, c := range []struct {
		name   string
		portal bool
		want   bool
	}{{"the choice on, the switch off", true, true}, {"neither on", false, false}} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapeOrganizationsPortal)
			input[keyOrganizations] = map[string]any{"portal": map[string]any{keyEnabled: c.portal}}
			in, err := Parse(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Render(input, secrets, render.ModeCommit); err != nil {
				t.Fatal(err)
			}
			var fragment struct {
				AgentPlatform struct {
					Organizations *struct {
						Enabled bool `yaml:"enabled"`
					} `yaml:"organizations"`
				} `yaml:"agentPlatform"`
			}
			if err := yaml.Unmarshal(render.MustYAML(in.portalAppConfig()), &fragment); err != nil {
				t.Fatal(err)
			}
			got := fragment.AgentPlatform.Organizations != nil && fragment.AgentPlatform.Organizations.Enabled
			if got != c.want {
				t.Errorf("agentPlatform.organizations.enabled %v, want %v", got, c.want)
			}
		})
	}
}

// TestPolicyDefaultAgent admits the chat gateway's default agent by its
// name in kagent or as <organization namespace>/<name>, the form
// klaus-gateway's --a2a-default-agent takes, and refuses another namespace
// or a name Kubernetes would not take, per installation and fleet-wide.
func TestPolicyDefaultAgent(t *testing.T) {
	const base = "flux: {sourceInterval: 1m}\n"
	for agent, admitted := range map[string]bool{
		"sre-agent":                    true,
		"kagent/sre-agent":             true,
		"agents-platform-x7k2p/triage": true,
		"agents-team-abcde/sre.agent":  true,
		"sre.agent":                    true,
		"agents-team.abcde/triage":     false,
		"agents-team-abcde/-triage":    false,
		"default/sre-agent":            false,
		"agents-platform-x7k2p/":       false,
		"Agents-Platform/triage":       false,
		"a/b/c":                        false,
	} {
		for _, doc := range []string{
			"klausGateway: {installations: {rowan: {defaultAgent: \"" + agent + "\"}}}\n",
			"klausGateway: {a2a: {defaultAgent: \"" + agent + "\"}}\n",
		} {
			_, err := parsePolicy([]byte(base + doc))
			if admitted && err != nil {
				t.Errorf("%s: %v", doc, err)
			}
			if !admitted && !errors.Is(err, ErrPolicy) {
				t.Errorf("%s: got %v, want ErrPolicy", doc, err)
			}
		}
	}
}

// TestPolicyOrganizationsBlock refuses an organizations block with a value
// commit mode writes left empty, a misspelt key among them; the fleet's own
// policy carries every one.
func TestPolicyOrganizationsBlock(t *testing.T) {
	const base = "flux: {sourceInterval: 1m}\n"
	if _, err := parsePolicy([]byte(base + "organizations: {branch: main, installationKustomization: orgs, flux: {namespace: flux-giantswarm, source: {kind: GitRepository, name: fleet}, decryptionsecret: sops-keys}}\n")); !errors.Is(err, ErrPolicy) || !strings.Contains(err.Error(), "organizations.flux.decryptionSecret") {
		t.Errorf("a misspelt key: got %v, want ErrPolicy naming it", err)
	}
	if _, err := loadPolicy(); err != nil {
		t.Errorf("the fleet's policy: %v", err)
	}
}
