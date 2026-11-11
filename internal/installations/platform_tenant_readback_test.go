package installations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A reconcile types no input: on an installation whose configmap patch
// carries the platform tenant's lists and muster's MCP-server viewers, the
// plan reads each back and renders it again, an empty list as [] — the chart
// takes a removed key as its default, which binds everyone again. The
// golden patches of the retired platform tenant, with and without the
// platform team, read back and rendered again are the same patches, byte for
// byte.
func TestPlatformTenantSurvivesAReconcile(t *testing.T) {
	subject := func(apiGroup, kind, name, namespace string) map[string]any {
		s := map[string]any{"kind": kind, "name": name}
		if apiGroup != "" {
			s["apiGroup"] = apiGroup
		}
		if namespace != "" {
			s["namespace"] = namespace
		}
		return s
	}
	group := func(name string) map[string]any { return subject("rbac.authorization.k8s.io", "Group", name, "") }
	for shape, want := range map[string]map[string]any{
		"platform-tenant-retired": {
			"tenants.platform.users": []any{}, "tenants.platform.editors": []any{}, "tenants.platformTeam": []any{},
			"musterMcpServerViewers": []any{group("oidc:example-agent-users")},
		},
		"platform-tenant-team": {
			"tenants.platform.users": []any{}, "tenants.platform.editors": []any{},
			"tenants.platformTeam": []any{group("oidc:example-platform-team")},
			"musterMcpServerViewers": []any{group("oidc:example-agent-users"),
				subject("", "ServiceAccount", "example-agent-runner", "example-agents")},
		},
	} {
		t.Run(shape, func(t *testing.T) {
			patch, read, again := renderGoldenFromTheRecord(t, shape)
			for input, value := range want {
				require.Equal(t, value, read[input], "%s read back", input)
			}
			require.Equal(t, patch, again)
		})
	}
}
