package plan

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The definition's platform patch as it renders without the keys the fleet
// still carries by hand: no trusted issuers, no connector pin, no resources,
// no model pick, the policy's review callers only.
const renderedPlatformPatchLean = `# Rendered by giantswarm-platform-manager, agent-platform definition. Do not edit by hand.
kagent:
  providers:
    anthropic:
      apiKeySecretRef: kagent-anthropic-key
muster:
  muster:
    oauth:
      server:
        existingSecret: muster-oauth
        trustedAudiences:
          - dex-k8s-authenticator
klausGateway:
  reviews:
    enabled: true
    allowedCallers:
      - system:serviceaccount:agent-platform:giantswarm-repo-manager
`

// The installation's patch on record: every kept key of the definition with a
// value of its own, one of them commented, a review caller of another team's
// sweep, and the keep switch of a move the fleet has completed, which no entry
// keeps any more.
const currentPlatformPatchKept = `kagent:
  providers:
    anthropic:
      # the installation's own pick, until the fleet default is right for it
      model: claude-opus-5
  modelConfigs:
    - name: anthropic-opus-5
      provider: Anthropic
  oauth2ProxyIngress:
    additionalPeers:
      - app: teleport-kube-agent
muster:
  resources:
    limits:
      memory: 1Gi
  muster:
    oauth:
      server:
        dex:
          connectorId: giantswarm-ad
        trustedIssuers:
          - issuer: https://irsa.example.io
            allowedAudiences:
              - kagent
klausGateway:
  reviews:
    allowedCallers:
      - system:serviceaccount:agent-platform:giantswarm-repo-manager
      - system:serviceaccount:marge:marge-shield-sweep
agent-platform-mcps:
  defaults:
    keep: true
`

func TestKeepAudiencesCarriesTheKeptKeysFromTheRecord(t *testing.T) {
	out, kept, err := keepAudiences([]byte(renderedPlatformPatchLean), []byte(currentPlatformPatchKept))
	if err != nil {
		t.Fatal(err)
	}
	_, ren, err := mapping(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"muster.muster.oauth.server.trustedIssuers",
		"muster.muster.oauth.server.dex.connectorId",
		"muster.resources.limits.memory",
		"kagent.providers.anthropic.model",
		"kagent.modelConfigs",
		"kagent.oauth2ProxyIngress.additionalPeers",
	} {
		if at(ren, strings.Split(path, ".")...) == nil {
			t.Errorf("%s: not carried into the render:\n%s", path, out)
		}
	}
	if n := at(ren, "agent-platform-mcps"); n != nil {
		t.Errorf("agent-platform-mcps.defaults.keep: a key no entry keeps stays out of the render (drift), got %v", n)
	}
	if n := at(ren, "muster", "muster", "oauth", "server", "dex", "connectorId"); n == nil || n.Value != "giantswarm-ad" {
		t.Errorf("connectorId: the record's pin stands, got %v", n)
	}
	if n := at(ren, "kagent", "providers", "anthropic", "apiKeySecretRef"); n == nil || n.Value != "kagent-anthropic-key" {
		t.Errorf("the render's own leaf under a kept path's parent is untouched, got %v", n)
	}
	if !strings.Contains(string(out), "# the installation's own pick") {
		t.Errorf("the record's comment goes with the kept key:\n%s", out)
	}
	callers := at(ren, "klausGateway", "reviews", "allowedCallers")
	if callers == nil || len(callers.Content) != 2 || callers.Content[1].Value != "system:serviceaccount:marge:marge-shield-sweep" {
		t.Errorf("allowedCallers: the other team's sweep is kept after the policy's, got %v", callers)
	}
	const kagentKey, musterKey = "kagent", "muster"
	want := []Kept{
		{List: ListAllowedCallers, Entry: "system:serviceaccount:marge:marge-shield-sweep"},
		{List: "kagent.providers.anthropic", Entry: "model"},
		{List: kagentKey, Entry: "modelConfigs"},
		{List: kagentKey, Entry: "oauth2ProxyIngress"},
		{List: musterKey, Entry: "resources"},
		{List: "muster.muster.oauth.server.dex", Entry: "connectorId"},
		{List: "muster.muster.oauth.server", Entry: "trustedIssuers"},
	}
	for _, w := range want {
		if !slices.Contains(kept, w) {
			t.Errorf("kept lacks %+v; kept: %+v", w, kept)
		}
	}
	if slices.Contains(kept, Kept{List: "agent-platform-mcps", Entry: "defaults"}) {
		t.Errorf("agent-platform-mcps.defaults is nobody's kept key; kept: %+v", kept)
	}
}

func TestKeepAudiencesLeavesTheRenderWithNothingOnRecord(t *testing.T) {
	out, kept, err := keepAudiences([]byte(renderedPlatformPatchLean), []byte("kagent:\n  providers:\n    anthropic:\n      apiKeySecretRef: other\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != renderedPlatformPatchLean || len(kept) != 0 {
		t.Errorf("nothing to keep: the render stands byte for byte, got kept %+v:\n%s", kept, out)
	}
}

func TestKeepSubtreesLeavesTheRendersOwnKeyAndAPathThroughAScalar(t *testing.T) {
	_, ren, err := mapping([]byte("kagent:\n  providers:\n    anthropic:\n      apiKeySecretRef: kagent-anthropic-key\nmuster:\n  resources: none\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, cur, err := mapping([]byte("kagent:\n  providers:\n    anthropic:\n      apiKeySecretRef: other\nmuster:\n  resources:\n    limits:\n      memory: 1Gi\n"))
	if err != nil {
		t.Fatal(err)
	}
	var kept []Kept
	keepSubtrees(ren, cur, []string{"kagent.providers.anthropic.apiKeySecretRef", "muster.resources.limits"}, &kept)
	if n := at(ren, "kagent", "providers", "anthropic", "apiKeySecretRef"); n.Value != "kagent-anthropic-key" {
		t.Errorf("a key the render carries is the definition's, got %q", n.Value)
	}
	if n := at(ren, "muster", "resources"); n.Kind != yaml.ScalarNode || len(kept) != 0 {
		t.Errorf("a path through a scalar has no place: nothing copied, got %v kept %+v", n, kept)
	}
}

func TestKeptPlatformKeysReadTheDefinition(t *testing.T) {
	for _, want := range []string{"muster.muster.oauth.server.trustedIssuers", "kagent.oauth2ProxyIngress", "muster.muster.oauth.server.dex.connectorId"} {
		if !slices.Contains(keptPlatformKeys, want) {
			t.Errorf("the agent-platform definition keeps %s; kept keys: %v", want, keptPlatformKeys)
		}
	}
	if slices.Contains(keptPlatformKeys, "agent-platform-mcps.defaults") {
		t.Errorf("agent-platform-mcps.defaults left with the M19 entry; kept keys: %v", keptPlatformKeys)
	}
	for _, k := range keptPlatformKeys {
		if strings.Contains(k, ":") || strings.Contains(k, "[") || strings.Contains(k, "<") {
			t.Errorf("a kept key is a plain path of mapping keys, got %q", k)
		}
	}
}
