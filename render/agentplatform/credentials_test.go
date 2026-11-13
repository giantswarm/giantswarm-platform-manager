package agentplatform

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	keyTokenExchange     = "tokenExchange"
	keyCredentialUI      = "uiURL"
	formURL              = "https://portal.example.com/credentials/{operation}"
	insecureFormURL      = "http://portal.example.com/{operation}"
	missingOperationURL  = "https://portal.example.com/credentials"
	refusedUIURL         = "RFC 6570 URI template"
	refusedManagerFloor  = "floor of"
	refusedExchangeChart = "meta chart 4.181.0-rc.3"
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
			require.Equal(t, []any{managerProviderAppName}, broker["clientAudiences"].(map[string]any)[componentAgentManager])
			require.Equal(t, map[string]any{"grantIssuer": managerGrantIssuer}, broker["targets"].(map[string]any)[managerProviderAppName])
			for client, audiences := range broker["clientAudiences"].(map[string]any) {
				if client != componentAgentManager {
					require.NotContains(t, audiences, managerProviderAppName)
				}
			}
			manager := credentialValues(t, in.agentManagerValues())
			require.Equal(t, map[string]any{keyEnabled: true, keyTokenExchange: map[string]any{
				"tokenEndpoint":  "https://" + in.host("muster") + "/oauth/token",
				"audience":       managerProviderAppName,
				"existingSecret": agentManagerBrokerSecret,
			}}, manager[keyGitHub])
			require.NotContains(t, manager, "githubTokenExchange")
			app := credentialValues(t, agentManagerProviderApp())
			require.Equal(t, managerGrantIssuer, app["issuer"])
			require.Equal(t, managerProviderAppName, app["name"])
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
		{"old manager", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentAgentManager: "2.21.0"}}
		}, refusedManagerFloor},
		{"old chart", func(input map[string]any) {
			input["versions"] = map[string]any{keyChart: ">=4.176.0 <4.177.0"}
		}, "meta chart"},
		{"credential UI chart without grant exchange", func(input map[string]any) {
			input["versions"] = map[string]any{keyChart: credentialChart}
		}, refusedExchangeChart},
		{"chart before grant exchange", func(input map[string]any) {
			input["versions"] = map[string]any{keyChart: "4.181.0-rc.2"}
		}, refusedExchangeChart},
		{"chart range excluding grant exchange", func(input map[string]any) {
			input["versions"] = map[string]any{keyChart: ">=4.177.0 <4.181.0"}
		}, refusedExchangeChart},
		{"release candidate manager", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentAgentManager: ">=2.22.0-rc.12 <3.0.0"}}
		}, refusedManagerFloor},
		{"old muster", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=5.60.0 <6.0.0"}}
		}, "muster version floor"},
		{"manager before the values schema", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentAgentManager: ">=3.0.0-rc.9 <4.0.0-0"}}
		}, "agent-manager version floor"},
		{"broker before Basic credential decoding", func(input map[string]any) {
			input["versions"] = map[string]any{keyComponents: map[string]any{componentMuster: ">=5.63.1 <6.0.0"}}
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
			federation["targets"].([]any)[0].(map[string]any)["installation"] = managerProviderAppName
			input["installation"].(map[string]any)["federation"] = federation
		}, "reserves the agent-manager broker target"},
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

// The pinned commit mode chosen with the exchange renders the exchange: the
// chart takes github.enabled with tokenExchange as the exchange, no pin.
func TestAgentManagerExchangeWithCommitRendersTheExchange(t *testing.T) {
	input, _ := credentialInput(t, shapeGiantswarmSlackAppPub)
	input["agentManager"].(map[string]any)[keyGitHub].(map[string]any)[keyEnabled] = true
	in, err := Parse(input)
	require.NoError(t, err)
	require.Equal(t, map[string]any{keyEnabled: true, keyTokenExchange: map[string]any{
		"tokenEndpoint":  "https://" + in.host("muster") + "/oauth/token",
		"audience":       managerProviderAppName,
		"existingSecret": agentManagerBrokerSecret,
	}}, credentialValues(t, in.agentManagerValues())[keyGitHub])
}

