package agentplatform

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	grantTokenEndpointField  = "tokenEndpoint"
	grantAudienceField       = "audience"
	grantExistingSecretField = "existingSecret"
	grantAppSecretField      = "secretName"
	grantFixtureInstallation = "graveler"
)

var managerGrantCases = []struct {
	component, input, issuer, minimum, ceiling, older string
}{
	{componentClusterManager, "clusterManager", "https://github.com/apps/giantswarm-cluster-manager", "0.37.0-rc.1", "0.38.0-0", "0.36.0"},
	{componentModelManager, "modelManager", "https://github.com/apps/giantswarm-model-manager", "1.20.0-rc.10", "2.0.0-0", "1.20.0-rc.9"},
	{componentSkillManager, "skillManager", "https://github.com/apps/giantswarm-agent-skills", "0.2.0-rc.1", "0.3.0-0", "0.1.0"},
}

func TestManagerGrantBrokerIsolation(t *testing.T) {
	for selection := 1; selection < 1<<len(managerGrantCases); selection++ {
		for _, withAgent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%03b/agent-%t", selection, withAgent), func(t *testing.T) {
				input, secrets := managerGrantInput(t, selection, withAgent)
				resolved, err := Parse(input)
				require.NoError(t, err)
				result, err := Render(input, secrets, render.ModeCommit)
				require.NoError(t, err)
				broker := credentialValues(t, resolved.brokerValues())
				clients := broker["brokerClients"].(map[string]any)
				audiences := broker["clientAudiences"].(map[string]any)
				targets := broker["targets"].(map[string]any)
				muster := credentialValues(t, resolved.musterValues())["muster"].(map[string]any)
				apps := muster["providerApps"].([]any)
				wantCount := 0
				if withAgent {
					wantCount++
				}
				for index, manager := range managerGrantCases {
					if selection&(1<<index) == 0 {
						require.NotContains(t, clients, manager.component)
						require.NotContains(t, targets, manager.component)
						continue
					}
					wantCount++
					require.Equal(t, []any{manager.component}, audiences[manager.component])
					require.Equal(t, map[string]any{"grantIssuer": manager.issuer}, targets[manager.component])
					for other, allowed := range audiences {
						if other != manager.component {
							require.NotContains(t, allowed, manager.component)
						}
					}
					var app map[string]any
					for _, candidate := range apps {
						if candidate.(map[string]any)[fieldName] == manager.component {
							app = candidate.(map[string]any)
						}
					}
					require.NotNil(t, app)
					require.Equal(t, manager.issuer, app["issuer"])
					require.Equal(t, map[string]any{fieldName: "giantswarm-" + manager.component + "-oauth-client"}, app["clientCredentialsSecretRef"])
					found := false
					for _, files := range result.Files {
						for path, file := range files {
							if strings.HasSuffix(path, "/secrets/"+manager.component+"-github-broker.yaml") {
								found = true
								require.Len(t, file.Generated, 1)
								require.Equal(t, resolved.Installation.Name+"-"+manager.component+"-broker-client-secret", file.Generated[0].Name)
								require.Equal(t, 32, file.Generated[0].Length)
							}
						}
					}
					require.True(t, found, "broker credentials missing for %s", manager.component)
				}
				require.Len(t, clients, wantCount)
				require.Len(t, apps, wantCount)
				require.Equal(t, true, muster["oauth"].(map[string]any)["mcpClient"].(map[string]any)[keyEnabled])
				trusted := credentialValues(t, resolved.trustedIssuers()[0])
				require.Contains(t, trusted["allowedAudiences"], ManagerExchangeSubjectAudience)
			})
		}
	}
}

func TestAdditionalManagerExchangeValues(t *testing.T) {
	for index, manager := range managerGrantCases {
		t.Run(manager.component, func(t *testing.T) {
			input, secrets := managerGrantInput(t, 1<<index, false)
			input[manager.input].(map[string]any)[keyGitHub].(map[string]any)[keyEnabled] = true
			result, err := Render(input, secrets, render.ModeCommit)
			require.NoError(t, err)
			var patch map[string]any
			for _, files := range result.Files {
				for path, file := range files {
					if strings.HasSuffix(path, "/apps/agent-platform/configmap-values.yaml.patch") {
						require.NoError(t, yaml.Unmarshal([]byte(file.Content), &patch))
					}
				}
			}
			require.NotNil(t, patch)
			github := patch[manager.component].(map[string]any)[keyGitHub].(map[string]any)
			require.Equal(t, true, github[keyEnabled])
			// #nosec G101 -- public endpoint and Secret reference, no credential values
			require.Equal(t, map[string]any{
				grantTokenEndpointField:  "https://muster.graveler.example.io/oauth/token",
				grantAudienceField:       manager.component,
				grantExistingSecretField: manager.component + "-github-broker",
			}, github[keyTokenExchange])
			if manager.component == componentSkillManager {
				require.Equal(t, map[string]any{grantAppSecretField: "giantswarm-agent-skills-app"}, github["app"])
			}
		})
	}
}

