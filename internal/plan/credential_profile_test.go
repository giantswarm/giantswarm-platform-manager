package plan

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialBrokerIssuerAfterReadback(t *testing.T) {
	const profile = "agent-manager:\n  githubTokenExchange:\n    endpoint: https://muster.example.com/oauth/token\nmuster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n          - issuer: https://dex.example.com\n            allowedAudiences: [backstage]\n"
	for _, test := range []struct {
		name    string
		current string
		refused bool
	}{
		{"no additional issuer", "{}", false},
		{"same canonical issuer", "muster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n          - issuer: https://dex.example.com\n            allowedAudiences: [backstage, dex-k8s-authenticator]\n            subjectClaim: email\n", false},
		{"independent issuer", "muster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n          - issuer: https://other.example.com\n            allowedAudiences: [backstage]\n", true},
		{"missing audience allowlist", "muster:\n  muster:\n    oauth:\n      server:\n        trustedIssuers:\n          - issuer: https://dex.example.com\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := keepAudiences([]byte(profile), []byte(test.current))
			if test.refused {
				require.ErrorContains(t, err, "one canonical Dex")
			} else {
				require.NoError(t, err)
			}
		})
	}
	_, _, err := keepAudiences([]byte("{}"), []byte("{}"))
	require.NoError(t, err)
}
