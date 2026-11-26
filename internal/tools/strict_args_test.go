package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestUnknownArgumentRefusedBeforeRender pins that a write tool refuses an
// argument its schema does not name instead of dropping it: a reconcile
// called with targets (not installations) would otherwise render the whole
// fleet. A named argument sent as null stays accepted, and the free-form
// inputs object keeps any key.
func TestUnknownArgumentRefusedBeforeRender(t *testing.T) {
	tools := New(Deps{})
	rendered := 0
	for i := range tools.writes {
		if tools.writes[i].Name == ToolReconcileCapability {
			tools.writes[i].DryRun = func(context.Context, map[string]any) (any, error) {
				rendered++
				return "rendered", nil
			}
		}
	}
	s := tools.MCPServer()

	call := func(args map[string]any) mcp.CallToolResult {
		t.Helper()
		req, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": ToolReconcileCapability, "arguments": args},
		})
		require.NoError(t, err)
		raw, err := json.Marshal(s.HandleMessage(t.Context(), req))
		require.NoError(t, err)
		var resp struct {
			Result mcp.CallToolResult `json:"result"`
		}
		require.NoError(t, json.Unmarshal(raw, &resp))
		return resp.Result
	}

	refused := call(map[string]any{"dryRun": true, "targets": []string{"alba"}})
	require.True(t, refused.IsError)
	require.Contains(t, refused.Content[0].(mcp.TextContent).Text, "targets")
	require.Zero(t, rendered)

	accepted := call(map[string]any{"dryRun": true, "installations": []string{"alba"}, "rotate": nil, "inputs": map[string]any{"anyKey": true}})
	require.False(t, accepted.IsError)
	require.Equal(t, 1, rendered)

	apply := call(map[string]any{"mode": "apply", "targets": []string{"alba"}})
	require.True(t, apply.IsError)
	require.Contains(t, apply.Content[0].(mcp.TextContent).Text, ApplyRefusal)
	require.Equal(t, 1, rendered)
}