// The exchange turned off renders no commit mode: the record of an exchange
// reads back no pin (agent-manager.github.enabled is the exchange's render),
// so the person's choices are the exchange off and the pin unset.
func TestAgentManagerExchangeOffRendersNoPin(t *testing.T) {
	input, _ := credentialInput(t, shapeGiantswarmSlackAppPub)
	input["agentManager"].(map[string]any)[keyGitHub] = map[string]any{keyTokenExchange: map[string]any{keyEnabled: false}}
	in, err := Parse(input)
	require.NoError(t, err)
	require.NotContains(t, credentialValues(t, in.agentManagerValues()), keyGitHub)
}

func TestAgentManagerCredentialHoldsAdmitted(t *testing.T) {
	for name, versions := range map[string]any{
		"no hold":            nil,
		"chart range":        map[string]any{keyChart: chartRangeFour},
		"chart release":      map[string]any{keyChart: "4.181.0-rc.3"},
		"chart stable":       map[string]any{keyChart: "4.181.0"},
		"chart grant range":  map[string]any{keyChart: ">=4.181.0-rc.3 <5.0.0-0"},
		"manager and muster": map[string]any{keyComponents: map[string]any{componentAgentManager: ">=3.0.0-rc.10 <4.0.0-0", componentMuster: ">=5.63.2 <6.0.0"}},
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

func TestAgentManagerCredentialUIKeepsItsVersionFloor(t *testing.T) {
	input, _ := credentialInput(t, shapeGiantswarmSlackAppPub)
	input["agentManager"].(map[string]any)[keyGitHub] = map[string]any{keyTokenExchange: map[string]any{keyEnabled: false}}
	input["versions"] = map[string]any{
		keyChart: "4.177.0",
		keyComponents: map[string]any{
			componentAgentManager: ">=2.22.0 <3.0.0",
			componentMuster:       ">=5.61.0 <6.0.0",
		},
	}
	_, err := Parse(input)
	require.NoError(t, err)
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
		keyGitHub:     map[string]any{keyTokenExchange: map[string]any{keyEnabled: true}},
		"credentials": map[string]any{keyCredentialUI: formURL},
	}
	// The meta chart takes the credential form with the organizations only.
	input[keyOrganizations] = map[string]any{keyEnabled: true}
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
	patch := configmapPatch(t, result, in.Installation.Name)
	require.Equal(t, map[string]any{"jwksEgress": map[string]any{keyEnabled: true}}, patch["gateway"])
	require.NotEmpty(t, patch["extraObjects"])
	require.Contains(t, patch, "agentManager")
}

// The meta chart takes the credential form with the organizations only
// (agent-manager links it beside its organization tools). A form's address
// read back from the record while the switch is off — a person turns the
// switch off alone — renders nothing of the form: no
// agent-manager.credentials, no route on the gateway, no JWKS egress for it
// and no probe of it; the render is not refused, so the switch goes off and
// the form goes with it.
func TestCredentialFormRendersWithTheOrganizationsOnly(t *testing.T) {
	input, secrets := loadInput(t, shapeCredentialForm)
	input[keyOrganizations] = map[string]any{keyEnabled: false}
	in, err := Parse(input)
	require.NoError(t, err)
	result, err := Render(input, secrets, render.ModeCommit)
	require.NoError(t, err)
	patch := configmapPatch(t, result, in.Installation.Name)
	manager, _ := patch[componentAgentManager].(map[string]any)
	require.NotContains(t, manager, "credentials")
	require.NotContains(t, patch, "agentManager")
	require.NotContains(t, patch, "gateway")
	for _, p := range result.Probes {
		require.NotEqual(t, agentManagerRouteDimension, p.ID)
	}
}

// configmapPatch is the agent-platform configmap values patch a render wrote
// for the installation, decoded.
func configmapPatch(t *testing.T, result *render.Result, name string) map[string]any {
	t.Helper()
	for _, files := range result.Files {
		if f, ok := files["installations/"+name+"/apps/agent-platform/configmap-values.yaml.patch"]; ok {
			var patch map[string]any
			require.NoError(t, yaml.Unmarshal(f.Content, &patch))
			return patch
		}
	}
	t.Fatalf("no configmap patch rendered for %s", name)
	return nil
}
