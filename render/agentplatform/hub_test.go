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

// A private target that runs the agent platform is also tunnelled to its
// kagent (probed at /ping) and its agentgateway: a RemoteApp each on the hub
// and a tunnel each among the hub's tunnelport entries; a public target and a
// private one without the platform get neither.
func TestPrivatePlatformTargetTunnels(t *testing.T) {
	input, secrets := loadInput(t, shapeHubPrivateTarget)
	result, err := Render(input, secrets)
	if err != nil {
		t.Fatal(err)
	}
	tree := result.Tree()
	remoteapps := string(tree["giantswarm/giantswarm-management-clusters/management-clusters/gopher/extras/agent-platform/tunnelport/remoteapps.yaml"])
	tunnels := string(tree["giantswarm/teleport-fleet/kubernetes/envs/prod/values.yaml"])
	for _, want := range []string{
		"  name: kagent-burrow\n", "  appName: kagent-burrow\n  port: 4180\n  tokenName: kagent-burrow-bot-token\n  probe:\n    path: /ping\n",
		"  name: agentgateway-burrow\n", "  appName: agentgateway-burrow\n  port: 8080\n  tokenName: agentgateway-burrow-bot-token\n",
	} {
		if !strings.Contains(remoteapps, want) {
			t.Errorf("remoteapps.yaml lacks %q:\n%s", want, remoteapps)
		}
	}
	for _, want := range []string{
		"    - name: kagent-burrow\n      appLabels:\n        app: kagent\n        cluster: burrow\n        customer: giantswarm\n      tokens:\n        - name: kagent-burrow-bot-token\n          consumer: gopher\n",
		"    - name: agentgateway-burrow\n      appLabels:\n        app: agentgateway\n        cluster: burrow\n        customer: giantswarm\n      tokens:\n        - name: agentgateway-burrow-bot-token\n          consumer: gopher\n",
	} {
		if !strings.Contains(tunnels, want) {
			t.Errorf("the tunnelport values lack %q:\n%s", want, tunnels)
		}
	}
	if strings.Contains(remoteapps, "marmot") || strings.Contains(tunnels, "marmot") {
		t.Errorf("the public target marmot is tunnelled")
	}
	if got := (Target{Installation: "x", Private: true}).tunnelledApps(); len(got) != 5 || got[4].name != "kubernetes" {
		t.Errorf("a private target without the platform: %+v", got)
	}
}
