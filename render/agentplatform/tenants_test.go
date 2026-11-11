package agentplatform

import (
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The keys of the platform tenant's choices in the input document.
const (
	keyTenants  = "tenants"
	keyPlatform = "platform"
	keyUsers    = "users"
	keyViewers  = "musterMcpServerViewers"
)

// subject is an RBAC subject as a document carries it.
func subject(apiGroup, kind, name string) map[string]any {
	s := map[string]any{fieldKind: kind, fieldName: name}
	if apiGroup != "" {
		s["apiGroup"] = apiGroup
	}
	return s
}

// platformUsers sets tenants.platform.users in a document.
func platformUsers(m map[string]any, users []any) {
	m[keyTenants] = map[string]any{keyPlatform: map[string]any{keyUsers: users}}
}

// TestPlatformTenantRefusals refuses the platform tenant's lists and
// muster's MCP-server viewers where the meta chart or the API server would
// fail the release, naming the input: no viewer with the organizations on
// (the chart's guard, whether the viewers or the users they fall back to are
// empty), a list off the 4 chart line, under a hold of the meta chart before
// the release that takes its key, the platform tenant's own lists where no
// kagent runs, and a subject a RoleBinding cannot carry.
func TestPlatformTenantRefusals(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(m map[string]any)
		names  string
	}{
		{"the organizations with the viewers empty", func(m map[string]any) {
			m[keyViewers] = []any{}
		}, "(musterMcpServerViewers) are an empty list"},
		{"the organizations with the users empty and the viewers unset", func(m map[string]any) {
			delete(m, keyViewers)
			platformUsers(m, []any{})
		}, "(tenants.platform.users) are an empty list while"},
		{"the users on the 3 line", func(m map[string]any) {
			delete(m, keyOrganizations)
			m["installation"].(map[string]any)["customer"] = testOrganisation
			m["installation"].(map[string]any)["chartLine"] = lineThree
		}, "(tenants.platform.users) is the 4 chart line's"},
		{"the viewers under a hold before the release that takes them", func(m map[string]any) {
			m["versions"] = map[string]any{keyChart: "4.178.0-rc.3"}
		}, "(musterMcpServerViewers) needs the meta chart 4.178.0-rc.4"},
		{"the users under a hold before the release that takes them", func(m map[string]any) {
			delete(m, keyViewers)
			delete(m, keyOrganizations)
			m["versions"] = map[string]any{keyChart: "4.131.1"}
		}, "(tenants.platform.users) needs the meta chart 4.132.0-rc.1"},
		{"the users where no kagent runs", func(m map[string]any) {
			delete(m, keyViewers)
			delete(m, keyOrganizations)
			m["installation"].(map[string]any)["customer"] = customerWithoutKagent
		}, "(tenants.platform.users) is bound in kagent's namespace"},
		{"the editors where no kagent runs", func(m map[string]any) {
			delete(m, keyViewers)
			delete(m, keyOrganizations)
			m[keyTenants] = map[string]any{keyPlatform: map[string]any{"editors": []any{}}}
			m["installation"].(map[string]any)["customer"] = customerWithoutKagent
		}, "(tenants.platform.editors) is bound in kagent's namespace"},
		{"a group in another API group", func(m map[string]any) {
			m[keyViewers] = []any{subject("example.io", subjectGroup, "oidc:example")}
		}, "entry 0 of muster's MCP-server viewers (musterMcpServerViewers) is a subject the API server refuses"},
		{"a ServiceAccount with an API group", func(m map[string]any) {
			m[keyViewers] = []any{subject(rbacGroup, subjectServiceAccount, "runner")}
		}, "a ServiceAccount's apiGroup is empty"},
		{"a ServiceAccount whose name is none", func(m map[string]any) {
			m[keyViewers] = []any{subject("", subjectServiceAccount, "Runner")}
		}, "no ServiceAccount's name"},
		{"a kind RBAC has not", func(m map[string]any) {
			m[keyViewers] = []any{subject("", "Role", "viewer")}
		}, "/musterMcpServerViewers/0/kind"},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapePlatformTenantRetired)
			c.mutate(input)
			_, err := Render(input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("got %v, want ErrInput naming %s", err, c.names)
			}
		})
	}
}

// TestPlatformTenantStands renders what the meta chart accepts: everyone
// unbound with the organizations off, the viewers' release or a range that
// reaches it, a group without its API group (the API server defaults it),
// and the users falling back to the chart's default with the organizations
// on. A list typed null is not written, so the chart's default comes back.
func TestPlatformTenantStands(t *testing.T) {
	for _, c := range []struct {
		name   string
		mutate func(m map[string]any)
	}{
		{"nobody bound with the organizations off", func(m map[string]any) {
			delete(m, keyOrganizations)
			m[keyViewers] = []any{}
		}},
		{"a hold on the viewers' release", func(m map[string]any) { m["versions"] = map[string]any{keyChart: "4.178.0-rc.4"} }},
		{"a hold on a range that reaches it", func(m map[string]any) { m["versions"] = map[string]any{keyChart: chartRangeFour} }},
		{"a group without its API group", func(m map[string]any) {
			m[keyViewers] = []any{subject("", subjectGroup, "oidc:example")}
		}},
		{"the users typed null, the viewers unset", func(m map[string]any) {
			delete(m, keyViewers)
			platformUsers(m, nil)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			input, secrets := loadInput(t, shapePlatformTenantRetired)
			c.mutate(input)
			if _, err := Render(input, secrets, render.ModeCommit); err != nil {
				t.Fatal(err)
			}
		})
	}
	input, _ := loadInput(t, shapePlatformTenantRetired)
	input[keyViewers] = nil
	input[keyTenants] = map[string]any{keyPlatform: map[string]any{keyUsers: nil, "editors": nil}, "platformTeam": nil}
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	var patch map[string]any
	if err := yaml.Unmarshal(render.MustYAML(in.configmapPatch()), &patch); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{keyTenants, keyViewers} {
		if _, written := patch[key]; written {
			t.Errorf("lists typed null write %s: %v", key, patch[key])
		}
	}
}
