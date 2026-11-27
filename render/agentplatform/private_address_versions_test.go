package agentplatform

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrivateAddressVersionHolds(t *testing.T) {
	const (
		privateChartRange = ">=4.185.0-0"
		publicChartRange  = ">=4.185.0"
		oldChart          = "4.184.0"
		oldMuster         = "5.63.24"
		oldKubernetes     = "1.15.1"
		musterHoldInput   = "versions.components.muster"
	)
	for _, testCase := range []struct {
		name         string
		shape        string
		chart        string
		muster       string
		kubernetes   string
		wantRefusal  string
		wantChartMin string
	}{
		{name: "private defaults", shape: shapeGiantswarmSlackApp, wantChartMin: privateChartRange},
		{name: "private old chart", shape: shapeGiantswarmSlackApp, chart: oldChart, wantRefusal: inputChartHold},
		{name: "private capped chart", shape: shapeGiantswarmSlackApp, chart: ">=4.0.0 <4.185.0", wantRefusal: inputChartHold},
		{name: "private current chart", shape: shapeGiantswarmSlackApp, chart: "4.185.0", wantChartMin: privateChartRange},
		{name: "private current chart candidate", shape: shapeGiantswarmSlackApp, chart: "4.185.0-rc.1", wantChartMin: privateChartRange},
		{name: "private old muster", shape: shapeGiantswarmSlackApp, muster: oldMuster, wantRefusal: musterHoldInput},
		{name: "private capped muster", shape: shapeGiantswarmSlackApp, muster: ">=5.63.2 <5.64.0", wantRefusal: musterHoldInput},
		{name: "private current muster", shape: shapeGiantswarmSlackApp, muster: ">=5.64.0 <6.0.0", wantChartMin: privateChartRange},
		{name: "private old kubernetes", shape: shapeGiantswarmSlackApp, kubernetes: oldKubernetes, wantRefusal: "versions.components.mcp-kubernetes"},
		{name: "private capped kubernetes", shape: shapeGiantswarmSlackApp, kubernetes: "~1.15.0", wantRefusal: "versions.components.mcp-kubernetes"},
		{name: "private current kubernetes", shape: shapeGiantswarmSlackApp, kubernetes: ">=1.16.0 <2.0.0", wantChartMin: privateChartRange},
		{name: "public private target defaults", shape: shapeHubPrivateTarget, wantChartMin: publicChartRange},
		{name: "public private target old muster", shape: shapeHubPrivateTarget, muster: oldMuster, wantRefusal: musterHoldInput},
		{name: "public private target old chart", shape: shapeHubPrivateTarget, chart: oldChart, wantRefusal: inputChartHold},
		{name: "public registered exchange defaults", shape: shapeRegisteredServers, wantChartMin: publicChartRange},
		{name: "public registered exchange old muster", shape: shapeRegisteredServers, muster: oldMuster, wantRefusal: musterHoldInput},
		{name: "public registered exchange capped muster", shape: shapeRegisteredServers, muster: "~5.63.0", wantRefusal: musterHoldInput},
		{name: "public registered exchange current muster", shape: shapeRegisteredServers, muster: "5.64.0", wantChartMin: publicChartRange},
		{name: "public registered exchange old kubernetes", shape: shapeRegisteredServers, kubernetes: oldKubernetes, wantChartMin: publicChartRange},
		{name: "public registered exchange old chart", shape: shapeRegisteredServers, chart: oldChart, wantRefusal: inputChartHold},
		{name: "public old components", shape: shapeGiantswarmSlackAppPub, chart: oldChart, muster: oldMuster, kubernetes: oldKubernetes, wantChartMin: ">=4.0.0-0"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			input, _ := loadInput(t, testCase.shape)
			input[keyVersions] = map[string]any{
				keyChart: testCase.chart,
				keyComponents: map[string]any{
					componentMuster:        testCase.muster,
					componentMCPKubernetes: testCase.kubernetes,
				},
			}
			configuration, err := Parse(input)
			if testCase.wantRefusal != "" {
				require.ErrorIs(t, err, ErrInput)
				require.ErrorContains(t, err, testCase.wantRefusal)
				return
			}
			require.NoError(t, err)
			require.Contains(t, configuration.chartSemver(), testCase.wantChartMin)
		})
	}
}

func TestPrivateAddressVersionsRefuseLineThree(t *testing.T) {
	for _, shape := range []string{shapeGiantswarmSlackApp, shapeHubPrivateTarget, shapeRegisteredServers} {
		t.Run(shape, func(t *testing.T) {
			input, _ := loadInput(t, shape)
			configuration, err := Parse(input)
			require.NoError(t, err)
			configuration.Installation.ChartLine = "3"
			require.ErrorContains(t, configuration.checkPrivateAddressVersions(), "3 chart line")
		})
	}
}

func publicFederationTargets(input map[string]any) {
	installation := input["installation"].(map[string]any)
	installation["private"] = false
	federation := installation["federation"].(map[string]any)
	for _, target := range federation["targets"].([]any) {
		target.(map[string]any)["private"] = false
	}
}
