package plan

import (
	"testing"

	"github.com/stretchr/testify/require"

	"gopkg.in/yaml.v3"
)

const identityEntraConnector = "giantswarm-ad"

func TestIdentityCheckPolicyReplacesKeptListOnlyWhenRendered(t *testing.T) {
	const current = `muster:
  muster:
    oauth:
      server:
        dex:
          connectorId: giantswarm-github
          identityCheckConnectors: [giantswarm-ad, giantswarm-simple-oidc]
`
	for _, testCase := range []struct {
		name     string
		rendered string
		want     []string
	}{
		{name: "unselected", rendered: "muster: {}\n", want: []string{identityEntraConnector, "giantswarm-simple-oidc"}},
		{name: "selected", rendered: "muster:\n  muster:\n    oauth:\n      server:\n        dex:\n          allowPrivateIPOIDC: true\n          identityCheckConnectors: [giantswarm-ad, giantswarm-gazelle-oidc]\n", want: []string{identityEntraConnector, "giantswarm-gazelle-oidc"}},
		{name: "explicit empty", rendered: "muster:\n  muster:\n    oauth:\n      server:\n        dex:\n          identityCheckConnectors: []\n", want: []string{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			result, _, err := keepAudiences([]byte(testCase.rendered), []byte(current))
			require.NoError(t, err)
			_, values, err := mapping(result)
			require.NoError(t, err)
			path := []string{musterKey, musterKey, "oauth", "server", "dex"}
			connector := at(values, append(path, "connectorId")...)
			require.NotNil(t, connector)
			require.Equal(t, "giantswarm-github", connector.Value)
			list := at(values, append(path, "identityCheckConnectors")...)
			require.NotNil(t, list)
			require.Equal(t, yaml.SequenceNode, list.Kind)
			got := make([]string, 0, len(list.Content))
			for _, entry := range list.Content {
				got = append(got, entry.Value)
			}
			require.Equal(t, testCase.want, got)
		})
	}
}
