package installations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The platform tenant's inputs, by the chart paths they write.
const (
	inputPlatformEditors = "tenants.platform.editors"
	inputPlatformTeam    = "tenants.platformTeam"
)

// subject is an RBAC subject as the record carries it.
func subject(apiGroup, kind, name, namespace string) map[string]any {
	s := map[string]any{"kind": kind, "name": name}
	if apiGroup != "" {
		s["apiGroup"] = apiGroup
	}
	if namespace != "" {
		s["namespace"] = namespace
	}
	return s
}

// group is a Group subject.
func group(name string) map[string]any {
	return subject("rbac.authorization.k8s.io", "Group", name, "")
}

// A reconcile types no input: on an installation whose configmap patch
// carries the platform tenant's lists and muster's MCP-server viewers, the
// plan reads each back and renders it again, an empty list as [] — the chart
// takes a removed key as its default, which binds everyone again. The
// golden patches of the retired platform tenant, with and without the
// platform team, read back and rendered again are the same patches, byte for
// byte.
func TestPlatformTenantSurvivesAReconcile(t *testing.T) {
	for shape, want := range map[string]map[string]any{
		"platform-tenant-retired": {
			"tenants.platform.users": []any{}, inputPlatformEditors: []any{}, inputPlatformTeam: []any{},
			"musterMcpServerViewers": []any{group("oidc:example-agent-users")},
		},
		"platform-tenant-team": {
			"tenants.platform.users": []any{}, inputPlatformEditors: []any{},
			inputPlatformTeam: []any{group("oidc:example-platform-team")},
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

// A broad system group on record in the editors or the platform team is
// refused at the reconcile as when it is typed, naming the input and the
// group: a hand edit of the configmap patch does not slip past the check.
func TestPlatformTenantBroadGroupOnRecordIsRefused(t *testing.T) {
	definition, found := FindCapability(AgentPlatform)
	require.True(t, found)
	for _, c := range []struct {
		input, group string
		edit         func(string) string
	}{
		{inputPlatformEditors, "system:serviceaccounts", func(p string) string {
			return strings.Replace(p, "    editors: []\n", "    editors:\n      - apiGroup: rbac.authorization.k8s.io\n        kind: Group\n        name: system:serviceaccounts\n", 1)
		}},
		{inputPlatformTeam, "system:authenticated", func(p string) string {
			return strings.Replace(p, "name: oidc:example-platform-team", "name: system:authenticated", 1)
		}},
	} {
		t.Run(c.input, func(t *testing.T) {
			_, read, doc, _ := inputFromTheRecord(t, "platform-tenant-team", c.edit)
			require.Equal(t, []any{group(c.group)}, read[c.input], "%s read back", c.input)
			_, err := definition.Render(doc, nil, render.ModeCommit)
			require.ErrorContains(t, err, "("+c.input+") is the group "+c.group+",")
		})
	}
}
