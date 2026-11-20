package verify

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestExactBrowserConnectorComparison(t *testing.T) {
	const (
		valuesPath = "muster.muster.oauth.server.dex.identityCheckConnectors"
		livePath   = "data.config.yaml:aggregator.oauth.server.dex.identityCheckConnectors"
		both       = "[entra, staff-hub]"
	)
	for _, testCase := range []struct {
		name     string
		expected string
		live     string
		exact    bool
		want     Mark
	}{
		{name: "same set", expected: both, live: "[staff-hub, entra]", exact: true, want: AsDefined},
		{name: "missing connector", expected: both, live: "[entra]", exact: true, want: Drifted},
		{name: "unexpected legacy connector", expected: both, live: "[entra, staff-hub, legacy]", exact: true, want: Drifted},
		{name: "subset comparison retains shared extras", expected: both, live: "[entra, staff-hub, legacy]", want: AsDefined},
		{name: "empty omitted", expected: "[]", exact: true, want: AsDefined},
		{name: "empty explicit", expected: "[]", live: "[]", exact: true, want: AsDefined},
		{name: "empty with unexpected connector", expected: "[]", live: "[legacy]", exact: true, want: Drifted},
		{name: "nonempty omitted", expected: both, exact: true, want: Drifted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			expected := "muster:\n  muster:\n    oauth:\n      server:\n        dex:\n          identityCheckConnectors: " + testCase.expected + "\n"
			live := "aggregator:\n  oauth:\n    server:\n      dex: {}\n"
			if testCase.live != "" {
				live = "aggregator:\n  oauth:\n    server:\n      dex:\n        identityCheckConnectors: " + testCase.live + "\n"
			}
			cluster := &recordingCluster{objects: map[string]map[string]any{
				kindConfigMap + "/" + testNamespace + "/" + testRelease: {keyData: map[string]any{"config.yaml": live}},
			}}
			probe := render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: kindConfigMap, Name: testRelease,
				Expect: render.Expectation{Compare: []render.Comparison{{Live: livePath, Rendered: valuesPath, Exact: testCase.exact}}}}
			executor := &executor{opts: LiveOptions{Cluster: cluster}, lv: &liveRender{values: flattenYAML(expected)}}
			check, differences, authRequired := executor.run(t.Context(), probe)
			require.Nil(t, authRequired)
			require.Equal(t, testCase.want, check.Mark, "%+v", differences)
		})
	}
}
