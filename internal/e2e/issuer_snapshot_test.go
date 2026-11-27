package e2e

import (
	"cmp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

func TestWatchRecordsCommittedIssuersFromTheInstallationsOwnFile(t *testing.T) {
	st := newStack(t)
	action, _ := rolledOut(t, st)
	const path = "installations/rowan/apps/agent-platform/configmap-values.yaml.patch"
	var repository, merge, content string
	for _, pullRequest := range action.Status.PullRequests {
		if !strings.HasSuffix(pullRequest.Repository, "-configs") {
			continue
		}
		repository, merge = pullRequest.Repository, cmp.Or(pullRequest.MergeCommit, pullRequest.HeadSHA)
		content = st.ghs.fileAt(repository, merge, path)
		break
	}
	require.NotEmpty(t, content)
	var values map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(content), &values))
	server := values["muster"].(map[string]any)["muster"].(map[string]any)["oauth"].(map[string]any)["server"].(map[string]any)
	server["trustedIssuers"] = []any{map[string]any{
		"issuer": "https://dex.example.test", "audiences": []any{"issuer-audience"}, "acceptedTypHeaders": []any{"JWT"},
	}}
	encoded, err := yaml.Marshal(values)
	require.NoError(t, err)
	content = string(encoded)
	st.ghs.rewriteAt(repository, merge, path, content)
	st.ghs.addFile(repository, path, content)
	const other = "installations/aaa/apps/agent-platform/configmap-values.yaml.patch"
	st.ghs.rewriteAt(repository, merge, other, "global:\n  domain: another.example.test\n")
	setValues := func(content string) {
		st.inst.edit("ConfigMap", fluxNamespace, platformValuesConfigMap, func(object map[string]any) {
			object[dataKey].(map[string]any)[platformValuesKey] = content
		})
	}
	setValues(content)
	admin := adminLive(t, st)
	result, text, failed := watchCall(t, admin, action.Name)
	require.False(t, failed, "%s", text)
	require.Equal(t, actions.StateEnabled, result.State, "%s", result.Message)
	snapshot, err := plan.TrustedIssuerSnapshot(content)
	require.NoError(t, err)
	stored := storedAction(t, st, action.Name)
	require.Contains(t, stored.KeptOnRecord(rowan), snapshot[0])
	require.Contains(t, stored.Status.KeptByInstallation, rowan)
	require.NotContains(t, text, "https://dex.example.test")

	server["trustedIssuers"].([]any)[0].(map[string]any)["allowPrivateIPJWKS"] = true
	encoded, err = yaml.Marshal(values)
	require.NoError(t, err)
	setValues(string(encoded))
	result, text, failed = watchCall(t, admin, action.Name)
	require.False(t, failed, "%s", text)
	require.Equal(t, actions.StateDrifted, result.State, "%s", result.Message)
	require.Equal(t, stored.KeptOnRecord(rowan), result.Action.KeptOnRecord(rowan))
}
