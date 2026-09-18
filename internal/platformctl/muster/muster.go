// Package muster reaches the manager's tools the way a person on a laptop
// does: through muster's own CLI. `muster agent --mcp-server` is muster's
// stdio-to-HTTP bridge — it takes the aggregator endpoint from muster's
// configuration and runs the person's OAuth login when the aggregator asks for
// one — and this package speaks MCP to it, nothing more. The bridge exposes
// muster's meta tools only (call_tool, list_tools, describe_tool, …); the
// aggregator's tools are one level down, behind call_tool {name, arguments},
// which answers the tool's own result as one JSON document. Behind muster the
// manager's tools are named x_<server>_<tool>; a server the person has not
// connected yet exposes no tools, and muster's core_auth_login answers the
// sign-in URL, which this package hands back as an AuthRequired error.
package muster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

// Server is the manager's name in muster: the MCPServer the chart registers,
// and the server core_auth_login connects.
const Server = tools.ToolPrefix

// DefaultBinary is the muster CLI as found on PATH.
const DefaultBinary = "muster"

// The bridge's meta tool every aggregator tool is called through, and the
// aggregator's own tool that connects a server for the person.
const (
	metaCallTool  = "call_tool"
	toolAuthLogin = "core_auth_login"
)

// Options say how the bridge is started.
type Options struct {
	// Binary is the muster CLI; "" is DefaultBinary on PATH.
	Binary string
	// Endpoint is the aggregator's MCP endpoint; "" leaves it to muster's
	// configuration, the way `muster agent` resolves it.
	Endpoint string
	// ConfigPath is muster's configuration directory; "" is muster's default.
	ConfigPath string
	// Stderr receives the bridge's stderr: its login prompts and the URL of
	// the aggregator's own sign-in. nil discards it.
	Stderr io.Writer
}

// Session is one connection to muster with the manager's tools in reach.
type Session struct {
	c *client.Client
}

// Open starts `muster agent --mcp-server` and initializes the MCP session.
func Open(ctx context.Context, o Options) (*Session, error) {
	binary := o.Binary
	if binary == "" {
		binary = DefaultBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		return nil, fmt.Errorf("the muster CLI is needed to reach the manager: %w (install muster from https://github.com/giantswarm/muster/releases or name it with --muster)", err)
	}
	args := []string{"agent", "--mcp-server"}
	if o.Endpoint != "" {
		args = append(args, "--endpoint", o.Endpoint)
	}
	if o.ConfigPath != "" {
		args = append(args, "--config-path", o.ConfigPath)
	}
	stderr := o.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	c, err := client.NewStdioMCPClientWithOptions(binary, nil, args, transport.WithCommandStderrWriter(stderr))
	if err != nil {
		return nil, fmt.Errorf("start %s %s: %w", binary, strings.Join(args, " "), err)
	}
	return New(ctx, c)
}

// New initializes the MCP session over an already constructed client — the
// bridge from Open, or an in-process bridge in tests.
func New(ctx context.Context, c *client.Client) (*Session, error) {
	if err := c.Start(ctx); err != nil {
		return nil, fmt.Errorf("connect to muster: %w", err)
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: "platformctl", Version: version.String()}
	if _, err := c.Initialize(ctx, req); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize the MCP session with muster: %w", err)
	}
	return &Session{c: c}, nil
}

// Close ends the session and the bridge process.
func (s *Session) Close() error { return s.c.Close() }

// Call runs one of the manager's tools by its plain name (list_installations,
// enable_capability, …) and returns the JSON document it answered. A tool
// refusal is a *ToolError; a server the person has not connected yet is an
// *AuthRequired carrying the sign-in URL muster answered.
func (s *Session) Call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	res, err := s.call(ctx, "x_"+Server+"_"+tool, args)
	if err != nil {
		if auth := s.signIn(ctx); auth != nil {
			return nil, auth
		}
		return nil, err
	}
	if res.IsError {
		if auth := s.signIn(ctx); auth != nil {
			return nil, auth
		}
		return nil, &ToolError{Tool: tool, Message: textOf(res)}
	}
	return json.RawMessage(textOf(res)), nil
}

// call runs one aggregator tool through the bridge's call_tool and returns
// the tool's own result, unwrapped from the document call_tool answers. An
// error is the bridge's or call_tool's own refusal — a tool it does not know,
// a lost connection — never the tool's answer.
func (s *Session) call(ctx context.Context, name string, args map[string]any) (*mcp.CallToolResult, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = metaCallTool
	req.Params.Arguments = map[string]any{"name": name, "arguments": args}
	res, err := s.c.CallTool(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return unwrap(name, res)
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
		return nil, fmt.Errorf("%s: muster's %s answered something other than a tool result: %q", name, metaCallTool, text)
	}
	out := &mcp.CallToolResult{IsError: e.IsError, StructuredContent: e.StructuredContent}
	for _, c := range e.Content {
		if c.Type == "text" {
			out.Content = append(out.Content, mcp.NewTextContent(c.Text))
		}
	}
	return out, nil
}

// signIn asks muster whether the manager is connected for this person: its
// core_auth_login answers a challenge with the sign-in URL when it is not, and
// something else when it is. Only a challenge is an AuthRequired.
func (s *Session) signIn(ctx context.Context) *AuthRequired {
	res, err := s.call(ctx, toolAuthLogin, map[string]any{"server": Server})
	if err != nil || res.IsError {
		return nil
	}
	structured, _ := res.StructuredContent.(map[string]any)
	url, _ := structured["authUrl"].(string)
	if url == "" {
		return nil
	}
	return &AuthRequired{Server: Server, URL: url, Message: textOf(res)}
}

func textOf(res *mcp.CallToolResult) string {
	parts := make([]string, 0, len(res.Content))
	for _, c := range res.Content {
		if t, ok := mcp.AsTextContent(c); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// AuthRequired says the person has not connected the manager in muster yet:
// URL is the sign-in muster offers, Message its full answer.
type AuthRequired struct {
	Server  string `json:"server"`
	URL     string `json:"authUrl"`
	Message string `json:"message"`
}

func (e *AuthRequired) Error() string {
	return fmt.Sprintf("sign in required: %s is not connected for you in muster; open %s, then run the command again (or: muster auth login --server %s)", e.Server, e.URL, e.Server)
}

// ToolError is the tool's own refusal: the text the manager answered with
// isError set.
type ToolError struct {
	Tool    string
	Message string
}

func (e *ToolError) Error() string { return e.Tool + ": " + e.Message }

// IsAuthRequired reports whether err is, or wraps, an AuthRequired.
func IsAuthRequired(err error) (*AuthRequired, bool) {
	var a *AuthRequired
	ok := errors.As(err, &a)
	return a, ok
}
