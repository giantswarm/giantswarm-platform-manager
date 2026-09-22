package muster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestOpenNamesTheMissingBinary(t *testing.T) {
	_, err := Open(context.Background(), Options{Binary: "muster-that-is-not-installed"})
	if err == nil || !strings.Contains(err.Error(), "muster-that-is-not-installed") || !strings.Contains(err.Error(), "--muster") {
		t.Fatalf("got %v", err)
	}
}

// The bridge's own call timeout is 5 minutes whatever platformctl waits; a
// session opened with a bound sends it as call_tool's timeout, so the bridge
// waits as long as platformctl does.
func TestCallSendsTheBridgeItsBound(t *testing.T) {
	var calls []map[string]any
	c, err := client.NewInProcessClient(mustertest.BridgeRecording(mustertest.Manager(true), &calls))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.s.CallTimeout = 12 * time.Minute
	if _, err := s.Call(context.Background(), "get_info", nil); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0]["timeout"] != float64(720) {
		t.Fatalf("call_tool got %v, want timeout 720 (seconds)", calls)
	}
}

// Without a bound nothing is sent: a session opened directly at muster would
// hand a timeout argument to the tool.
func TestCallWithoutABoundSendsNoTimeout(t *testing.T) {
	var calls []map[string]any
	c, err := client.NewInProcessClient(mustertest.BridgeRecording(mustertest.Manager(true), &calls))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Call(context.Background(), "get_info", nil); err != nil {
		t.Fatal(err)
	}
	if _, sent := calls[0]["timeout"]; sent {
		t.Fatalf("call_tool got %v, want no timeout argument", calls[0])
	}
}

// A call the bridge gives up on is reported as cut by the bridge, with the
// bound it waited, not as a bare deadline.
func TestTheBridgesDeadlineIsACutByTheBridge(t *testing.T) {
	aggregator := mustertest.Manager(true)
	aggregator["x_"+Server+"_slow"] = func(context.Context, map[string]any) *mcp.CallToolResult { return nil }
	c, err := client.NewInProcessClient(mustertest.Bridge(aggregator))
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.s.CallTimeout = 12 * time.Minute
	_, err = s.Call(context.Background(), "slow", nil)
	cut, ok := IsCut(err)
	if !ok {
		t.Fatalf("got %v, want a Cut", err)
	}
	if cut.Layer != LayerBridge || cut.Tool != "slow" || cut.Timeout != "12m0s" {
		t.Fatalf("got %+v, want the bridge's cut of slow after 12m0s", cut)
	}
	if !strings.Contains(err.Error(), "cut by the muster CLI bridge") || !strings.Contains(err.Error(), "12m0s") {
		t.Fatalf("got %q, want the bridge named with its bound", err)
	}
}

// Each layer's deadline is told apart: platformctl's by the call's context,
// the bridge's and muster's by their wording; anything else is not a cut.
func TestCutOfNamesTheLayer(t *testing.T) {
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		err   error
		layer Layer
	}{
		{"platformctl", expired, errors.New("verify_installation: transport error: context deadline exceeded"), LayerPlatformctl},
		{"bridge", context.Background(), errors.New("verify_installation: " + mustertest.BridgeDeadline), LayerBridge},
		{"muster", context.Background(), errors.New("verify_installation: Meta-tool execution failed: failed to call tool: no answer within the server's timeout of 3m0s: context deadline exceeded"), LayerMuster},
	} {
		cut := cutOf(tc.ctx, "verify_installation", 5*time.Minute, tc.err)
		if cut == nil || cut.Layer != tc.layer {
			t.Fatalf("%s: got %+v, want layer %s", tc.name, cut, tc.layer)
		}
	}
	if cut := cutOf(context.Background(), "verify_installation", 5*time.Minute, errors.New("verify_installation: muster's call_tool answered something else")); cut != nil {
		t.Fatalf("got %+v, want no cut for another error", cut)
	}
}
