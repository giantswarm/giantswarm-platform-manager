package agentplatform

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestDeferredTargetHasNoSecretOrConsumers(t *testing.T) {
	input, secrets := loadInput(t, shapeHubPrivateTarget)
	result, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	name := render.ExchangeSecretName(render.TokenExchangeClient("burrow", "gopher", true))
	filtered, err := result.WithoutPairs([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range filtered.Tree() {
		if strings.Contains(path, "burrow") || strings.Contains(string(content), "burrow") {
			t.Errorf("deferred target remains in %s", path)
		}
	}
	found := false
	for path, content := range filtered.Tree() {
		if strings.Contains(path, "marmot-token-exchange-credentials.yaml") {
			found = true
		}
		if strings.HasSuffix(path, "agent-platform/configmap-values.yaml.patch") && !strings.Contains(string(content), "marmot") {
			t.Errorf("selected target missing from %s", path)
		}
	}
	if !found {
		t.Fatal("selected pair is missing")
	}
	for path, content := range result.Tree() {
		if strings.HasSuffix(path, "agent-platform/configmap-values.yaml.patch") && !strings.Contains(string(content), "burrow") {
			t.Fatal("deferral mutated the original render")
		}
	}
}
