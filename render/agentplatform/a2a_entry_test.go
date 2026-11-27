package agentplatform

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const a2aFixtureConnector = "a2a-outside"
const a2aFixtureApplication = "application"

func a2aEntryInput(t *testing.T) (map[string]any, map[string]string) {
	t.Helper()
	input, secrets := loadInput(t, shapeOrganizationsApply)
	input["klausGateway"] = map[string]any{"a2aServer": map[string]any{
		"enabled":           true,
		"staffConnectors":   []any{"staff"},
		"exchangeSecretRef": map[string]any{fieldName: "a2a-exchange", fieldKey: "client-secret"},
		"issuers": []any{map[string]any{
			fieldName: "outside", "issuer": "https://identity.example.com", "audience": a2aFixtureApplication, "connectorId": a2aFixtureConnector,
			"allowedClients": []any{a2aFixtureApplication}, "jwks": map[string]any{"host": "identity.example.com", "path": "/keys", "port": 443},
		}},
	}}
	input["tenants"] = map[string]any{"platform": map[string]any{"users": []any{}}}
	input["musterMcpServerViewers"] = []any{map[string]any{fieldKind: "Group", fieldName: "staff:agents"}}
	return input, secrets
}

func a2aChoice(input map[string]any) map[string]any {
	return input["klausGateway"].(map[string]any)["a2aServer"].(map[string]any)
}

func TestA2AEntryDisabled(t *testing.T) {
	input, secrets := loadInput(t, shapeOrganizationsApply)
	baseline, err := Render(input, secrets, render.ModeCommit)
	require.NoError(t, err)
	input["klausGateway"] = map[string]any{"a2aServer": map[string]any{"enabled": false}}
	disabled, err := Render(input, secrets, render.ModeCommit)
	require.NoError(t, err)
	require.Equal(t, baseline.Tree(), disabled.Tree())
}

func TestA2AEntryWithoutSlack(t *testing.T) {
	input, secrets := a2aEntryInput(t)
	parsed, err := Parse(input)
	require.NoError(t, err)
	require.True(t, parsed.klausGateway())
	require.False(t, parsed.slackGateway())
	result, err := Render(input, secrets, render.ModeCommit)
	require.NoError(t, err)
	configuration := parsed.klausGatewayValues()
	require.Equal(t, false, mapValue(mapValue(configuration, "slack").(render.Map), "enabled"))
	require.Equal(t, false, mapValue(mapValue(configuration, "obo").(render.Map), "enabled"))
	route := mapValue(mapValue(configuration, "agentgatewayRoute").(render.Map), "a2aServer").(render.Map)
	exchange := mapValue(route, "exchange").(render.Map)
	require.Equal(t, []string{"klaus-gateway", "dex-k8s-authenticator", componentKagent}, mapValue(exchange, "audiences"))
	require.Equal(t, "a2a-exchange", mapValue(exchange, "secretName"))
	require.Equal(t, "", treeFile(t, result, "/secrets/"+klausGatewaySlackSecret+".yaml"))

	var dex struct {
		OIDC struct {
			GrantTypes    []string `yaml:"grantTypes"`
			StaticClients map[string]struct {
				AllowedConnectors []string `yaml:"allowedConnectors"`
				TrustedPeers      []string `yaml:"trustedPeers"`
			} `yaml:"staticClients"`
			ExtraStaticClients map[string]struct {
				GrantTypes        []string `yaml:"grantTypes"`
				AllowedConnectors []string `yaml:"allowedConnectors"`
			} `yaml:"extraStaticClients"`
		} `yaml:"oidc"`
	}
	require.NoError(t, yaml.Unmarshal(render.MustYAML(parsed.dexPatch()), &dex))
	require.NotContains(t, dex.OIDC.GrantTypes, a2aClientCredentialsGrant)
	require.Equal(t, []string{a2aExchangeGrant}, dex.OIDC.ExtraStaticClients[a2aExchangeClient].GrantTypes)
	require.Equal(t, []string{a2aFixtureConnector}, dex.OIDC.ExtraStaticClients[a2aExchangeClient].AllowedConnectors)
	for _, name := range []string{"gitopsui", "grafana", "gsCLIAuth", "happa", a2aAuthenticatorKey, "mcpCapi", "mcpKubernetes", "mcpPrometheus", "muster"} {
		require.Equal(t, []string{"staff"}, dex.OIDC.StaticClients[name].AllowedConnectors)
	}
	require.Contains(t, dex.OIDC.StaticClients[a2aAuthenticatorKey].TrustedPeers, a2aExchangeClient)
}

func TestA2AApplicationIdentity(t *testing.T) {
	input, _ := a2aEntryInput(t)
	installation := input["installation"].(map[string]any)
	choice := a2aChoice(input)
	choice["issuers"].([]any)[0].(map[string]any)["issuer"] = "https://dex." + installation["baseDomain"].(string)
	choice["issuers"].([]any)[0].(map[string]any)["audience"] = "klaus-gateway-app"
	choice["applications"] = []any{map[string]any{"id": a2aFixtureApplication, fieldName: "Application", "secretRef": map[string]any{fieldName: "application-client", fieldKey: "secret"}}}
	parsed, err := Parse(input)
	require.NoError(t, err)
	oidc := mapValue(parsed.dexPatch(), "oidc").(render.Map)
	require.Contains(t, mapValue(oidc, "grantTypes"), a2aClientCredentialsGrant)
	clients := mapValue(oidc, "extraStaticClients").(render.Map)
	for _, client := range clients {
		grants := mapValue(client.Value.(render.Map), "grantTypes")
		if a2aFixtureApplication == client.Key {
			require.Equal(t, []string{a2aClientCredentialsGrant}, grants)
		} else {
			require.NotContains(t, grants, a2aClientCredentialsGrant, client.Key)
		}
	}
	require.Equal(t, []string{a2aFixtureApplication}, mapValue(mapValue(clients, "klaus-gateway-app").(render.Map), "trustedPeers"))
}

func TestA2AEntryRefusals(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"implicit RBAC", func(input map[string]any) { delete(input, "tenants") }, "explicit RBAC"},
		{"broad RBAC", func(input map[string]any) {
			input["musterMcpServerViewers"] = []any{map[string]any{fieldKind: "Group", fieldName: "system:authenticated"}}
		}, "broad system groups"},
		{"staff connector", func(input map[string]any) { a2aChoice(input)["staffConnectors"] = []any{a2aFixtureConnector} }, "distinct from staffConnectors"},
		{"duplicate issuer", func(input map[string]any) {
			issuers := a2aChoice(input)["issuers"].([]any)
			a2aChoice(input)["issuers"] = append(issuers, issuers[0])
		}, "unique names"},
		{"insecure issuer", func(input map[string]any) {
			a2aChoice(input)["issuers"].([]any)[0].(map[string]any)["issuer"] = "http://identity.example.com"
		}, "klausGateway"},
		{"missing exchange reference", func(input map[string]any) { delete(a2aChoice(input), "exchangeSecretRef") }, "exchangeSecretRef"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input, _ := a2aEntryInput(t)
			testCase.mutate(input)
			_, err := Parse(input)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}
