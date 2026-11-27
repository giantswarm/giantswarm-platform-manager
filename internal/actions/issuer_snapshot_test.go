package actions

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

func TestKeptOnRecordCombinesSpecIdentifiersAndStatusSnapshots(t *testing.T) {
	const installation = "rowan"
	kept := []plan.Kept{{List: plan.ListExtraAudience, Entry: "other-client"}}
	action := Action{Spec: Spec{KeptByInstallation: map[string][]plan.Kept{installation: kept}}}
	require.Equal(t, kept, action.KeptOnRecord(installation))
	snapshot := []plan.Kept{{List: plan.ListTrustedIssuers, Digest: "sha256:recorded"}}
	action.Status.KeptByInstallation = map[string][]plan.Kept{installation: snapshot}
	require.Equal(t, append(append([]plan.Kept{}, kept...), snapshot...), action.KeptOnRecord(installation))
	action.Status.KeptByInstallation[installation] = []plan.Kept{}
	require.Equal(t, kept, action.KeptOnRecord(installation))
	require.Nil(t, action.KeptOnRecord("other"))
}
