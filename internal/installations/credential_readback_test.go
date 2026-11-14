package installations

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	inputAgentManagerExchange = "agentManager.github.tokenExchange.enabled"
	inputAgentManagerCommit   = "agentManager.github.enabled"
	keyCredentials            = "credentials"
)

func TestAgentManagerCredentialReadBack(t *testing.T) {
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)
	for _, test := range []struct {
		name  string
		patch string
		want  map[string]any
	}{
		// #nosec G101 -- public form URL template, no credential value
		{"installation form", "agent-manager:\n  credentials:\n    platformUIURL: https://portal.acme.test/models/{namespace}/credentials/{operation}\n", map[string]any{
			inputAgentManagerExchange:                false,
			inputOrganizationsCommit:                 false,
			"agentManager.credentials.platformUIURL": "https://portal.acme.test/models/{namespace}/credentials/{operation}",
		}},
		// #nosec G101 -- route configuration and public URLs, no credential value
		{"grant exchange and organization form", "agent-manager:\n  github:\n    enabled: true\n    tokenExchange:\n      tokenEndpoint: https://muster.rowan.acme.test/oauth/token\n  credentials:\n    uiURL: https://portal.acme.test/credentials/{operation}\n", map[string]any{
			inputAgentManagerExchange:        true,
			inputOrganizationsCommit:         false,
			"agentManager.credentials.uiURL": "https://portal.acme.test/credentials/{operation}",
		}},
		{"former key", "agent-manager:\n  github:\n    enabled: false\n  githubTokenExchange:\n    endpoint: https://muster.rowan.acme.test/oauth/token\n", map[string]any{
			inputAgentManagerCommit:   false,
			inputAgentManagerExchange: true,
			inputOrganizationsCommit:  false,
		}},
		{"pin", "agent-manager:\n  github:\n    enabled: true\n", map[string]any{
			inputAgentManagerCommit:   true,
			inputAgentManagerExchange: false,
			inputOrganizationsCommit:  false,
		}},
		{"absent", "{}\n", map[string]any{inputAgentManagerExchange: false, inputOrganizationsCommit: false}},
	} {
		t.Run(test.name, func(t *testing.T) {
			read := files(map[string]string{"acme/configs:" + definition.EnabledMarker("rowan"): test.patch})
			got, err := definition.ReadBack(t.Context(), read, readBackInstallation, readBackName, nil)
			require.NoError(t, err)
			require.Equal(t, managerGrantDefaults(test.want), got)
		})
	}
}

func TestInstallationCredentialFormWithoutOrganizations(t *testing.T) {
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)
	input := map[string]any{
		"organizations": map[string]any{keyEnabled: false},
		"agentManager": map[string]any{keyCredentials: map[string]any{
			"platformUIURL": "https://portal.acme.test/models/{namespace}/credentials/{operation}",
		}},
	}
	refused, err := definition.TypedWithoutSwitch(input, input)
	require.NoError(t, err)
	require.Empty(t, refused)
}

// A reconcile types no input: the plan reads the person's choices back from
// the record and renders them over the installation's facts. On an
// installation whose configmap patch carries agent-manager's credential form
// (agentManager.credentials.uiURL), that render keeps the route the form's
// backend reaches agent-manager on (agentManager.route, the JWKS egress),
// derived from the form's address and never read back itself: the
// credential-form golden's patch read back and rendered again is the same
// patch, byte for byte.
func TestAgentManagerCredentialFormSurvivesAReconcile(t *testing.T) {
	patch, read, again := renderGoldenFromTheRecord(t, "credential-form")
	require.Contains(t, patch, "agentManager:\n  route:\n", "the golden carries no route to keep")
	for input := range read {
		require.False(t, strings.HasPrefix(input, "agentManager.route"), "%s is derived, not read back", input)
	}
	require.Equal(t, patch, again)
}

// renderGoldenFromTheRecord is a reconcile of a golden shape that types no
// input: the shape's golden configmap patch on record, the person's choices
// read back from it, rendered again over the shape's installation facts. It
// answers the patch on record, what was read back and the patch rendered
// again.
func renderGoldenFromTheRecord(t *testing.T, shapeName string) (patch string, read map[string]any, again string) {
	t.Helper()
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)
	shape := os.DirFS(filepath.Join("../../render/agentplatform/testdata", shapeName))
	raw, err := fs.ReadFile(shape, "input.yaml")
	require.NoError(t, err)
	var doc struct {
		Input map[string]any `yaml:"input"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	record := doc.Input["installation"].(map[string]any)
	name := record["name"].(string)
	marker := definition.EnabledMarker(name)
	golden, err := fs.ReadFile(shape, "golden/giantswarm/giantswarm-configs/"+marker)
	require.NoError(t, err)

	inst := Installation{Name: name, BaseDomain: record["baseDomain"].(string), Repositories: Repositories{Configs: "giantswarm/giantswarm-configs", ManagementClusters: "giantswarm/giantswarm-management-clusters"}}
	read, err = definition.ReadBack(t.Context(), files(map[string]string{inst.Repositories.Configs + ":" + marker: string(golden)}), inst, "", nil)
	require.NoError(t, err)

	input := map[string]any{"installation": record}
	for key, value := range read {
		node := input
		steps := strings.Split(key, ".")
		for _, step := range steps[:len(steps)-1] {
			if _, ok := node[step]; !ok {
				node[step] = map[string]any{}
			}
			node = node[step].(map[string]any)
		}
		node[steps[len(steps)-1]] = value
	}
	result, err := definition.Render(input, nil, render.ModeCommit)
	require.NoError(t, err)
	return string(golden), read, string(result.Tree()[filepath.Join("giantswarm", "giantswarm-configs", marker)])
}
