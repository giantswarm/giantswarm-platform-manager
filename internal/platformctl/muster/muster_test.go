package muster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// fakeMuster is an aggregator as the bridge shows it: the manager's tools
// under x_<server>_ when connected, hidden when not, and core_auth_login
// answering a challenge in the latter case.
func fakeMuster(connected bool) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("muster", "test")
	if connected {
		s.AddTool(mcp.NewTool("x_"+Server+"_get_info"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText(`{"version": "0.7.0"}`), nil
		})
		s.AddTool(mcp.NewTool("x_"+Server+"_list_installations"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultError("list_installations needs a caller"), nil
		})
	}
	s.AddTool(mcp.NewTool("core_auth_login"), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.GetString("server", "") != Server {
			return mcp.NewToolResultError("unknown server"), nil
		}
		if connected {
			return mcp.NewToolResultText("Already authenticated to " + Server), nil
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{mcp.NewTextContent("Authentication Required\n\nServer: " + Server)},
			StructuredContent: map[string]any{"authUrl": "https://example.test/login"},
		}, nil
	})
	return s
}

func session(t *testing.T, connected bool) *Session {
	t.Helper()
	c, err := client.NewInProcessClient(fakeMuster(connected))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCallAnswersTheToolsJSON(t *testing.T) {
	s := session(t, true)
	raw, err := s.Call(context.Background(), "get_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Version string }
	if err := json.Unmarshal(raw, &got); err != nil || got.Version != "0.7.0" {
		t.Fatalf("got %s, %v", raw, err)
	}
}

func TestCallReportsTheToolsRefusal(t *testing.T) {
	s := session(t, true)
	_, err := s.Call(context.Background(), "list_installations", map[string]any{"customer": "x"})
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "list_installations" || te.Message != "list_installations needs a caller" {
		t.Fatalf("got %v", err)
	}
	if _, ok := IsAuthRequired(err); ok {
		t.Fatal("a connected server's refusal is not a sign-in")
	}
}

func TestCallOfAnUnconnectedServerIsASignIn(t *testing.T) {
	s := session(t, false)
	_, err := s.Call(context.Background(), "get_info", nil)
	auth, ok := IsAuthRequired(err)
	if !ok {
		t.Fatalf("got %v", err)
	}
	if auth.URL != "https://example.test/login" || auth.Server != Server {
		t.Fatalf("got %+v", auth)
	}
}

func TestOpenNamesTheMissingBinary(t *testing.T) {
	_, err := Open(context.Background(), Options{Binary: "muster-that-is-not-installed"})
	if err == nil || !strings.Contains(err.Error(), "muster-that-is-not-installed") || !strings.Contains(err.Error(), "--muster") {
		t.Fatalf("got %v", err)
	}
}
