package clustermcpservers

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestDeferredHubHasNoDexClientOrSecret(t *testing.T) {
	result, err := Render(loadInput(t, "hub-target"), nil, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	client := render.TokenExchangeClient("osprey", "warren", false)
	filtered, err := result.WithoutPairs([]string{render.ExchangeSecretName(client)})
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range filtered.Tree() {
		if strings.Contains(path, client) || strings.Contains(string(content), client) {
			t.Errorf("deferred client remains in %s", path)
		}
	}
	for path, content := range filtered.Tree() {
		if strings.HasSuffix(path, "dex-app/configmap-values.yaml.patch") && !strings.Contains(string(content), render.TokenExchangeClient("osprey", "gopher", true)) {
			t.Errorf("selected hub missing from %s", path)
		}
	}
}
