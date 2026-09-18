package muster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster/mustertest"
)

func bridge(t *testing.T, connected bool) *client.Client {
	t.Helper()
	c, err := client.NewInProcessClient(mustertest.Bridge(mustertest.Manager(connected)))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func session(t *testing.T, connected bool) *Session {
	t.Helper()
	s, err := New(context.Background(), bridge(t, connected))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// The double is the bridge as muster runs it: meta tools only, the manager's
// tools not callable by name — what platformctl v0.17.3 tried and failed on.
func TestTheBridgeExposesOnlyMetaTools(t *testing.T) {
	c := bridge(t, true)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Initialize(ctx, mcp.InitializeRequest{}); err != nil {
		t.Fatal(err)
	}
	list, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) == 0 {
		t.Fatal("the bridge lists no tools")
	}
	for _, tool := range list.Tools {
		if strings.HasPrefix(tool.Name, "x_") || strings.HasPrefix(tool.Name, "core_") {
			t.Fatalf("the bridge exposes an aggregator tool by name: %s", tool.Name)
		}
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "x_" + Server + "_get_info"
	if _, err := c.CallTool(ctx, req); err == nil {
		t.Fatal("the manager's tool is callable by name on the bridge; the double does not model muster's bridge")
	}
}

func TestCallAnswersTheToolsJSON(t *testing.T) {
	s := session(t, true)
	raw, err := s.Call(context.Background(), "get_info", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != mustertest.Info {
		t.Fatalf("got %s, want the tool's document as it is", raw)
	}
}

func TestCallSendsTheArgumentsToTheTool(t *testing.T) {
	s := session(t, true)
	raw, err := s.Call(context.Background(), "enable_capability", map[string]any{"capability": "agent-platform", "dryRun": true})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Capability string `json:"capability"`
		DryRun     bool   `json:"dryRun"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.Capability != "agent-platform" || !got.DryRun {
		t.Fatalf("got %s, %v", raw, err)
	}
}

func TestCallReportsTheToolsRefusal(t *testing.T) {
	s := session(t, true)
	_, err := s.Call(context.Background(), "list_installations", map[string]any{"customer": "x"})
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "list_installations" || te.Message != mustertest.Refusal {
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
	if auth.URL != mustertest.LoginURL || auth.Server != Server {
		t.Fatalf("got %+v", auth)
	}
}

func TestCallOfAToolMusterDoesNotKnowIsCallToolsRefusal(t *testing.T) {
	s := session(t, true)
	_, err := s.Call(context.Background(), "no_such_tool", nil)
	if err == nil || !strings.Contains(err.Error(), "tool not found") {
		t.Fatalf("got %v", err)
	}
	if _, ok := IsAuthRequired(err); ok {
		t.Fatal("a connected server's unknown tool is not a sign-in")
	}
}

// The bridge appends its sign-in notices after call_tool's document; the
// document is the first text, whatever follows it.
func TestUnwrapReadsTheFirstTextOnly(t *testing.T) {
	res := &mcp.CallToolResult{Content: []mcp.Content{
		mcp.NewTextContent(`{"isError":false,"content":[{"type":"text","text":"{\"version\":\"1\"}"}],"structuredContent":{"authUrl":"u"}}`),
		mcp.NewTextContent("Authentication required for 1 server"),
	}}
	got, err := unwrap("x", res)
	if err != nil {
		t.Fatal(err)
	}
	if textOf(got) != `{"version":"1"}` || got.IsError {
		t.Fatalf("got %+v", got)
	}
	if s, _ := got.StructuredContent.(map[string]any); s["authUrl"] != "u" {
		t.Fatalf("structured content lost: %+v", got.StructuredContent)
	}
}

func TestUnwrapRefusesAnAnswerThatIsNoToolResult(t *testing.T) {
	_, err := unwrap("x", mcp.NewToolResultText("plain text"))
	if err == nil || !strings.Contains(err.Error(), "something other than a tool result") {
		t.Fatalf("got %v", err)
	}
	_, err = unwrap("x", mcp.NewToolResultError("Meta-tool execution failed: connection lost"))
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenNamesTheMissingBinary(t *testing.T) {
	_, err := Open(context.Background(), Options{Binary: "muster-that-is-not-installed"})
	if err == nil || !strings.Contains(err.Error(), "muster-that-is-not-installed") || !strings.Contains(err.Error(), "--muster") {
		t.Fatalf("got %v", err)
	}
}
