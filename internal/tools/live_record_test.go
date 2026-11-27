package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

const (
	kubernetesClient = "dex-client-mcp-kubernetes"
	capiClient       = "dex-client-mcp-capi"
)

func TestGeneratedNamesAreTheRenderedValuesByInstallation(t *testing.T) {
	plans := []DryRun{
		{Installation: plan.Installation{Name: rtGarm, GeneratedSecrets: []plan.GeneratedSecret{{Name: kubernetesClient}, {Name: capiClient}}}},
		{Installation: plan.Installation{Name: "glean", GeneratedSecrets: []plan.GeneratedSecret{}}},
	}
	require.Equal(t, map[string][]string{rtGarm: {capiClient, kubernetesClient}}, generatedNames(plans))
	require.Nil(t, generatedNames([]DryRun{{Installation: plan.Installation{Name: rtGarm}}}))
}

func TestLiveRecordTakesTheInstallationsRenderFromTheAction(t *testing.T) {
	a := actions.Action{Spec: actions.Spec{
		DeferredPairs:           map[string][]string{rtGarm: {"federation-gazelle-garm-client-secret"}},
		GeneratedByInstallation: map[string][]string{rtGarm: {kubernetesClient}, "glean": {"dex-client-mcp-prometheus"}},
	}}
	var opts verify.LiveOptions
	liveRecord(&opts, a, rtGarm)
	require.Equal(t, []string{"federation-gazelle-garm-client-secret"}, opts.DeferredPairs)
	require.Equal(t, []string{kubernetesClient}, opts.Generated)

	var legacy verify.LiveOptions
	liveRecord(&legacy, actions.Action{}, rtGarm)
	require.Nil(t, legacy.DeferredPairs)
	require.Nil(t, legacy.Generated)
}
