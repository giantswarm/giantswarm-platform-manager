package tools

import (
	"encoding/json"
	"testing"

	"github.com/giantswarm/mcp-toolkit/brand"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestMCPServerAdvertisesGiantSwarmIcon pins the logo muster stores as the
// connector's icon: the initialize result carries the Giant Swarm logo.
func TestMCPServerAdvertisesGiantSwarmIcon(t *testing.T) {
	initReq := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
	raw, err := json.Marshal(New(Deps{}).MCPServer().HandleMessage(t.Context(), initReq))
	require.NoError(t, err)

	var resp struct {
		Result mcp.InitializeResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(raw, &resp))
	require.Equal(t, []mcp.Icon{brand.GiantSwarmIcon()}, resp.Result.ServerInfo.Icons)
}
