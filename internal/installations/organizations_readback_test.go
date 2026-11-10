package installations

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// inputOrganizationsCommit is the organizations' commit mode, read back as
// whether the configmap patch on record names agent-manager's commit
// repository: false wherever the patch is on record without it.
const inputOrganizationsCommit = "organizations.commit.enabled"

// The agent-platform definition reads the organizations' choices back from
// the record, so a reconcile keeps what a person set: the switch and the API
// hostnames from the configmap patch, commit mode from agent-manager's
// commit repository there, the portal's pages from the Component's fragment,
// else from the portal's own app-config (a hand-kept portal carries the key
// there by hand).
func TestAgentPlatformReadsBackOrganizations(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	const fragment = "acme/mcs:management-clusters/rowan/extras/backstage/agent-platform/app-config.yaml"
	const appConfig = "acme/mcs:management-clusters/rowan/extras/backstage/backstage/app-config.yaml"
	commit := "agentPlatformOrganizations:\n  enabled: true\n  apiHostnames:\n    - internal-api.rowan.acme.test\n  fluxNamespace: flux-giantswarm\n" +
		"agent-manager:\n  tenants:\n    commit:\n      repository: acme/acme-management-clusters\n      branch: main\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  map[string]any
	}{
		{"commit mode with the API hostnames", map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): commit}, map[string]any{
			"organizations.enabled": true, "organizations.apiHostnames": []any{"internal-api.rowan.acme.test"}, inputOrganizationsCommit: true}},
		{"apply mode", map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): "agentPlatformOrganizations:\n  enabled: true\n"}, map[string]any{
			"organizations.enabled": true, inputOrganizationsCommit: false}},
		{"the portal's pages in the fragment", map[string]string{
			fragment: "data:\n  app-config.agent-platform.yaml: |\n    agentPlatform:\n      organizations:\n        enabled: true\n",
		}, map[string]any{"organizations.portal.enabled": true}},
		{"the portal's pages by hand in a hand-kept portal", map[string]string{
			appConfig: "data:\n  values: |\n    backstage:\n      appConfig:\n        agentPlatform:\n          organizations:\n            enabled: true\n",
		}, map[string]any{"organizations.portal.enabled": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := def.ReadBack(context.Background(), files(tc.files), readBackInstallation, readBackName, nil)
			if err != nil {
				t.Fatal(err)
			}
			// The other portal choices read back beside them are their own tests'.
			for input := range got {
				if !strings.HasPrefix(input, "organizations.") {
					delete(got, input)
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("read back %v, want %v", got, tc.want)
			}
		})
	}
}
