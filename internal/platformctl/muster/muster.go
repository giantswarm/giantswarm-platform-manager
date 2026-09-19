// Package muster reaches the manager's tools the way a person on a laptop
// does: through muster's own CLI. `muster agent --mcp-server` is muster's
// stdio-to-HTTP bridge — it takes the aggregator endpoint from muster's
// configuration and runs the person's OAuth login when the aggregator asks for
// one — and this package speaks MCP to it through the aggregator package,
// nothing more. Behind muster the manager's tools are named
// x_<server>_<tool>: the App-pinned registration's under Server, the live
// registration's under LiveServer; a server the person has not connected yet
// exposes no tools, and muster's core_auth_login answers the sign-in URL,
// which this package hands back as an AuthRequired error.
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

	"github.com/giantswarm/giantswarm-platform-manager/internal/aggregator"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

// Server is the manager's name in muster: the App-pinned MCPServer the chart
// registers, and the server core_auth_login connects. LiveServer is the
// second registration of the same Deployment, the one muster forwards the
// person's own token to: nothing to sign in to.
const (
	Server     = tools.ToolPrefix
	LiveServer = tools.LiveToolPrefix
)

// DefaultBinary is the muster CLI as found on PATH.
const DefaultBinary = "muster"

// toolAuthLogin is the aggregator's own tool that connects a server for the person.
const toolAuthLogin = "core_auth_login"

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
	s *aggregator.Session
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
	s, err := aggregator.New(ctx, c, "platformctl", version.String())
	if err != nil {
		return nil, err
	}
	return &Session{s: s}, nil
}

// Close ends the session and the bridge process.
func (s *Session) Close() error { return s.s.Close() }

// Call runs one of the App-pinned manager's tools by its plain name
// (list_installations, enable_capability, …) and returns the JSON document
// it answered. A tool refusal is a *ToolError; a server the person has not
// connected yet is an *AuthRequired carrying the sign-in URL muster answered.
func (s *Session) Call(ctx context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	return s.CallServer(ctx, Server, tool, args)
}

// CallServer is Call against the named registration of the manager: Server
// or LiveServer.
func (s *Session) CallServer(ctx context.Context, server, tool string, args map[string]any) (json.RawMessage, error) {
	res, err := s.s.Call(ctx, "x_"+server+"_"+tool, args)
	if err != nil {
		if auth := s.signIn(ctx, server); auth != nil {
			return nil, auth
		}
		return nil, err
	}
	if res.IsError {
		if auth := s.signIn(ctx, server); auth != nil {
			return nil, auth
		}
		return nil, &ToolError{Tool: tool, Message: aggregator.TextOf(res)}
	}
	return json.RawMessage(aggregator.TextOf(res)), nil
}

// signIn asks muster whether server is connected for this person: its
// core_auth_login answers a challenge with the sign-in URL when it is not, and
// something else when it is. Only a challenge is an AuthRequired.
func (s *Session) signIn(ctx context.Context, server string) *AuthRequired {
	res, err := s.s.Call(ctx, toolAuthLogin, map[string]any{"server": server})
	if err != nil || res.IsError {
		return nil
	}
	structured, _ := res.StructuredContent.(map[string]any)
	url, _ := structured["authUrl"].(string)
	if url == "" {
		return nil
	}
	return &AuthRequired{Server: server, URL: url, Message: aggregator.TextOf(res)}
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
