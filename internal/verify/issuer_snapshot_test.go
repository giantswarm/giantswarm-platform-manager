package verify

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	snapshotIssuerKey        = "issuer"
	snapshotAcceptedTypesKey = "acceptedTypHeaders"
	snapshotAudiencesKey     = "audiences"
)

func TestRecordedIssuerDigestIsNotDrivenByPrivateFacts(t *testing.T) {
	inputs := fixtureInput(t, "../../render/agentplatform/testdata/hub-private-target/input.yaml")
	inputs[installationKey].(map[string]any)["private"] = true
	definition, ok := installations.FindCapability(installations.AgentPlatform)
	require.True(t, ok)
	digest, err := plan.ConfigurationDigest([]any{map[string]any{snapshotIssuerKey: "https://dex.example.test"}})
	require.NoError(t, err)
	live, err := renderLive(LiveOptions{
		Definition: definition, Inputs: Inputs{Values: inputs, Typed: map[string]any{installationKey: map[string]any{"private": true}}},
		Kept: []plan.Kept{{List: plan.ListTrustedIssuers, Digest: digest}},
	})
	require.NoError(t, err)
	require.Equal(t, digest, live.values[plan.ListTrustedIssuers])
	require.Empty(t, live.driven[valuesKey+"#"+plan.ListTrustedIssuers])
}

func TestIssuerSnapshotChecksTheWholeLiveList(t *testing.T) {
	const issuer = "https://dex.example.test"
	committed := []any{map[string]any{snapshotIssuerKey: issuer, snapshotAudiencesKey: []any{clientMuster}, snapshotAcceptedTypesKey: []any{"JWT"}}}
	digest, err := plan.ConfigurationDigest(committed)
	require.NoError(t, err)
	kept := []plan.Kept{{List: plan.ListTrustedIssuers, Digest: digest}}
	values := map[string]string{
		plan.ListTrustedIssuers + "[0].issuer":             issuer,
		plan.ListTrustedIssuers + "[0].allowPrivateIPJWKS": strconv.FormatBool(true),
		plan.ListTrustedIssuers + "[0].acceptedTypHeaders": "[]",
	}
	withKept(values, kept, ValuesSuffix("agent-platform"))
	require.Equal(t, map[string]string{plan.ListTrustedIssuers: digest}, values)
	for _, test := range []struct {
		name    string
		issuers any
		mark    Mark
	}{
		{name: "recorded list", issuers: committed, mark: AsDefined},
		{name: "extra permission", issuers: []any{map[string]any{snapshotIssuerKey: issuer, snapshotAudiencesKey: []any{clientMuster}, snapshotAcceptedTypesKey: []any{"JWT"}, "allowPrivateIPJWKS": true}}, mark: Drifted},
		{name: "changed token type", issuers: []any{map[string]any{snapshotIssuerKey: issuer, snapshotAudiencesKey: []any{clientMuster}, snapshotAcceptedTypesKey: []any{"at+jwt"}}}, mark: Drifted},
		{name: "extra issuer", issuers: append(append([]any{}, committed...), map[string]any{snapshotIssuerKey: "https://other.example.test"}), mark: Drifted},
		{name: "empty list", issuers: []any{}, mark: Drifted},
		{name: "missing list", mark: Drifted},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := map[string]any{}
			if test.issuers != nil {
				server["trustedIssuers"] = test.issuers
			}
			liveValues := map[string]any{clientMuster: map[string]any{clientMuster: map[string]any{"oauth": map[string]any{"server": server}}}}
			cluster := &recordingCluster{objects: map[string]map[string]any{
				kindHelmRelease + "/" + testNamespace + "/" + testRelease: {keySpec: map[string]any{valuesKey: liveValues}},
			}}
			whole := render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: kindHelmRelease, Name: testRelease}
			selected := whole
			selected.Expect.Compare = []render.Comparison{{Live: "spec.values." + plan.ListTrustedIssuers, Rendered: plan.ListTrustedIssuers}}
			for _, probe := range []render.Probe{whole, selected} {
				execution := &executor{opts: LiveOptions{Cluster: cluster, Kept: kept}, lv: &liveRender{values: values}}
				check, differences, authRequired := execution.run(t.Context(), probe)
				require.Nil(t, authRequired)
				require.Equal(t, test.mark, check.Mark, "%s: %+v", check.Message, differences)
				for _, difference := range differences {
					require.Equal(t, plan.ListTrustedIssuers, difference.Path)
					require.Empty(t, difference.Input)
					require.NotContains(t, difference.Current, issuer)
				}
			}
		})
	}
}
