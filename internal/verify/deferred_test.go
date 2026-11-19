package verify

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestRolloutRenderUsesTheActionsDeferredPairs(t *testing.T) {
	content, err := os.ReadFile("../../render/agentplatform/testdata/hub-private-target/input.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Input map[string]any `yaml:"input"`
	}
	if err := yaml.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	definition, ok := installations.FindCapability("agent-platform")
	if !ok {
		t.Fatal("no definition")
	}
	name := render.ExchangeSecretName(render.TokenExchangeClient("burrow", "gopher", true))
	filtered, err := renderLive(LiveOptions{Definition: definition, Inputs: Inputs{Values: fixture.Input}, DeferredPairs: []string{name}})
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range filtered.values {
		if strings.Contains(path, "burrow") || strings.Contains(value, "burrow") {
			t.Errorf("rollout expects deferred target at %s", path)
		}
	}
	complete, err := renderLive(LiveOptions{Definition: definition, Inputs: Inputs{Values: fixture.Input}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for path := range complete.values {
		if strings.Contains(path, "burrow") {
			found = true
		}
	}
	if !found {
		t.Fatal("full verification omitted the deferred target")
	}
}

func TestRolloutRenderProbesOnlyThePairsTheCommitRendered(t *testing.T) {
	connector := render.ConnectorSecretName("giantswarm-gopher-oidc")
	cases := []struct {
		capability string
		fixture    string
		pair       string
		with       func(t *testing.T, values map[string]any)
	}{
		{
			capability: installations.ClusterMCPServers,
			fixture:    "../../render/clustermcpservers/testdata/hub-target/input.yaml",
			pair:       render.FederationSecretName("gopher", "osprey"),
			with: func(t *testing.T, values map[string]any) {
				fleet := fixtureInput(t, "../../render/clustermcpservers/testdata/fleet/input.yaml")
				connectors := federationOf(fleet)["connectors"].([]any)
				federationOf(values)["connectors"] = connectors[:1]
			},
		},
		{
			capability: installations.AgentPlatform,
			fixture:    "../../render/agentplatform/testdata/public-customer/input.yaml",
			pair:       render.FederationSecretName("gopher", "kestrel"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.capability, func(t *testing.T) {
			definition, ok := installations.FindCapability(tc.capability)
			require.True(t, ok)
			values := fixtureInput(t, tc.fixture)
			if tc.with != nil {
				tc.with(t, values)
			}
			res, _, _, err := renderValues(definition, values, nil, nil)
			require.NoError(t, err)
			generated := generatedNames(res)
			require.Contains(t, generated, tc.pair)
			withoutPair := slices.DeleteFunc(slices.Clone(generated), func(name string) bool { return name == tc.pair })

			for _, probed := range []struct {
				name      string
				opts      LiveOptions
				connector bool
			}{
				{name: "nothing on record", connector: true},
				{name: "pair rendered by the commit", opts: LiveOptions{Generated: generated}, connector: true},
				{name: "pair not rendered by the commit", opts: LiveOptions{Generated: withoutPair}},
				{name: "pair deferred by the commit", opts: LiveOptions{DeferredPairs: []string{tc.pair}}},
			} {
				t.Run(probed.name, func(t *testing.T) {
					opts := probed.opts
					opts.Definition, opts.Inputs = definition, Inputs{Values: values}
					lv, err := renderLive(opts)
					require.NoError(t, err)
					require.Equal(t, probed.connector, probesSecret(lv.probes, connector))
					res, _, _, err := renderValues(definition, values, opts.DeferredPairs, opts.Generated)
					require.NoError(t, err)
					require.Equal(t, probed.connector, rendersText(res, connector))
				})
			}
		})
	}
}

func fixtureInput(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path) // #nosec G304 -- a fixture path of the test
	require.NoError(t, err)
	var fixture struct {
		Input map[string]any `yaml:"input"`
	}
	require.NoError(t, yaml.Unmarshal(content, &fixture))
	return fixture.Input
}

func federationOf(values map[string]any) map[string]any {
	return values["installation"].(map[string]any)["federation"].(map[string]any)
}

func generatedNames(res *render.Result) []string {
	var names []string
	for _, files := range res.Files {
		for _, f := range files {
			for _, g := range f.Generated {
				if !slices.Contains(names, g.Name) {
					names = append(names, g.Name)
				}
			}
		}
	}
	return names
}

func probesSecret(probes []render.Probe, secret string) bool {
	return slices.ContainsFunc(probes, func(p render.Probe) bool {
		return p.Kind == render.SecretLoaded && p.Name == secret
	})
}

func rendersText(res *render.Result, text string) bool {
	for path, content := range res.Tree() {
		if strings.Contains(path, text) || strings.Contains(string(content), text) {
			return true
		}
	}
	return false
}