func TestAdditionalManagerGrantVersionGuards(t *testing.T) {
	for index, manager := range managerGrantCases {
		for _, invalid := range []string{"missing hold", "older manager", "older muster", "older meta chart"} {
			t.Run(manager.component+"/"+invalid, func(t *testing.T) {
				input, _ := managerGrantInput(t, 1<<index, false)
				holds := input["versions"].(map[string]any)[keyComponents].(map[string]any)
				switch invalid {
				case "missing hold":
					delete(holds, manager.component)
				case "older manager":
					holds[manager.component] = manager.older
				case "older muster":
					holds[componentMuster] = "5.63.1"
				case "older meta chart":
					input["versions"].(map[string]any)[keyChart] = "4.176.0"
				}
				_, err := Parse(input)
				require.Error(t, err)
				require.ErrorContains(t, err, manager.input+".github.tokenExchange.enabled")
			})
		}
	}
}

func TestAdditionalManagerGrantRequiresRunningManager(t *testing.T) {
	for index, manager := range managerGrantCases {
		t.Run(manager.component, func(t *testing.T) {
			input, _ := managerGrantInput(t, 1<<index, false)
			resolved, err := Parse(input)
			require.NoError(t, err)
			resolved.Installation.ChartLine = lineThree
			require.ErrorContains(t, resolved.validateAdditionalManagerGrants(), manager.input+".github.tokenExchange.enabled")
		})
	}
}

func TestAdditionalManagerGrantRejectsBrokerCollisions(t *testing.T) {
	for index, manager := range managerGrantCases {
		for _, kind := range []string{"client", "target"} {
			t.Run(manager.component+"/"+kind, func(t *testing.T) {
				input, _ := managerGrantInput(t, 1<<index, false)
				other, _ := loadInput(t, shapeHubPrivateTarget)
				federation := other["installation"].(map[string]any)["federation"].(map[string]any)
				for _, target := range federation["targets"].([]any) {
					target.(map[string]any)["hubs"] = []any{portalCaseOwn, grantFixtureInstallation}
				}
				if kind == "client" {
					federation["brokerClientId"] = manager.component
				} else {
					federation["targets"].([]any)[0].(map[string]any)["installation"] = manager.component
				}
				input["installation"].(map[string]any)["federation"] = federation
				_, err := Parse(input)
				require.ErrorContains(t, err, "reserves the "+manager.component+" broker "+kind)
			})
		}
	}
}

func TestSkillManagerGrantRequiresInstallationApp(t *testing.T) {
	input, _ := managerGrantInput(t, 1<<2, false)
	delete(input["skillManager"].(map[string]any)[keyGitHub].(map[string]any), "app")
	_, err := Parse(input)
	require.ErrorContains(t, err, "skillManager.github.app.secretName")
}

func TestSkillManagerLegacyAccessRequiresSupportedChart(t *testing.T) {
	input, _ := managerGrantInput(t, 0, false)
	input["skillManager"] = map[string]any{keyGitHub: map[string]any{keyEnabled: true}}
	input["versions"].(map[string]any)[keyChart] = "4.178.0"
	_, err := Parse(input)
	require.ErrorContains(t, err, "skillManager.github.enabled")
}

func TestAdditionalManagerExchangeOffRemovesBrokerAccess(t *testing.T) {
	for index, manager := range managerGrantCases {
		t.Run(manager.component, func(t *testing.T) {
			input, _ := managerGrantInput(t, 1<<index, false)
			input[manager.input].(map[string]any)[keyGitHub].(map[string]any)[keyTokenExchange].(map[string]any)[keyEnabled] = false
			resolved, err := Parse(input)
			require.NoError(t, err)
			require.False(t, resolved.brokers())
			muster := credentialValues(t, resolved.musterValues())["muster"].(map[string]any)
			require.NotContains(t, muster, "providerApps")
			server := muster["oauth"].(map[string]any)["server"].(map[string]any)
			require.NotContains(t, server, "tokenExchangeBroker")
		})
	}
}

func managerGrantInput(t *testing.T, selection int, withAgent bool) (map[string]any, map[string]string) {
	t.Helper()
	input, secrets := credentialInput(t, shapeGiantswarmSlackAppPub)
	if !withAgent {
		input["agentManager"] = map[string]any{}
	}
	holds := map[string]any{componentMuster: ">=5.63.2 <6.0.0"}
	for index, manager := range managerGrantCases {
		if selection&(1<<index) == 0 {
			continue
		}
		github := map[string]any{keyTokenExchange: map[string]any{keyEnabled: true}}
		if manager.component == componentSkillManager {
			github["app"] = map[string]any{grantAppSecretField: "giantswarm-agent-skills-app"}
		}
		input[manager.input] = map[string]any{keyGitHub: github}
		holds[manager.component] = ">=" + manager.minimum + " <" + manager.ceiling
	}
	input["versions"] = map[string]any{keyComponents: holds}
	return input, secrets
}
