package plan

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The definition's platform patch as it renders the audiences it knows: the
// authenticator, the kagent UI's client, the portal's client and backstage,
// in the three lists.
const renderedPlatformPatch = `# Rendered by giantswarm-platform-manager, agent-platform definition. Do not edit by hand.
kagent:
  oauth2-proxy:
    extraArgs:
      oidc-extra-audience: dex-k8s-authenticator,kagent,portal-client,backstage # gitleaks:allow
muster:
  muster:
    oauth:
      server:
        existingSecret: muster-oauth
        trustedAudiences:
          - dex-k8s-authenticator
          - kagent
          - portal-client
          - backstage
agent-platform-mcps:
  agentgateway:
    jwt:
      extraProviders:
        - issuer: https://dex.glean.example.io
          audiences:
            - portal-client
            - backstage
          jwks:
            jwksPath: /keys
`

// The installation's patch on record: each list carries an id of its own
// beside the definition's, one of them twice, one shared between two lists;
// the edge carries a second provider of another issuer.
const currentPlatformPatch = `kagent:
  oauth2-proxy:
    extraArgs:
      oidc-extra-audience: dex-k8s-authenticator, kagent,extra-only, shared-id,extra-only # gitleaks:allow
muster:
  muster:
    oauth:
      server:
        trustedAudiences:
          - dex-k8s-authenticator
          # a local development client
          - local-dev-client
          - portal-client
          - shared-id
agent-platform-mcps:
  agentgateway:
    jwt:
      extraProviders:
        - issuer: https://other.example.io
          audiences: [foreign-only]
        - issuer: https://dex.glean.example.io
          audiences: [backstage, edge-only]
`

// sharedID is the id on record in two lists at once: kept in each.
const sharedID = "shared-id"

func TestKeepAudiencesKeepsEachListsOwnEntriesAfterTheRenders(t *testing.T) {
	got, kept, err := keepAudiences([]byte(renderedPlatformPatch), []byte(currentPlatformPatch))
	if err != nil {
		t.Fatal(err)
	}
	want := []Kept{{ListTrustedAudiences, "local-dev-client"}, {ListTrustedAudiences, sharedID}, {ListExtraAudience, "extra-only"}, {ListExtraAudience, sharedID}, {ListEdgeAudiences, "edge-only"}}
	if !slices.Equal(kept, want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	s := string(got)
	for _, frag := range []string{
		renderedHeader,
		"oidc-extra-audience: dex-k8s-authenticator,kagent,portal-client,backstage,extra-only,shared-id # gitleaks:allow\n",
		"trustedAudiences:\n          - dex-k8s-authenticator\n          - kagent\n          - portal-client\n          - backstage\n          # a local development client\n          - local-dev-client\n          - shared-id\n",
		"audiences:\n            - portal-client\n            - backstage\n            - edge-only\n",
		"existingSecret: muster-oauth",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("merged file lacks %q:\n%s", frag, s)
		}
	}
	// An id stays in its own list: the UI's extra audience is no trusted
	// audience of muster, the trusted audience no audience of the UI, another
	// issuer's audience no audience of the definition's provider.
	if strings.Count(s, "extra-only") != 1 || strings.Count(s, "local-dev-client") != 1 || strings.Count(s, "edge-only") != 1 || strings.Contains(s, "foreign-only") {
		t.Errorf("an id left its list:\n%s", s)
	}
}

func TestKeepAudiencesMergesAHandWrittenListOfExtraAudiencesAsASet(t *testing.T) {
	current := "kagent:\n  oauth2-proxy:\n    extraArgs:\n      oidc-extra-audience: [dex-k8s-authenticator, list-client, list-client]\n"
	got, kept, err := keepAudiences([]byte(renderedPlatformPatch), []byte(current))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept, []Kept{{ListExtraAudience, "list-client"}}) || !strings.Contains(string(got), "oidc-extra-audience: dex-k8s-authenticator,kagent,portal-client,backstage,list-client # gitleaks:allow\n") {
		t.Errorf("kept %v:\n%s", kept, got)
	}
}

