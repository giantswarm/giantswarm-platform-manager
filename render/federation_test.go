package render

import (
	"strings"
	"testing"
)

// The two sides of a federation login share one generated value: the hub's
// Dex-side Secret of the client for the target and the target's connector
// Secret, each naming the other's file as its peer, so a plan never draws
// the secret on one side alone.
func TestFederationSecretsPeerEachOther(t *testing.T) {
	hubFile, hub := HubFederationSecret("gopher", "plover", "giantswarm-simple-oidc")
	targetFile, target := ConnectorSecret("giantswarm-simple-oidc", "gopher", "plover")
	if hubFile != "dex-client-federation-plover-secret.yaml" || targetFile != "dex-connector-giantswarm-simple-oidc-secret.yaml" {
		t.Fatalf("the files: %s, %s", hubFile, targetFile)
	}
	if len(hub.Generated) != 1 || len(target.Generated) != 1 {
		t.Fatalf("one generated value on each side: %+v %+v", hub.Generated, target.Generated)
	}
	h, g := hub.Generated[0], target.Generated[0]
	if h.Name != g.Name || h.Name != "federation-gopher-plover-client-secret" {
		t.Errorf("the shared value: %s, %s", h.Name, g.Name)
	}
	if h.Peer == nil || *h.Peer != (Peer{Installation: "plover", Path: PlatformSecretsPath("plover", targetFile)}) {
		t.Errorf("the hub side's peer: %+v", h.Peer)
	}
	if g.Peer == nil || *g.Peer != (Peer{Installation: "gopher", Path: PlatformSecretsPath("gopher", hubFile)}) {
		t.Errorf("the target side's peer: %+v", g.Peer)
	}
	content := string(target.Content)
	for _, want := range []string{"name: dex-connector-giantswarm-simple-oidc", "namespace: giantswarm", "client-id: federation-plover"} {
		if !strings.Contains(content, want) {
			t.Errorf("the connector Secret lacks %q:\n%s", want, content)
		}
	}
}

// The hub's client for a target redirects to the target's Dex callback alone
// and reads its secret from a referenced Secret; the target's Dex reads the
// client's id and secret from its environment, projected from the
// connector Secret, optional so a Dex without the Secret yet still starts.
func TestFederationClientAndEnvironment(t *testing.T) {
	want := "id: federation-plover\nname: plover federation login\nredirectURIs:\n  - https://dex.plover.oakridge.example/callback\nsecretRef:\n  name: dex-client-federation-plover\n  key: secret\n"
	if got := string(MustYAML(HubFederationClient("plover", "plover.oakridge.example"))); got != want {
		t.Errorf("the hub's client:\n%s\nwant:\n%s", got, want)
	}
	env := string(MustYAML(ConnectorEnvVars("giantswarm-simple-oidc")))
	for _, want := range []string{
		"name: DEX_CONNECTOR_GIANTSWARM_SIMPLE_OIDC_CLIENT_ID", "key: client-id",
		"name: DEX_CONNECTOR_GIANTSWARM_SIMPLE_OIDC_CLIENT_SECRET", "key: client-secret",
		"name: dex-connector-giantswarm-simple-oidc", "optional: true",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("the environment lacks %q:\n%s", want, env)
		}
	}
}
