package agentplatform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const privateRedirectPermission = "allowPrivateIPRedirectURIs: true"

func TestPrivateClientPinsRequireCompatibleMuster(t *testing.T) {
	for _, testCase := range []struct {
		version    string
		compatible bool
	}{{"5.63.24", false}, {"5.64.0", true}} {
		t.Run(testCase.version, func(t *testing.T) {
			input, _ := loadInput(t, shapeGiantswarmSlackApp)
			input["versions"] = map[string]any{"components": map[string]any{"muster": testCase.version}}
			_, err := Parse(input)
			if testCase.compatible {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "versions.components.muster")
			}
		})
	}
}

func TestPrivateClientPinsPreserveAddressPermissions(t *testing.T) {
	input, secrets := loadInput(t, shapeGiantswarmSlackApp)
	result, err := Render(input, secrets, render.ModeCommit)
	require.NoError(t, err)
	var platform, kubernetes string
	for path, content := range result.Tree() {
		if strings.HasSuffix(path, "apps/agent-platform/configmap-values.yaml.patch") {
			platform = string(content)
		}
		if strings.HasSuffix(path, "extras/mcp-kubernetes/user-values.yaml") {
			kubernetes = string(content)
		}
	}
	for _, setting := range []string{"preRegisteredClientMetadata:", privateRedirectPermission, "allowPrivateIPClientMetadataHosts:", "allowPrivateIPOIDC: true"} {
		require.Contains(t, platform, setting)
	}
	for _, setting := range []string{"preRegisteredClientMetadata:", "allowPrivateIPs: true", privateRedirectPermission} {
		require.Contains(t, kubernetes, setting)
	}
	require.NotContains(t, platform+kubernetes, "restrictClientMetadata: true")
	require.NotContains(t, platform+kubernetes, "allowPrivateIPRedirectURIHosts:")
	require.NotContains(t, kubernetes, "privateIPHosts:")
}