func TestKeepAudiencesLeavesTheRenderAsItIsWithNothingToKeep(t *testing.T) {
	for name, current := range map[string]string{
		"the render's entries alone": "muster:\n  muster:\n    oauth:\n      server:\n        trustedAudiences: [backstage, portal-client]\nkagent:\n  oauth2-proxy:\n    extraArgs:\n      oidc-extra-audience: kagent,backstage\n",
		"no list on record":          "muster:\n  muster:\n    resources: {}\n",
		"a list that is no list":     "muster:\n  muster:\n    oauth:\n      server:\n        trustedAudiences: stale\n",
	} {
		t.Run(name, func(t *testing.T) {
			got, kept, err := keepAudiences([]byte(renderedPlatformPatch), []byte(current))
			if err != nil || kept != nil || string(got) != renderedPlatformPatch {
				t.Errorf("err %v, kept %v; content changed:\n%s", err, kept, got)
			}
		})
	}
}

// A list the render lacks keeps nothing: without kagent the UI's audiences
// on record do not bring the UI's section back.
func TestKeepAudiencesKeepsNothingIntoAListTheRenderLacks(t *testing.T) {
	rendered := "muster:\n  muster:\n    oauth:\n      server:\n        trustedAudiences:\n          - dex-k8s-authenticator\n"
	got, kept, err := keepAudiences([]byte(rendered), []byte(currentPlatformPatch))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept, []Kept{{ListTrustedAudiences, "local-dev-client"}, {ListTrustedAudiences, "portal-client"}, {ListTrustedAudiences, sharedID}}) || strings.Contains(string(got), "kagent") || strings.Contains(string(got), "edge-only") {
		t.Errorf("kept %v:\n%s", kept, got)
	}
}

func TestKeepAudiencesRefusesACurrentFileThatIsNoMapping(t *testing.T) {
	if _, _, err := keepAudiences([]byte(renderedPlatformPatch), []byte("- a\n")); !errors.Is(err, errNoMapping) {
		t.Errorf("err = %v, want errNoMapping", err)
	}
}

// The authenticator's trusted peers are the fourth list: a peer on record the
// definition does not render stays after the definition's, in the dex patch
// alone, while the client's other keys are the definition's whole.
func TestKeepDexPatchKeepsTheInstallationsOwnTrustedPeers(t *testing.T) {
	rendered := "oidc:\n  staticClients:\n    muster:\n      clientSecretRef: {name: dex-client-muster, key: secret}\n    dexK8SAuthenticator:\n      trustedPeers:\n        - portal-client\n        - backstage\n        - hub-token-exchange\n"
	current := "oidc:\n  staticClients:\n    dexK8SAuthenticator:\n      clientSecret: stale-inline-secret\n      trustedPeers:\n        - backstage\n        - peer-only\n        - portal-client\n        - peer-only\n"
	got, kept, err := keepDexPatch([]byte(rendered), []byte(current))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept, []Kept{{ListTrustedPeers, "peer-only"}}) {
		t.Fatalf("kept %v", kept)
	}
	s := string(got)
	if !strings.Contains(s, "trustedPeers:\n        - portal-client\n        - backstage\n        - hub-token-exchange\n        - peer-only\n") || strings.Contains(s, "stale-inline-secret") || strings.Count(s, "peer-only") != 1 {
		t.Errorf("merged patch:\n%s", s)
	}
	if got, kept, err := keepDexPatch([]byte(rendered), []byte("oidc:\n  staticClients:\n    dexK8SAuthenticator:\n      trustedPeers: [backstage, portal-client]\n")); err != nil || kept != nil || string(got) != rendered {
		t.Errorf("the render's peers alone keep nothing: %v, %v:\n%s", err, kept, got)
	}
}
