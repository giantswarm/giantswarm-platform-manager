package agentplatform

import (
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// hiveFragment is the portal-hive shape's Component fragment.
const hiveFragment = "giantswarm/riverbend-management-clusters/management-clusters/otter/extras/backstage/agent-platform/app-config.yaml"

// The Hive is refused where the installation cannot serve it: the portal is
// not its own, its muster registers no grant server or no board server, or
// no plan repository is named. Each refusal names the input.
func TestHiveRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(input map[string]any)
		want   string
	}{
		{"no hosted portal", func(input map[string]any) {
			inst := input["installation"].(map[string]any)
			inst["portals"] = inst["portals"].([]any)[1:]
		}, "no portal hosted on it"},
		{"a sibling's portal", func(input map[string]any) {
			input["installation"].(map[string]any)["portals"].([]any)[0].(map[string]any)["installation"] = "badger"
		}, "no portal hosted on it"},
		{"no grant server", func(input map[string]any) {
			inst := input["installation"].(map[string]any)
			inst["mcpServers"] = inst["mcpServers"].([]any)[1:]
		}, "githubGrant"},
		{"no board server", func(input map[string]any) {
			inst := input["installation"].(map[string]any)
			inst["mcpServers"] = inst["mcpServers"].([]any)[:1]
		}, "mcp-pro"},
		{"no plan repository", func(input map[string]any) {
			input["hive"].(map[string]any)["plans"] = map[string]any{"repositories": []any{}}
		}, "hive.plans.repositories"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapePortalHive)
			tc.change(input)
			_, err := Render(input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

// Off, the fragment carries neither block and includes the shared list
// without the Hive's pages; on a hand-kept portal, the blocks without a list,
// the portal's own list carrying the pages. A server's tool prefix is named
// only where it differs from the server's name.
func TestHiveFragment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(input map[string]any)
		blocks  bool
		list    string
		toolPfx bool
	}{
		{"on", func(map[string]any) {}, true, "#extensionsAgentPlatformHive\n", true},
		{"off", func(input map[string]any) { input["hive"].(map[string]any)["enabled"] = false }, false, "#extensionsAgentPlatform\n", false},
		{"on a hand-kept portal", func(input map[string]any) {
			input["installation"].(map[string]any)["portals"].([]any)[0].(map[string]any)["handKept"] = true
		}, true, "", true},
		{"pro's prefix is its name", func(input map[string]any) {
			input["installation"].(map[string]any)["mcpServers"].([]any)[1].(map[string]any)["toolPrefix"] = "otter-mcp-pro"
		}, true, "#extensionsAgentPlatformHive\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapePortalHive)
			tc.change(input)
			result, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			fragment := string(result.Tree()[hiveFragment])
			if got := strings.Contains(fragment, "    plans:\n") && strings.Contains(fragment, "    roadmap:\n"); got != tc.blocks {
				t.Errorf("plans and roadmap blocks %v, want %v:\n%s", got, tc.blocks, fragment)
			}
			if tc.list == "" && strings.Contains(fragment, "extensions:") || tc.list != "" && !strings.Contains(fragment, "shared-config.yaml"+tc.list) {
				t.Errorf("extension list, want %q:\n%s", tc.list, fragment)
			}
			if got := strings.Contains(fragment, "toolPrefix:"); got != tc.toolPfx {
				t.Errorf("toolPrefix %v, want %v:\n%s", got, tc.toolPfx, fragment)
			}
		})
	}
}
