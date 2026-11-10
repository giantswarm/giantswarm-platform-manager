package agentplatform

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// keyFormerApp is the former name of agentManager.skills.github.app.secretName.
const keyFormerApp = "appSecretName"

func TestFormerInputNameRendersAsTheCurrentOne(t *testing.T) {
	render1 := func(skills map[string]any) (*render.Result, error) {
		input, secrets := loadInput(t, shapeHubPrivateTarget)
		input["agentManager"] = map[string]any{keySkills: skills}
		return Render(input, secrets, render.ModeCommit)
	}
	current, err := render1(map[string]any{keyGitHub: appSecret(skillsApp), keyMint: true})
	require.NoError(t, err)

	for name, skills := range map[string]map[string]any{
		"former name alone":             {keyFormerApp: skillsApp, keyMint: true},
		"both names, same value":        {keyFormerApp: skillsApp, keyGitHub: appSecret(skillsApp), keyMint: true},
		"former name empty beside":      {keyFormerApp: "", keyGitHub: appSecret(skillsApp), keyMint: true},
		"current name empty beside old": {keyFormerApp: skillsApp, keyGitHub: appSecret(""), keyMint: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := render1(skills)
			require.NoError(t, err)
			require.Equal(t, current.Files, got.Files)
		})
	}

	_, err = render1(map[string]any{keyFormerApp: "other-app", keyGitHub: appSecret(skillsApp)})
	require.True(t, errors.Is(err, ErrInput), "%v", err)
	require.ErrorContains(t, err, "agentManager.skills.github.app.secretName only")
}

// TestComponentInputsAreNamedAfterTheChartPath holds the convention of the
// managers' inputs: an input rendered into one key of its component's values
// carries that key's path under the component as its name.
func TestComponentInputsAreNamedAfterTheChartPath(t *testing.T) {
	raw, err := definitions.FS.ReadFile("agent-platform/schema.json")
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	components := map[string]string{"agentManager": "agent-manager", "clusterManager": "cluster-manager", "modelManager": "model-manager", "skillManager": "skill-manager"}
	checked := 0
	var walk func(node map[string]any, path []string)
	walk = func(node map[string]any, path []string) {
		if props, ok := node["properties"].(map[string]any); ok {
			for k, child := range props {
				walk(child.(map[string]any), append(append([]string{}, path...), k))
			}
			return
		}
		renders, _ := node["x-renders"].([]any)
		if len(path) < 2 || len(renders) != 1 {
			return
		}
		component, ok := components[path[0]]
		if !ok {
			return
		}
		target, _ := renders[0].(string)
		prefix := "configmap:" + component + "."
		if !strings.HasPrefix(target, prefix) {
			return
		}
		checked++
		require.Equal(t, strings.TrimPrefix(target, prefix), strings.Join(path[1:], "."), "input %s renders %s", strings.Join(path, "."), target)
	}
	walk(schema, nil)
	require.Positive(t, checked)
}
