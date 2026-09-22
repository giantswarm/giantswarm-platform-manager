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
	"time"

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
	// CallTimeout is how long the bridge waits for muster's answer to one
	// call, sent as call_tool's timeout argument; zero leaves the bridge its
	// own default of 5 minutes, whatever the caller's context allows.
	CallTimeout time.Duration
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
	s, err := New(ctx, c)
	if err != nil {
		return nil, err
	}
	s.s.CallTimeout = o.CallTimeout
	return s, nil
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
		if cut := cutOf(ctx, tool, s.s.CallTimeout, err); cut != nil {
			return nil, cut
		}
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

// Cut says a call ended without an answer, and which layer gave up: every
// layer reports a bare "context deadline exceeded", and only the layer tells
// a slow manager from an answer lost on the way. The bridge waits CallTimeout
// for muster's answer; platformctl waits a little longer for the bridge's,
// so when muster does not answer it is the bridge that reports it, and
// platformctl's own bound fires only when the bridge itself does not answer.
type Cut struct {
	// Layer is who gave up.
	Layer Layer `json:"layer"`
	// Tool is the manager's tool that was called.
	Tool string `json:"tool"`
	// Timeout is the bound that ran out, where the layer is this side's.
	Timeout string `json:"timeout,omitempty"`
}

// Layer names the side that cut a call.
type Layer string

const (
	// LayerPlatformctl is platformctl's own --timeout: the bridge did not
	// answer, not even with its own deadline.
	LayerPlatformctl Layer = "platformctl"
	// LayerBridge is the muster CLI bridge: no answer from muster within the
	// call's bound, though muster and the manager may have answered.
	LayerBridge Layer = "bridge"
	// LayerMuster is muster: the manager did not answer within the
	// registration's spec.timeout.
	LayerMuster Layer = "muster"
)

func (e *Cut) Error() string {
	switch e.Layer {
	case LayerBridge:
		return fmt.Sprintf("%s: cut by the muster CLI bridge, which got no answer from muster within its call timeout of %s; muster and the manager may well have answered (the manager's log says whether it did)", e.Tool, e.Timeout)
	case LayerMuster:
		return fmt.Sprintf("%s: cut by muster, which got no answer from the manager within the registration's timeout (the MCPServer's spec.timeout)", e.Tool)
	default:
		return fmt.Sprintf("%s: cut by platformctl, whose --timeout of %s ran out waiting for the bridge to answer at all", e.Tool, e.Timeout)
	}
}

// IsCut reports whether err is, or wraps, a Cut.
func IsCut(err error) (*Cut, bool) {
	var c *Cut
	ok := errors.As(err, &c)
	return c, ok
}

// How the layers word their deadline: mcp-go's transport error for the
// context the bridge bounded its call with, wrapped by the bridge's client,
// and muster's for the registration's spec.timeout.
const (
	bridgeDeadline = "tool call failed: transport error: context deadline exceeded"
	musterDeadline = "no answer within the server's timeout"
)

// cutOf reads which layer cut the call to tool out of err, nil when err is
// something else. ctx is the call's; timeout the bound the bridge was given.
func cutOf(ctx context.Context, tool string, timeout time.Duration, err error) *Cut {
	text := err.Error()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return &Cut{Layer: LayerPlatformctl, Tool: tool, Timeout: bound(timeout)}
	case strings.Contains(text, bridgeDeadline):
		return &Cut{Layer: LayerBridge, Tool: tool, Timeout: bound(timeout)}
	case strings.Contains(text, musterDeadline):
		return &Cut{Layer: LayerMuster, Tool: tool}
	}
	return nil
}

// bound prints the bridge's bound, or its own default where none was sent.
func bound(timeout time.Duration) string {
	if timeout <= 0 {
		return "5m0s (its default)"
	}
	return timeout.String()
}
