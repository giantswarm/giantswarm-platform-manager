package agentplatform

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	keyCredentialUI     = "uiURL"
	formURL             = "https://portal.example.com/credentials/{operation}"
	insecureFormURL     = "http://portal.example.com/{operation}"
	missingOperationURL = "https://portal.example.com/credentials"
)

func TestAgentManagerCredentialBroker(t *testing.T) {
	for _, shape := range []string{shapeGiantswarmSlackAppPub, shapeHubPrivateTarget, shapePortalGitHubGrant} {
		t.Run(shape, func(t *testing.T) {
			input, secrets := credentialInput(t, shape)
			in, err := Parse(input)
			require.NoError(t, err)
			result, err := Render(input, secrets, render.ModeCommit)
			require.NoError(t, err)
			broker := credentialValues(t, in.brokerValues())
			require.Equal(t, []any{agentManagerBrokerAudience}, broker["clientAudiences"].(map[string]any)[componentAgentManager])
			require.Equal(t, map[string]any{"grantIssuer": agentManagerGrantIssuer}, broker["targets"].(map[string]any)[agentManagerBrokerAudience])
			for client, audiences := range broker["clientAudiences"].(map[string]any) {
				if client != componentAgentManager {
					require.NotContains(t, audiences, agentManagerBrokerAudience)
				}
			}
			manager := credentialValues(t, in.agentManagerValues())
			require.Equal(t, map[string]any{keyEnabled: false}, manager[keyGitHub])
			exchange := manager["githubTokenExchange"].(map[string]any)
			require.Equal(t, componentAgentManager, exchange["clientID"])
			require.Equal(t, agentManagerBrokerSecret, exchange["secretName"])
			require.Equal(t, "https://"+in.host("muster")+"/oauth/token", exchange["endpoint"])
			require.Equal(t, agentManagerGrantIssuer, credentialValues(t, in.agentManagerProviderApp())["issuer"])
			muster := credentialValues(t, in.musterValues())["muster"].(map[string]any)
			require.Equal(t, true, muster["oauth"].(map[string]any)["mcpClient"].(map[string]any)[keyEnabled])
			trusted := credentialValues(t, in.trustedIssuers()[0])
			require.Equal(t, "https://"+in.host("dex"), trusted["issuer"])
			require.ElementsMatch(t, in.audiences(), trusted["allowedAudiences"])
			found := false
			for _, files := range result.Files {
				for path, file := range files {
					if path == "management-clusters/"+in.Installation.Name+"/extras/agent-platform/secrets/"+agentManagerBrokerSecret+".yaml" {
						found = true
						require.Len(t, file.Generated, 1)
						require.Equal(t, in.Installation.Name+"-agent-manager-broker-client-secret", file.Generated[0].Name)
						require.Equal(t, 32, file.Generated[0].Length)
					}
				}
			}
			require.True(t, found)
			require.Equal(t, in.Installation.Name+"-muster-credentials-revision", result.Revisions[in.Installation.Name+"-agent-manager-broker-client-secret"])
		})
	}
}

func TestAgentManagerCredentialInputsFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(map[string]any)
		want   string
	}{
		{"legacy pin", func(input map[string]any) {
			input["agentManager"].(map[string]any)[keyGitHub] = map[string]any{keyEnabled: true}
		}, "mutually exclusive"},
		{"old manager", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentAgentManager: "2.21.0"}}
		}, "floor of"},
		{"no manager floor", func(input map[string]any) { delete(input, "versions") }, "floor of"},
		{"old muster", func(input map[string]any) {
			input["versions"].(map[string]any)[keyComponents].(map[string]any)[componentMuster] = "5.57.0"
		}, "muster version floor"},
		{"client collision", func(input map[string]any) {
			other, _ := loadInput(t, shapeHubPrivateTarget)
			federation := other["installation"].(map[string]any)["federation"].(map[string]any)
			for _, target := range federation["targets"].([]any) {
				target.(map[string]any)["hubs"] = []any{portalCaseOwn, "graveler"}
			}
			federation["brokerClientId"] = componentAgentManager
			input["installation"].(map[string]any)["federation"] = federation
		}, "reserves the agent-manager broker client"},
		{"target collision", func(input map[string]any) {
			other, _ := loadInput(t, shapeHubPrivateTarget)
			federation := other["installation"].(map[string]any)["federation"].(map[string]any)
			for _, target := range federation["targets"].([]any) {
				target.(map[string]any)["hubs"] = []any{portalCaseOwn, "graveler"}
			}
			federation["targets"].([]any)[0].(map[string]any)["installation"] = agentManagerBrokerAudience
			input["installation"].(map[string]any)["federation"] = federation
		}, "reserves the agent-manager-github broker target"},
		{"insecure UI", func(input map[string]any) {
			input["agentManager"].(map[string]any)["credentials"] = map[string]any{keyCredentialUI: insecureFormURL}
		}, "HTTPS URL"},
		{"missing operation", func(input map[string]any) {
			input["agentManager"].(map[string]any)["credentials"] = map[string]any{keyCredentialUI: missingOperationURL}
		}, "HTTPS URL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, _ := credentialInput(t, shapeGiantswarmSlackAppPub)
			test.change(input)
			_, err := Parse(input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func credentialInput(t *testing.T, shape string) (map[string]any, map[string]string) {
	t.Helper()
	input, secrets := loadInput(t, shape)
	if shape == shapePortalGitHubGrant {
		input["installation"].(map[string]any)["customer"] = "giantswarm"
		input["installation"].(map[string]any)["chartLine"] = "4"
		input["installation"].(map[string]any)["podCertificateRequest"] = true
	}
	input["agentManager"] = map[string]any{
		"githubTokenExchange": map[string]any{keyEnabled: true},
		"credentials":         map[string]any{keyCredentialUI: formURL},
	}
	input["versions"] = map[string]any{keyComponents: map[string]any{componentAgentManager: ">=2.22.0-rc.3 <3.0.0"}}
	return input, secrets
}

func credentialValues(t *testing.T, values render.Map) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, yaml.Unmarshal(render.MustYAML(values), &result))
	return result
}
