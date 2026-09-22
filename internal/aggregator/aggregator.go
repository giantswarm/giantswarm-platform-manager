// Package aggregator speaks MCP to muster's aggregator the way its bridge and
// the platform's servers see it: the aggregator's tools — the manager's
// x_<server>_<tool>, muster's core_*, an installation's x_<family>_<tool> —
// are one level down, behind the meta tool call_tool {name, arguments}, which
// answers the called tool's own result as one JSON document, and list_tools,
// which names them. This package unwraps that document and nothing more; who
// the session is (the bearer it carries) is the caller's business.
package aggregator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// The meta tools every aggregator tool is reached through.
const (
	MetaCallTool  = "call_tool"
	MetaListTools = "list_tools"
)

// ArgCallTimeout is call_tool's timeout argument on muster's CLI bridge:
// seconds to wait for this one call. See Session.CallTimeout.
const ArgCallTimeout = "timeout"

// Session is one MCP session with the aggregator.
type Session struct {
	c *client.Client
	// CallTimeout, when set, goes with every call as call_tool's timeout
	// argument: the bound muster's CLI bridge puts on that one call in place
	// of its own --timeout (5 minutes), so a caller that waits longer is not
	// cut short by the bridge. The argument is the bridge's and never reaches
	// the aggregator's tool; a session opened directly at muster leaves it
	// zero, where muster's call_tool would hand it to the tool.
	CallTimeout time.Duration
}

// Open connects to url over streamable HTTP, the bearer put on every request
// read from bearer at request time (a forwarded token muster refreshes stays
// current), and initializes the session as clientName. httpClient nil is the
// default client.
func Open(ctx context.Context, url string, bearer func() string, clientName, clientVersion string, httpClient *http.Client) (*Session, error) {
	opts := []transport.StreamableHTTPCOption{transport.WithHTTPHeaderFunc(func(context.Context) map[string]string {
		if t := bearer(); t != "" {
			return map[string]string{"Authorization": "Bearer " + t}
		}
		return nil
	})}
	if httpClient != nil {
		opts = append(opts, transport.WithHTTPBasicClient(httpClient))
	}
	c, err := client.NewStreamableHttpClient(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("muster at %s: %w", url, err)
	}
	return New(ctx, c, clientName, clientVersion)
}

// New initializes the session over an already constructed client — the
// bridge process, an in-process server in tests.
func New(ctx context.Context, c *client.Client, clientName, clientVersion string) (*Session, error) {
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("connect to muster: %w", err)
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: clientName, Version: clientVersion}
	if _, err := c.Initialize(ctx, req); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize the MCP session with muster: %w", err)
	}
	return &Session{c: c}, nil
}

// Close ends the session.
func (s *Session) Close() error { return s.c.Close() }

// Call runs one aggregator tool through call_tool and returns the tool's own
// result, unwrapped from the document call_tool answers. An error is
// call_tool's own refusal — a tool it does not know, a lost session — never
// the tool's answer, which comes back as the result with IsError set.
func (s *Session) Call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = MetaCallTool
	meta := map[string]any{"name": name, "arguments": args}
	if s.CallTimeout > 0 {
		meta[ArgCallTimeout] = s.CallTimeout.Seconds()
	}
	req.Params.Arguments = meta
	res, err := s.c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return unwrap(name, res)
}

// Tools names the aggregator's tools for this session, as list_tools answers
// them: a server the person is not connected to contributes none.
func (s *Session) Tools(ctx context.Context) ([]string, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = MetaListTools
	req.Params.Arguments = map[string]any{}
	res, err := s.c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MetaListTools, err)
	}
	var doc struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	text := TextOf(res)
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("%s answered something other than a tool list: %.200q", MetaListTools, text)
	}
	names := make([]string, 0, len(doc.Tools))
	for _, t := range doc.Tools {
		names = append(names, t.Name)
	}
	return names, nil
}

// envelope is what call_tool answers as its first text content: the called
// tool's result serialized as one JSON document.
type envelope struct {
	IsError bool `json:"isError"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent any `json:"structuredContent"`
}

// unwrap reads the called tool's result out of call_tool's answer. The bridge
// may append its own notices after the envelope; only the first text counts.
func unwrap(name string, res *mcp.CallToolResult) (*mcp.CallToolResult, error) {
	var text string
	for _, c := range res.Content {
		if t, ok := mcp.AsTextContent(c); ok {
			text = t.Text
			break
		}
	}
	var e envelope
	if err := json.Unmarshal([]byte(text), &e); err != nil || e.Content == nil {
		if res.IsError {
			return nil, fmt.Errorf("%s: %s", name, text)
		}
		return nil, fmt.Errorf("%s: muster's %s answered something other than a tool result: %.200q", name, MetaCallTool, text)
	}
	out := &mcp.CallToolResult{IsError: e.IsError, StructuredContent: e.StructuredContent}
	for _, c := range e.Content {
		if c.Type == "text" {
			out.Content = append(out.Content, mcp.NewTextContent(c.Text))
		}
	}
	return out, nil
}

// TextOf joins a result's text contents.
func TextOf(res *mcp.CallToolResult) string {
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if t, ok := mcp.AsTextContent(c); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}
