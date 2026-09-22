package agentplatform

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The registry's hub's broker carries the GitHub grant target with the GitHub
// issuer, and its broker client is an audience of it; a customer aggregator
// brokers for its siblings without the grant target, and an installation that
// brokers for nobody renders no broker.
func TestHubBrokerGrantTarget(t *testing.T) {
	patch := func(shape, repo, name string) string {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		p := "giantswarm/" + repo + "-configs/installations/" + name + "/apps/agent-platform/configmap-values.yaml.patch"
		content, ok := result.Tree()[p]
		if !ok {
			t.Fatalf("%s: no %s", shape, p)
		}
		return string(content)
	}
	hub := patch(shapeHubPrivateTarget, "giantswarm", "gopher")
	if !strings.Contains(hub, "            github:\n              grantIssuer: "+githubGrantIssuer+"\n") {
		t.Errorf("the hub's broker carries no github grant target:\n%s", hub)
	}
	if !strings.Contains(hub, "          clientAudiences:\n            broker-client-id-placeholder:\n              - github\n") {
		t.Errorf("github is not the broker client's audience:\n%s", hub)
	}
	if owned := patch(shapeGiantswarmOwned, "giantswarm", "gopher"); strings.Contains(owned, "tokenExchangeBroker") || strings.Contains(owned, "grantIssuer") {
		t.Errorf("an installation without targets renders a broker:\n%s", owned)
	}
	if aggregator := patch(shapeMultiClusterAggregator, "oakridge", "heron"); !strings.Contains(aggregator, "tokenExchangeBroker") || strings.Contains(aggregator, "github") {
		t.Errorf("a customer aggregator's broker carries the github grant target:\n%s", aggregator)
	}
}

// A private target whose agent platform the hub's portal proxies is also
// tunnelled to its kagent (probed at /ping) and its agentgateway: a RemoteApp
// each on the hub and a tunnel each among the hub's tunnelport entries; a
// public target and a private one the portal does not proxy get neither.
func TestPrivatePlatformTargetTunnels(t *testing.T) {
	input, secrets := loadInput(t, shapeHubPrivateTarget)
	result, err := Render(input, secrets, render.ModeCommit)
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
		t.Errorf("a private target the portal does not proxy: %+v", got)
	}
}

