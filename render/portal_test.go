package render

import "testing"

// baseExtensions is the baseline list's include.
const baseExtensions = "shared-config.yaml#extensions"

// The portal's extension list is one anchor of the fleet's shared config per
// combination: the baseline, with the platform's section, with the AI chat
// on it, with the Hive pages on it, with the Grafana dashboards card, and
// their combinations — named by what each adds to the baseline, in that
// order, so the two definitions agree on the name the fleet carries. The
// chat's and the Hive's suffixes follow the platform's: no list carries
// either without the platform's section.
func TestPortalExtensionsInclude(t *testing.T) {
	for _, tc := range []struct {
		agentPlatform, aiChat, hive, grafanaWired bool
		want                                      string
	}{
		{false, false, false, false, baseExtensions},
		{false, false, false, true, "shared-config.yaml#extensionsGrafanaDashboards"},
		{false, true, false, false, baseExtensions},
		{false, false, true, false, baseExtensions},
		{true, false, false, false, "shared-config.yaml#extensionsAgentPlatform"},
		{true, false, false, true, "shared-config.yaml#extensionsAgentPlatformGrafanaDashboards"},
		{true, true, false, false, "shared-config.yaml#extensionsAgentPlatformAiChat"},
		{true, true, false, true, "shared-config.yaml#extensionsAgentPlatformAiChatGrafanaDashboards"},
		{true, false, true, false, "shared-config.yaml#extensionsAgentPlatformHive"},
		{true, false, true, true, "shared-config.yaml#extensionsAgentPlatformHiveGrafanaDashboards"},
		{true, true, true, false, "shared-config.yaml#extensionsAgentPlatformAiChatHive"},
		{true, true, true, true, "shared-config.yaml#extensionsAgentPlatformAiChatHiveGrafanaDashboards"},
	} {
		if got := PortalExtensionsInclude(tc.agentPlatform, tc.aiChat, tc.hive, tc.grafanaWired); got != tc.want {
			t.Errorf("platform %v, chat %v, Hive %v, Grafana wired %v: %q, want %q", tc.agentPlatform, tc.aiChat, tc.hive, tc.grafanaWired, got, tc.want)
		}
	}
	if got := PortalSharedInclude("routes"); got != "shared-config.yaml#routes" {
		t.Errorf("shared include %q", got)
	}
}
