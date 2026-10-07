package agentplatform

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The keys of the version holds and their reasons among the inputs.
const (
	keyVersions   = "versions"
	keyChart      = "chart"
	keyComponents = "components"
	keyReasons    = "reasons"
)

// TestVersionHolds renders the version holds as the inputs carry them: a hold
// read back from the record stays, a typed one moves it, an empty one lifts
// it, and the chart line's range stands.
func TestVersionHolds(t *testing.T) {
	for _, c := range []struct {
		name, chart, agentManager string
		wantChart                 string
	}{
		{"held on record", "4.114.1", "1.9.2", `value: "4.114.1"`},
		{"moved by a typed input", "4.115.0", "1.10.0", `value: "4.115.0"`},
		{"lifted by an empty input", "", "", `value: ">=4.0.0-0 <5.0.0-0"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapeGiantswarmSlackAppPub)
			input[keyVersions] = map[string]any{keyChart: c.chart, keyComponents: map[string]any{componentAgentManager: c.agentManager}}
			in, err := Parse(input)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			kustomization := string(r.Files["giantswarm/giantswarm-management-clusters"]["management-clusters/graveler/extras/agent-platform/kustomization.yaml"].Content)
			if !strings.Contains(kustomization, c.wantChart) || strings.Contains(kustomization, holdComment(keyChart, "")) != (c.chart != "") {
				t.Errorf("kustomization without %s or its hold comment:\n%s", c.wantChart, kustomization)
			}
			patch := string(render.MustYAML(in.configmapPatch()))
			held := "  agent-manager:\n    enabled: true\n    # " + holdComment("components.agent-manager", "") + "\n    versionRange: \"" + c.agentManager + "\"\n"
			if c.agentManager != "" && !strings.Contains(patch, held) {
				t.Errorf("configmap patch without the hold after the toggle:\n%s", patch)
			}
			if c.agentManager == "" && strings.Contains(patch, "versionRange") {
				t.Errorf("configmap patch with a hold none was given:\n%s", patch)
			}
		})
	}
}

// TestVersionHoldReasons renders a hold's reason as the comment above it, line
// by line, where the inputs carry one (read back from the record, or typed);
// a hold without one carries the generic comment naming its input, and a
// reason without a hold writes nothing.
func TestVersionHoldReasons(t *testing.T) {
	const chartReason = "Held on the running release until the next line lands\n(acme/platform#12); back to the range in that PR."
	const agentManagerReason = "Held on the running release:\n  the next release renders what the running line does not serve."
	input, secrets := loadInput(t, shapeGiantswarmSlackAppPub)
	input[keyVersions] = map[string]any{
		keyChart:      "4.114.1",
		keyComponents: map[string]any{componentAgentManager: "1.9.2", componentKlausGateway: "4.0.0"},
		keyReasons:    map[string]any{keyChart: chartReason, keyComponents: map[string]any{componentAgentManager: agentManagerReason, componentMuster: "Held, yet no hold is set."}},
	}
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	kustomization := string(r.Files["giantswarm/giantswarm-management-clusters"]["management-clusters/graveler/extras/agent-platform/kustomization.yaml"].Content)
	if want := "      # Held on the running release until the next line lands\n      # (acme/platform#12); back to the range in that PR.\n      - op: replace\n        path: /spec/ref/semver\n        value: \"4.114.1\"\n"; !strings.Contains(kustomization, want) || strings.Contains(kustomization, "Held by the input") {
		t.Errorf("kustomization without the chart hold's reason as its comment:\n%s", kustomization)
	}
	patch := string(render.MustYAML(in.configmapPatch()))
	if want := "  agent-manager:\n    enabled: true\n    # Held on the running release:\n    #   the next release renders what the running line does not serve.\n    versionRange: \"1.9.2\"\n"; !strings.Contains(patch, want) {
		t.Errorf("configmap patch without the agent-manager hold's reason as its comment:\n%s", patch)
	}
	if want := "  klaus-gateway:\n    enabled: true\n    # " + holdComment("components.klaus-gateway", "") + "\n    versionRange: \"4.0.0\"\n"; !strings.Contains(patch, want) {
		t.Errorf("configmap patch without the generic comment on the hold without a reason:\n%s", patch)
	}
	if strings.Contains(patch, "yet no hold") {
		t.Errorf("configmap patch with a reason for a hold that is not set:\n%s", patch)
	}
}