// A target's Dex registers one connector per hub that brokers into it, and
// only one of an organisation's hubs carries the organisation's plain name:
// the target's first hub (the registry's, where it is one) names
// <customer>-simple-oidc in its broker and identity provider, every further
// hub of the organisation its own <customer>-<hub>-oidc; a target this hub
// alone brokers into — its hubs naming this hub only, or none — names the
// plain one. A target whose hubs do not name this hub is refused.
func TestConnectorPerHubAndTarget(t *testing.T) {
	patch := func(shape, repo, name string) string {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		return string(result.Tree()["giantswarm/"+repo+"-configs/installations/"+name+"/apps/agent-platform/configmap-values.yaml.patch"])
	}
	second := patch(shapeSecondHub, "giantswarm", "warren")
	for _, want := range []string{
		"            marmot:\n              dexTokenEndpoint: https://dex.marmot.example.io/token\n              connectorId: giantswarm-warren-oidc\n",
		"            vole:\n              dexTokenEndpoint: https://dex.vole.example.io/token\n              connectorId: giantswarm-simple-oidc\n",
		"    marmot:\n      tokenEndpoint: https://dex.marmot.example.io/token\n      connectorId: giantswarm-warren-oidc\n",
		"    vole:\n      tokenEndpoint: https://dex.vole.example.io/token\n      connectorId: giantswarm-simple-oidc\n",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("the second hub's patch lacks %q:\n%s", want, second)
		}
	}
	if first := patch(shapeHubPrivateTarget, "giantswarm", "gopher"); strings.Count(first, "connectorId: giantswarm-simple-oidc\n") != 4 || strings.Contains(first, "gopher-oidc") {
		t.Errorf("the first hub names the plain connector for both targets, twice each:\n%s", first)
	}
	// The aggregator's record with a target's hubs edited: none names this hub alone.
	aggregator := func(hubs any) map[string]any {
		input, _ := loadInput(t, shapeMultiClusterAggregator)
		target := input["installation"].(map[string]any)["federation"].(map[string]any)["targets"].([]any)[0].(map[string]any)
		if hubs == nil {
			delete(target, "hubs")
		} else {
			target["hubs"] = hubs
		}
		return input
	}
	in, err := Parse(aggregator(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := in.connector(in.Installation.Federation.Targets[0]); got != "oakridge-simple-oidc" {
		t.Errorf("a target naming no hubs: %s", got)
	}
	if in.Connectors.Further != "oakridge-heron-oidc" {
		t.Errorf("the further hub's connector: %s", in.Connectors.Further)
	}
	_, err = Parse(aggregator([]any{"plover"}))
	if err == nil || !strings.Contains(err.Error(), "targets[0].hubs") || !strings.Contains(err.Error(), "heron is not among them") {
		t.Errorf("a target whose hubs do not name this hub: %v", err)
	}
}

// A tunnel is shared by every hub that brokers into its target, each hub with
// a token of its own in teleport-fleet's values, so a token is named for the
// hub the way the connector is: the target's first hub's is
// <app>-<target>-bot-token, every further hub's carries its name — in the
// values entries and in the RemoteApp that names the token alike. warren is
// marmot's second hub after gopher; gopher is burrow's first.
func TestTunnelTokensPerHub(t *testing.T) {
	input, secrets := loadInput(t, shapeSecondHub)
	marmot := input["installation"].(map[string]any)["federation"].(map[string]any)["targets"].([]any)[0].(map[string]any)
	marmot["private"] = true
	result, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	tree := result.Tree()
	remoteapps := string(tree["giantswarm/giantswarm-management-clusters/management-clusters/warren/extras/agent-platform/tunnelport/remoteapps.yaml"])
	tunnels := string(tree["giantswarm/teleport-fleet/kubernetes/envs/prod/values.yaml"])
	for _, want := range []string{
		"  appName: dex-marmot\n  port: 5556\n  tokenName: dex-marmot-bot-token-warren\n",
		"  appName: kubernetes-marmot\n  port: 6443\n  tokenName: kubernetes-marmot-bot-token-warren\n",
	} {
		if !strings.Contains(remoteapps, want) {
			t.Errorf("the second hub's remoteapps.yaml lacks %q:\n%s", want, remoteapps)
		}
	}
	for _, want := range []string{
		"    - name: dex-marmot\n      appLabels:\n        app: dex\n        cluster: marmot\n        customer: giantswarm\n      tokens:\n        - name: dex-marmot-bot-token-warren\n          consumer: warren\n",
		"    - name: kubernetes-marmot\n      appLabels:\n        app: kubernetes\n        cluster: marmot\n        customer: giantswarm\n      tokens:\n        - name: kubernetes-marmot-bot-token-warren\n          consumer: warren\n",
	} {
		if !strings.Contains(tunnels, want) {
			t.Errorf("the second hub's tunnelport values lack %q:\n%s", want, tunnels)
		}
	}
	if strings.Contains(remoteapps, "-bot-token\n") || strings.Contains(tunnels, "-bot-token\n") {
		t.Errorf("the second hub names an unsuffixed token:\n%s\n%s", remoteapps, tunnels)
	}
	// The first hub's tokens are unsuffixed (TestPrivatePlatformTargetTunnels holds gopher's fileset).
	for _, tc := range []struct {
		hubs      []string
		hub, want string
	}{
		{nil, "gopher", "dex-burrow-bot-token"},
		{[]string{"gopher"}, "gopher", "dex-burrow-bot-token"},
		{[]string{"gopher", "warren"}, "gopher", "dex-burrow-bot-token"},
		{[]string{"gopher", "warren"}, "warren", "dex-burrow-bot-token-warren"},
	} {
		if got := (Target{Installation: "burrow", Hubs: tc.hubs}).tokenName("dex", tc.hub); got != tc.want {
			t.Errorf("hubs %v, hub %s: tokenName = %s, want %s", tc.hubs, tc.hub, got, tc.want)
		}
	}
}
