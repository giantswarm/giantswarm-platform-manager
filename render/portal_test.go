package render

import "testing"

// The portal's extension list is one anchor of the fleet's shared config per
// combination: the baseline, with the platform's section, with the Grafana
// dashboards card, with both — named by what each adds to the baseline, in
// that order, so the two definitions agree on the name the fleet carries.
func TestPortalExtensionsInclude(t *testing.T) {
	for _, tc := range []struct {
		agentPlatform, grafanaWired bool
		want                        string
	}{
		{false, false, "shared-config.yaml#extensions"},
		{false, true, "shared-config.yaml#extensionsGrafanaDashboards"},
		{true, false, "shared-config.yaml#extensionsAgentPlatform"},
		{true, true, "shared-config.yaml#extensionsAgentPlatformGrafanaDashboards"},
	} {
		if got := PortalExtensionsInclude(tc.agentPlatform, tc.grafanaWired); got != tc.want {
			t.Errorf("platform %v, Grafana wired %v: %q, want %q", tc.agentPlatform, tc.grafanaWired, got, tc.want)
		}
	}
	if got := PortalSharedInclude("routes"); got != "shared-config.yaml#routes" {
		t.Errorf("shared include %q", got)
	}
}
