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

// organizations is an organizations input document: the switch, the API
// hostnames and commit mode, each left out where nil.
func organizations(enabled, commit any, hostnames []any) map[string]any {
	o := map[string]any{}
	if enabled != nil {
		o[keyEnabled] = enabled
	}
	if commit != nil {
		o["commit"] = map[string]any{keyEnabled: commit}
	}
	if hostnames != nil {
		o["apiHostnames"] = hostnames
	}
	return map[string]any{"organizations": o}
}

// inputCredentialUI is agent-manager's credential form, which renders with
// the organizations' switch only; formAddress a form's address.
const (
	inputCredentialUI = "agentManager.credentials.uiURL"
	formAddress       = "https://portal.acme.test/credentials/{operation}"
)

// credentialForm adds the credential form's address to an input document.
func credentialForm(doc map[string]any, address string) map[string]any {
	doc["agentManager"] = map[string]any{"credentials": map[string]any{"uiURL": address}}
	return doc
}

// The agent-platform definition refuses commit mode, API hostnames or the
// credential form typed while the switch is off, naming the switch; the same
// values read back from the record, typed with the switch on, or typed at
// their defaults stand, so a person turns the switch off alone or with its
// dependents; a switch typed as no boolean is left to the schema to refuse.
func TestAgentPlatformRefusesADependentTypedWithoutItsSwitch(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	hostnames := []any{"internal-api.rowan.acme.test"}
	for _, tc := range []struct {
		name          string
		typed, values map[string]any
		refused       string
	}{
		{"commit mode typed, the switch off", organizations(nil, true, nil), organizations(false, true, nil), inputOrganizationsCommit},
		{"API hostnames typed, the switch off", organizations(nil, nil, hostnames), organizations(false, false, hostnames), "organizations.apiHostnames"},
		{"commit mode typed with the switch", organizations(true, true, nil), organizations(true, true, nil), ""},
		{"commit mode and API hostnames read back, the switch typed off", organizations(false, nil, nil), organizations(false, true, hostnames), ""},
		{"commit mode and API hostnames typed off with the switch", organizations(false, false, []any{}), organizations(false, false, []any{}), ""},
		{"commit mode typed, the switch typed as no boolean", organizations("true", true, nil), organizations("true", true, nil), ""},
		{"the credential form typed, the switch off", credentialForm(organizations(nil, nil, nil), formAddress), credentialForm(organizations(false, false, nil), formAddress), inputCredentialUI},
		{"the credential form read back, the switch typed off", organizations(false, nil, nil), credentialForm(organizations(false, false, nil), formAddress), ""},
		{"the credential form typed empty with the switch off", credentialForm(organizations(false, nil, nil), ""), credentialForm(organizations(false, false, nil), ""), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := def.TypedWithoutSwitch(tc.typed, tc.values)
			if err != nil {
				t.Fatal(err)
			}
			if tc.refused == "" && got != "" {
				t.Errorf("refused: %s", got)
			}
			if tc.refused != "" && (!strings.Contains(got, tc.refused) || !strings.Contains(got, "type organizations.enabled: true")) {
				t.Errorf("got %q, want a refusal naming %s and the switch", got, tc.refused)
			}
		})
	}
}
