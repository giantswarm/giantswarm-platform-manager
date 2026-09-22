package render

import "testing"

// The portal's extension list is one anchor of the fleet's shared config per
// combination: the baseline, with the platform's section, with the AI chat
// on it, with the Grafana dashboards card, and their combinations — named by
// what each adds to the baseline, in that order, so the two definitions agree
// on the name the fleet carries. The chat's suffix follows the platform's:
// no list carries the chat without the platform's section.
func TestPortalExtensionsInclude(t *testing.T) {
	for _, tc := range []struct {
		agentPlatform, aiChat, grafanaWired bool
		want                                string
	}{
		{false, false, false, "shared-config.yaml#extensions"},
		{false, false, true, "shared-config.yaml#extensionsGrafanaDashboards"},
		{false, true, false, "shared-config.yaml#extensions"},
		{true, false, false, "shared-config.yaml#extensionsAgentPlatform"},
		{true, false, true, "shared-config.yaml#extensionsAgentPlatformGrafanaDashboards"},
		{true, true, false, "shared-config.yaml#extensionsAgentPlatformAiChat"},
		{true, true, true, "shared-config.yaml#extensionsAgentPlatformAiChatGrafanaDashboards"},
	} {
		if got := PortalExtensionsInclude(tc.agentPlatform, tc.aiChat, tc.grafanaWired); got != tc.want {
			t.Errorf("platform %v, chat %v, Grafana wired %v: %q, want %q", tc.agentPlatform, tc.aiChat, tc.grafanaWired, got, tc.want)
		}
	}
	if got := PortalSharedInclude("routes"); got != "shared-config.yaml#routes" {
		t.Errorf("shared include %q", got)
	}
}
