package aggregator

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

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
	if TextOf(got) != `{"version":"1"}` || got.IsError {
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

// The called tool's own refusal comes back as the result, isError set — a
// refusal to tell apart from call_tool's.
func TestUnwrapKeepsTheToolsOwnError(t *testing.T) {
	res := mcp.NewToolResultText(`{"isError":true,"content":[{"type":"text","text":"secrets is forbidden: User \"oidc:viewer\" cannot list resource"}]}`)
	got, err := unwrap("x_kubernetes_list", res)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsError || !strings.Contains(TextOf(got), "is forbidden") {
		t.Fatalf("got %+v", got)
	}
}
