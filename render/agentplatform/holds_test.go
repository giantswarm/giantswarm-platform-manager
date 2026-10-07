package agentplatform

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
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
			input["versions"] = map[string]any{"chart": c.chart, "components": map[string]any{componentAgentManager: c.agentManager}}
			in, err := Parse(input)
			if err != nil {
				t.Fatal(err)
			}
			r, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			kustomization := string(r.Files["giantswarm/giantswarm-management-clusters"]["management-clusters/graveler/extras/agent-platform/kustomization.yaml"].Content)
			if !strings.Contains(kustomization, c.wantChart) || strings.Contains(kustomization, holdComment("chart")) != (c.chart != "") {
				t.Errorf("kustomization without %s or its hold comment:\n%s", c.wantChart, kustomization)
			}
			patch := string(render.MustYAML(in.configmapPatch()))
			held := "  agent-manager:\n    enabled: true\n    # " + holdComment("components.agent-manager") + "\n    versionRange: \"" + c.agentManager + "\"\n"
			if c.agentManager != "" && !strings.Contains(patch, held) {
				t.Errorf("configmap patch without the hold after the toggle:\n%s", patch)
			}
			if c.agentManager == "" && strings.Contains(patch, "versionRange") {
				t.Errorf("configmap patch with a hold none was given:\n%s", patch)
			}
		})
	}
}
