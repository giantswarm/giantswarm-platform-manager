package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster/mustertest"
)

// bridgeEnv, when set, makes this test binary muster's bridge over stdio —
// meta tools only, the manager's tools behind call_tool — so the commands run
// end to end against a bridge the way they run against `muster agent
// --mcp-server`: "connected" with the manager reachable, "sign-in" without.
const bridgeEnv = "PLATFORMCTL_TEST_BRIDGE"

func TestMain(m *testing.M) {
	switch os.Getenv(bridgeEnv) {
	case "":
		os.Exit(m.Run())
	case "connected":
		serveBridge(true)
	case "sign-in":
		serveBridge(false)
	default:
		os.Exit(2)
	}
}

func serveBridge(connected bool) {
	if err := mcpserver.ServeStdio(mustertest.Bridge(mustertest.Manager(connected))); err != nil {
		os.Exit(1)
	}
}

// bridge runs the command against this binary as the bridge.
func bridge(t *testing.T, state string, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(bridgeEnv, state)
	var stdout, stderr bytes.Buffer
	code := run(append(args, "--muster", os.Args[0]), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestEnableDryRunThroughTheBridge(t *testing.T) {
	code, out, errs := bridge(t, "connected", "installation", "enable", "lab", "agent-platform", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	want := "enable_capability dry run: agent-platform on hub " + mustertest.Hub + ", as " + mustertest.Caller
	if !strings.Contains(out, want) || !strings.Contains(out, "Order: lab") {
		t.Fatalf("got %q, want %q and the installation", out, want)
	}
}

func TestEnableDryRunJSONIsTheToolsDocument(t *testing.T) {
	code, out, errs := bridge(t, "connected", "installation", "enable", "lab", "agent-platform", "--dry-run", "--output", "json")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	var got struct {
		Caller     string `json:"caller"`
		Capability string `json:"capability"`
		DryRun     bool   `json:"dryRun"`
		IsError    *bool  `json:"isError"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v: %q", err, out)
	}
	if got.Caller != mustertest.Caller || got.Capability != "agent-platform" || !got.DryRun {
		t.Fatalf("got %q", out)
	}
	if got.IsError != nil {
		t.Fatalf("the bridge's envelope leaked into the output: %q", out)
	}
}

func TestVerifyThroughTheBridge(t *testing.T) {
	code, out, errs := bridge(t, "connected", "installation", "verify", "lab", "agent-platform")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	want := "verify agent-platform on lab (hub " + mustertest.Hub + "), as " + mustertest.Caller
	if !strings.Contains(out, want) || !strings.Contains(out, "State: enabled") {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestAManagerNotConnectedAsksForTheSignIn(t *testing.T) {
	code, out, errs := bridge(t, "sign-in", "installation", "verify", "lab", "agent-platform")
	if code != exitSignIn {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errs)
	}
	if !strings.Contains(errs, mustertest.LoginURL) || !strings.Contains(errs, "muster auth login") {
		t.Fatalf("stderr %q lacks the sign-in URL or the muster command", errs)
	}
}
