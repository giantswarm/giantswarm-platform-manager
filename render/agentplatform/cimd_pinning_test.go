package agentplatform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const privateRedirectPermission = "allowPrivateIPRedirectURIs: true"

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
