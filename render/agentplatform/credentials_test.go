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
	refusedUIURL        = "RFC 6570 URI template"
	refusedManagerFloor = "floor of"
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
			require.Equal(t, map[string]any{"grantIssuer": managerGrantIssuer}, broker["targets"].(map[string]any)[agentManagerBrokerAudience])
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
			require.Equal(t, managerGrantIssuer, credentialValues(t, agentManagerProviderApp())["issuer"])
			muster := credentialValues(t, in.musterValues())["muster"].(map[string]any)
			require.Equal(t, true, muster["oauth"].(map[string]any)["mcpClient"].(map[string]any)[keyEnabled])
			trusted := credentialValues(t, in.trustedIssuers()[0])
			require.Equal(t, "https://"+in.host("dex"), trusted["issuer"])
			require.Equal(t, toAny(append(in.portalAudiences(), authenticatorClient)), trusted["allowedAudiences"])
			require.NotContains(t, trusted["allowedAudiences"], componentKagent)
			require.NotContains(t, muster, "aggregator")
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
		}, refusedManagerFloor},
		{"old chart", func(input map[string]any) {
			input["versions"] = map[string]any{keyChart: ">=4.176.0 <4.177.0"}
		}, "meta chart"},
		{"release candidate manager", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentAgentManager: ">=2.22.0-rc.12 <3.0.0"}}
		}, refusedManagerFloor},
		{"old muster", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=5.60.0 <6.0.0"}}
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
		}, refusedUIURL},
		{"missing operation", func(input map[string]any) {
			input["agentManager"].(map[string]any)["credentials"] = map[string]any{keyCredentialUI: missingOperationURL}
		}, refusedUIURL},
		{"unknown placeholder", func(input map[string]any) {
			input["agentManager"].(map[string]any)["credentials"] = map[string]any{keyCredentialUI: formURL + "/{foo}"}
		}, refusedUIURL},
		{"placeholder in host", func(input map[string]any) {
			// #nosec G101 -- a URL template, no credential value
			input["agentManager"].(map[string]any)["credentials"] = map[string]any{keyCredentialUI: "https://{operation}.example.com/credentials"}
		}, refusedUIURL},
		{"repeated operation", func(input map[string]any) {
			input["agentManager"].(map[string]any)["credentials"] = map[string]any{keyCredentialUI: formURL + "?again={operation}"}
		}, refusedUIURL},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, _ := credentialInput(t, shapeGiantswarmSlackAppPub)
			test.change(input)
			_, err := Parse(input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestAgentManagerCredentialHoldsAdmitted(t *testing.T) {
	for name, versions := range map[string]any{
		"no hold":            nil,
		"chart range":        map[string]any{keyChart: chartRangeFour},
		"manager and muster": map[string]any{keyComponents: map[string]any{componentAgentManager: ">=2.22.0 <3.0.0", componentMuster: ">=5.61.0 <6.0.0"}},
	} {
		t.Run(name, func(t *testing.T) {
			input, _ := credentialInput(t, shapeGiantswarmSlackAppPub)
			if versions != nil {
				input["versions"] = versions
			}
			_, err := Parse(input)
			require.NoError(t, err)
		})
	}
}

func TestAgentManagerCredentialUIIsInputOnly(t *testing.T) {
	input, _ := credentialInput(t, shapePortalGitHubGrant)
	delete(input["agentManager"].(map[string]any), "credentials")
	in, err := Parse(input)
	require.NoError(t, err)
	require.NotContains(t, credentialValues(t, in.agentManagerValues()), "credentials")
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
	delete(input, "versions")
	return input, secrets
}

func credentialValues(t *testing.T, values render.Map) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, yaml.Unmarshal(render.MustYAML(values), &result))
	return result
}

func TestValidCredentialUI(t *testing.T) {
	for template, want := range map[string]bool{
		formURL: true,
		"https://portal.example.com:8443/orgs/{parent}/{organization}?operation={operation}": true,
		insecureFormURL:     false,
		missingOperationURL: false,
		"https://user@portal.example.com/{operation}":              false,
		"https://portal.example.com/{operation}#fragment":          false,
		"https://portal.example.com/credentials{?operation}":       true,
		"https://portal.example.com/orgs{/parent,operation}":       true,
		"https://portal.example.com/credentials/{+operation}":      true,
		"https://portal.example.com/a b/{operation}":               false,
		"https://portal.example.com/a|b/{operation}":               false,
		"https://portal.example.com/{operation}?again={operation}": false,
		"https://portal.example.com/{operation}/{foo}":             false,
		"https://{organization}.example.com/{operation}":           false,
	} {
		require.Equal(t, want, validCredentialUI(template), template)
	}
}

func toAny(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}

// Where the edge already validates a portal's ID token, the credential form's
// route shares the edge's JWKS egress: the configmap patch carries one
// gateway block, so konfigure's merge sees no duplicate key, beside the
// edge's own provider and the route (the credential-form golden covers the
// route alone).
func TestAgentManagerRouteBesideTheEdgeProvider(t *testing.T) {
	input, secrets := loadInput(t, shapeOrganizationsApply)
	input["agentManager"] = map[string]any{"credentials": map[string]any{keyCredentialUI: formURL}}
	in, err := Parse(input)
	require.NoError(t, err)
	require.True(t, in.edgeJWTProvider())
	result, err := Render(input, secrets, render.ModeCommit)
	require.NoError(t, err)
	var patch map[string]any
	for _, files := range result.Files {
		if f, ok := files["installations/"+in.Installation.Name+"/apps/agent-platform/configmap-values.yaml.patch"]; ok {
			require.NoError(t, yaml.Unmarshal(f.Content, &patch))
		}
	}
	require.Equal(t, map[string]any{"jwksEgress": map[string]any{keyEnabled: true}}, patch["gateway"])
	require.NotEmpty(t, patch["extraObjects"])
	route := patch["agentManager"].(map[string]any)["route"].(map[string]any)
	require.Equal(t, true, route[keyEnabled])
	require.Equal(t, "https://"+in.host("dex"), route["jwtAuthentication"].(map[string]any)["issuer"])
}
