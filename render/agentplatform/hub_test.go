package agentplatform

import (
	"strings"
	"testing"
)

// A hub's broker carries the GitHub grant target with the GitHub issuer, and
// its broker client is an audience of it; an installation that brokers for
// nobody renders no broker.
func TestHubBrokerGrantTarget(t *testing.T) {
	patch := func(shape, name string) string {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets)
		if err != nil {
			t.Fatal(err)
		}
		return string(result.Tree()["giantswarm/giantswarm-configs/installations/"+name+"/apps/agent-platform/configmap-values.yaml.patch"])
	}
	hub := patch(shapeHubPrivateTarget, "gopher")
	if !strings.Contains(hub, "            github:\n              grantIssuer: "+githubGrantIssuer+"\n") {
		t.Errorf("the hub's broker carries no github grant target:\n%s", hub)
	}
	if !strings.Contains(hub, "          clientAudiences:\n            broker-client-id-placeholder:\n              - github\n") {
		t.Errorf("github is not the broker client's audience:\n%s", hub)
	}
	if owned := patch(shapeGiantswarmOwned, "gopher"); strings.Contains(owned, "tokenExchangeBroker") || strings.Contains(owned, "grantIssuer") {
		t.Errorf("an installation without targets renders a broker:\n%s", owned)
	}
}
