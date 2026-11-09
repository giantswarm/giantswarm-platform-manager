package plan

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

func TestCredentialProfilePreservesOtherProviderApps(t *testing.T) {
	const rendered = `agent-manager:
  githubTokenExchange:
    endpoint: https://muster.example.com/oauth/token
muster:
  muster:
    providerApps:
      - name: agent-manager
        issuer: https://github.com/apps/giantswarm-agent-manager
        clientCredentialsSecretRef:
          name: giantswarm-agent-manager-oauth-client
    oauth:
      server:
        trustedIssuers:
          - issuer: https://dex.example.com
            allowedAudiences: [backstage]
`
	const current = `muster:
  muster:
    providerApps:
      - name: chatgpt
        flow: chatgptDeviceCode
`
	merged, _, err := keepAudiences([]byte(rendered), []byte(current))
	require.NoError(t, err)
	var values map[string]any
	require.NoError(t, yaml.Unmarshal(merged, &values))
	apps := values["muster"].(map[string]any)["muster"].(map[string]any)["providerApps"].([]any)
	require.Len(t, apps, 2)
	require.Equal(t, map[string]any{keyName: "chatgpt", "flow": "chatgptDeviceCode"}, apps[1])
	refreshed, _, err := keepAudiences([]byte(rendered), merged)
	require.NoError(t, err)
	require.Equal(t, merged, refreshed)
}

func TestCredentialProviderAppReconciliation(t *testing.T) {
	const enabled = `muster:
  muster:
    providerApps:
      - name: agent-manager
        issuer: https://github.com/apps/giantswarm-agent-manager
        clientCredentialsSecretRef:
          name: giantswarm-agent-manager-oauth-client
`
	manager := map[string]any{keyName: agentplatform.ManagerProviderAppName, keyIssuer: agentplatform.ManagerGrantIssuer, "clientCredentialsSecretRef": map[string]any{keyName: "giantswarm-agent-manager-oauth-client"}}
	chatgpt := map[string]any{keyName: "chatgpt", "flow": "chatgptDeviceCode"}
	other := map[string]any{keyName: "other", keyIssuer: "https://other.example.com", "tokenEndpoint": "https://other.example.com/token", "clientCredentialsSecretRef": map[string]any{keyName: "other-client"}} // #nosec G101 -- public endpoints and Secret references only
	for _, test := range []struct {
		name     string
		rendered string
		current  string
		want     []map[string]any
		refusal  string
	}{
		{"disable keeps other apps", "{}", "[{name: agent-manager, issuer: https://github.com/apps/giantswarm-agent-manager}, {name: chatgpt, flow: chatgptDeviceCode}]", []map[string]any{chatgpt}, ""},
		{"disable removes managed app", "{}", "[{name: agent-manager, issuer: https://github.com/apps/giantswarm-agent-manager}]", nil, ""},
		{"managed settings update", enabled, "[{name: agent-manager, issuer: https://github.com/apps/giantswarm-agent-manager, clientCredentialsSecretRef: {name: previous-client}}, {name: chatgpt, flow: chatgptDeviceCode}]", []map[string]any{manager, chatgpt}, ""},
		{"other OAuth app stays whole", enabled, "[{name: other, issuer: https://other.example.com, tokenEndpoint: https://other.example.com/token, clientCredentialsSecretRef: {name: other-client}}]", []map[string]any{manager, other}, ""},
		{"unmanaged same name while disabled", "{}", "[{name: agent-manager, issuer: https://other.example.com}]", []map[string]any{{keyName: agentplatform.ManagerProviderAppName, keyIssuer: "https://other.example.com"}}, ""},
		{"same name conflicts", enabled, "[{name: agent-manager, issuer: https://other.example.com}]", nil, "conflicts with the managed"},
		{"same issuer conflicts", enabled, "[{name: other, issuer: https://github.com/apps/giantswarm-agent-manager}]", nil, "conflicts with the managed"},
		{"duplicate names refused", enabled, "[{name: chatgpt, flow: chatgptDeviceCode}, {name: chatgpt, flow: chatgptDeviceCode}]", nil, "uniquely named"},
		{"missing name refused", enabled, "[{flow: chatgptDeviceCode}]", nil, "uniquely named"},
		{"invalid list refused", enabled, "stale", nil, "must be a list"},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := "muster:\n  muster:\n    providerApps: " + test.current + "\n"
			merged, _, err := keepAudiences([]byte(test.rendered), []byte(current))
			if test.refusal != "" {
				require.ErrorContains(t, err, test.refusal)
				return
			}
			require.NoError(t, err)
			_, values, err := mapping(merged)
			require.NoError(t, err)
			var apps []map[string]any
			if providers := at(values, "muster", "muster", "providerApps"); providers != nil {
				require.NoError(t, providers.Decode(&apps))
			}
			require.Equal(t, test.want, apps)
		})
	}
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
