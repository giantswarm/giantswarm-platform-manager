package tools

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestDisabledRelayRejectsContinuationBeforeHandler(t *testing.T) {
	called := false
	next := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		called = true
		return mcp.NewToolResultText("ran"), nil
	}
	request := mcp.CallToolRequest{}
	request.Params.Name = ToolMergeAction
	request.Params.RequestState = "previously-issued-state"
	result, err := (&Tools{}).relayURLElicitations(next)(t.Context(), request)
	require.Error(t, err)
	require.Nil(t, result)
	require.False(t, called)
	var protocolError interface{ JSONRPCError() mcp.JSONRPCError }
	require.ErrorAs(t, err, &protocolError)
	require.Equal(t, mcp.INVALID_PARAMS, protocolError.JSONRPCError().Error.Code)
}

func TestRequestStateStaysOnSupportedTools(t *testing.T) {
	const sealedState = "sealed-state"
	for _, testCase := range []struct {
		name     string
		tool     string
		state    string
		rejected bool
	}{
		{name: "ordinary write", tool: ToolDenyAction},
		{name: "rerouted continuation", tool: ToolDenyAction, state: sealedState, rejected: true},
		{name: "verify continuation", tool: ToolVerifyInstallation, state: sealedState},
		{name: "merge continuation", tool: ToolMergeAction, state: sealedState},
		{name: "watch continuation", tool: ToolWatchAction, state: sealedState},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			called := false
			next := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				called = true
				return mcp.NewToolResultText("ran"), nil
			}
			request := mcp.CallToolRequest{}
			request.Params.Name = testCase.tool
			request.Params.RequestState = testCase.state
			result, err := rejectUnsupportedRequestState(next)(t.Context(), request)
			require.Equal(t, !testCase.rejected, called)
			if !testCase.rejected {
				require.NoError(t, err)
				require.NotNil(t, result)
				return
			}
			require.Nil(t, result)
			var protocolError interface{ JSONRPCError() mcp.JSONRPCError }
			require.ErrorAs(t, err, &protocolError)
			require.Equal(t, mcp.INVALID_PARAMS, protocolError.JSONRPCError().Error.Code)
		})
	}
}
