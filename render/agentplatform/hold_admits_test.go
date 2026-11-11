package agentplatform

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHoldAdmits(t *testing.T) {
	const floor = "4.175.0-rc.21"
	for _, c := range []struct {
		hold string
		want bool
	}{
		{"", true},
		{"4.178.0", true},
		{"4.174.3", false},
		{"4.175.0-rc.21", true},
		{">=4.0.0 <5.0.0", true},
		{">=4.0.0-0 <5.0.0-0", true},
		{">=4.170.0 <4.176.0", true},
		{"<4.174.0", false},
		{">=4.170.0 <4.174.0", false},
		{">=4.178.0 <4.179.0", true},
		{">=4.178.0-0 <4.179.0-0", true},
		{">4.178.0 <4.179.0", true},
		{"~4.178.0", true},
		{"4.178.x", true},
		{"~4.174.0", false},
		{"not a constraint", true},
	} {
		t.Run(c.hold, func(t *testing.T) {
			require.Equal(t, c.want, holdAdmits(c.hold, floor))
		})
	}
}

func TestBoundedHoldAboveTheModelFreeChartDeclaresNoModels(t *testing.T) {
	input, _ := loadInput(t, shapeGiantswarmSlackAppPub)
	input[keyVersions] = map[string]any{keyChart: ">=4.178.0 <4.179.0"}
	in, err := Parse(input)
	require.NoError(t, err)
	require.False(t, in.declaresModels())
}
