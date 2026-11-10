package plan

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

const (
	testAgentManagerApp = "agent-manager"
)

func TestCredentialProviderAppsAreTheRenders(t *testing.T) {
	const rendered = "muster:\n  muster:\n    providerApps:\n      - name: agent-manager\n        issuer: https://github.com/apps/giantswarm-agent-manager\n"
	const current = "muster:\n  muster:\n    providerApps:\n      - name: chatgpt\n        flow: chatgptDeviceCode\n"
	merged, _, err := keepAudiences([]byte(rendered), []byte(current))
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, yaml.Unmarshal(merged, &values))
	apps := values["muster"].(map[string]any)["muster"].(map[string]any)["providerApps"].([]any)
	require.Equal(t, []any{map[string]any{keyName: testAgentManagerApp, keyIssuer: "https://github.com/apps/giantswarm-agent-manager"}}, apps)
}

func TestCredentialBrokerIssuerAfterReadback(t *testing.T) {
	const authenticatorAudience = "dex-k8s-authenticator"
	const profile = "agent-manager:\n  githubTokenExchange:\n    endpoint: https://muster.example.com/oauth/token\nmuster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n          - issuer: https://dex.example.com\n            allowedAudiences: [backstage, dex-k8s-authenticator]\n"
	issuers := func(entries string) string {
		return "muster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n" + entries
	}
	for _, test := range []struct {
		name      string
		current   string
		audiences []string
		refused   bool
	}{
		{"record without issuers", "{}", []string{render.PortalDexClientID, authenticatorAudience}, false},
		{"record gains the exchange's audiences", issuers("          - issuer: https://dex.example.com\n            allowedAudiences: [backstage]\n            subjectClaim: email\n"), []string{render.PortalDexClientID, authenticatorAudience}, false},
		{"record keeps its own audiences", issuers("          - issuer: https://dex.example.com\n            allowedAudiences: [local-portal, backstage]\n"), []string{"local-portal", render.PortalDexClientID, authenticatorAudience}, false},
		{"independent issuer", issuers("          - issuer: https://other.example.com\n            allowedAudiences: [backstage]\n"), nil, true},
		{"missing audience allowlist", issuers("          - issuer: https://dex.example.com\n"), nil, true},
		{"empty audience allowlist", issuers("          - issuer: https://dex.example.com\n            allowedAudiences: []\n"), nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			merged, _, err := keepAudiences([]byte(profile), []byte(test.current))
			if test.refused {
				require.ErrorContains(t, err, "one canonical Dex")
				return
			}
			require.NoError(t, err)
			_, values, err := mapping(merged)
			require.NoError(t, err)
			var entries []struct {
				AllowedAudiences []string `yaml:"allowedAudiences"`
			}
			require.NoError(t, at(values, "muster", "muster", "oauth", "server", "trustedIssuers").Decode(&entries))
			require.Len(t, entries, 1)
			require.Equal(t, test.audiences, entries[0].AllowedAudiences)
			again, _, err := keepAudiences([]byte(profile), merged)
			require.NoError(t, err)
			require.Equal(t, merged, again)
		})
	}
	_, _, err := keepAudiences([]byte("{}"), []byte("{}"))
	require.NoError(t, err)
}

func TestCredentialBrokerIssuerKeepsTheRecordWhenOff(t *testing.T) {
	const portalOnly = "muster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n          - issuer: https://dex.example.com\n            allowedAudiences: [backstage]\n"
	const current = "muster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n          - issuer: https://dex.example.com\n            allowedAudiences: [backstage, " + agentplatform.ManagerExchangeSubjectAudience + "]\n"
	merged, _, err := keepAudiences([]byte(portalOnly), []byte(current))
	require.NoError(t, err)
	_, values, err := mapping(merged)
	require.NoError(t, err)
	var entries []struct {
		Issuer           string   `yaml:"issuer"`
		AllowedAudiences []string `yaml:"allowedAudiences"`
	}
	require.NoError(t, at(values, "muster", "muster", "oauth", "server", "trustedIssuers").Decode(&entries))
	require.Equal(t, []string{render.PortalDexClientID, agentplatform.ManagerExchangeSubjectAudience}, entries[0].AllowedAudiences)
}
