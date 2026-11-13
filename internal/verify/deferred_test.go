package verify

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestRolloutRenderUsesTheActionsDeferredPairs(t *testing.T) {
	content, err := os.ReadFile("../../render/agentplatform/testdata/hub-private-target/input.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Input map[string]any `yaml:"input"`
	}
	if err := yaml.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	definition, ok := installations.FindCapability("agent-platform")
	if !ok {
		t.Fatal("no definition")
	}
	name := render.ExchangeSecretName(render.TokenExchangeClient("burrow", "gopher", true))
	filtered, err := renderLive(LiveOptions{Definition: definition, Inputs: Inputs{Values: fixture.Input}, DeferredPairs: []string{name}})
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range filtered.values {
		if strings.Contains(path, "burrow") || strings.Contains(value, "burrow") {
			t.Errorf("rollout expects deferred target at %s", path)
		}
	}
	complete, err := renderLive(LiveOptions{Definition: definition, Inputs: Inputs{Values: fixture.Input}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for path := range complete.values {
		if strings.Contains(path, "burrow") {
			found = true
		}
	}
	if !found {
		t.Fatal("full verification omitted the deferred target")
	}
}
