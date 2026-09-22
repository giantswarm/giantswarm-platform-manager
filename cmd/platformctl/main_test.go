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

// TestReconcileTargetsThroughTheBridge holds the targets reconcile sends: one
// name is the tool's installation, two or more the set installations, --all
// the empty set — every installation of the registry; a commit over one is
// the action, over a set the wave.
func TestReconcileTargetsThroughTheBridge(t *testing.T) {
	reconcile := func(rest ...string) []string { return append([]string{"installation", "reconcile"}, rest...) }
	all := strings.Join(mustertest.Registry, ", ")
	for _, c := range []struct {
		args []string
		want []string
	}{
		{reconcile("lab", agentPlatform, "--dry-run"), []string{"reconcile_capability dry run: agent-platform on hub " + mustertest.Hub, "Order: lab\n"}},
		{reconcile("lab", "hazel", agentPlatform, "--dry-run"), []string{"reconcile_capability dry run: agent-platform on hub " + mustertest.Hub, "Order: lab, hazel\n"}},
		{reconcile("--all", agentPlatform, "--dry-run"), []string{"reconcile_capability dry run: agent-platform on hub " + mustertest.Hub, "Order: " + all + "\n"}},
		{reconcile("lab", agentPlatform, "--commit"), []string{"reconcile_capability commit: agent-platform on lab (hub " + mustertest.Hub + ")"}},
		{reconcile("lab", "hazel", agentPlatform, "--commit"), []string{"reconcile_capability commit: agent-platform wave on hub " + mustertest.Hub, "Order: lab, hazel\n"}},
		{reconcile("--all", agentPlatform, "--commit"), []string{"reconcile_capability commit: agent-platform wave on hub " + mustertest.Hub, "Order: " + all + "\n"}},
	} {
		code, out, errs := bridge(t, "connected", c.args...)
		if code != exitOK {
			t.Errorf("%v: exit %d, stderr %q", c.args, code, errs)
			continue
		}
		for _, want := range c.want {
			if !strings.Contains(out, want) {
				t.Errorf("%v: output lacks %q:\n%s", c.args, want, out)
			}
		}
	}
}

// A set's dry run prints each planned file's path and change, and its content
// only with --content — the manager's own default for a set, asked for
// explicitly by the flag either way.
func TestSetDryRunContentThroughTheBridge(t *testing.T) {
	path := "acme/lab-configs/installations/lab/apps/agent-platform/configmap-values.yaml.patch"
	code, out, errs := bridge(t, "connected", installationCmd, "reconcile", "lab", "hazel", agentPlatform, "--dry-run")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if !strings.Contains(out, "installations/lab/apps/agent-platform/configmap-values.yaml.patch") || strings.Contains(out, mustertest.FileContent) {
		t.Fatalf("without --content: got\n%s", out)
	}
	code, out, errs = bridge(t, "connected", installationCmd, "reconcile", "lab", "hazel", agentPlatform, "--dry-run", "--content")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	if !strings.Contains(out, "--- "+path+" (update)\n"+mustertest.FileContent) {
		t.Fatalf("with --content: got\n%s", out)
	}
}

// An answer above the manager's limit is the manager's refusal, printed as
// the tool's error with the size and the limit — the command ends at once,
// it never waits for an answer the path drops.
func TestAnAnswerAboveTheLimitIsAnErrorNotAWait(t *testing.T) {
	for _, args := range [][]string{
		{installationCmd, "reconcile", "lab", mustertest.Oversized, agentPlatform, "--dry-run", "--content"},
		{installationCmd, "enable", mustertest.Oversized, agentPlatform, "--dry-run", "--content"},
	} {
		code, out, errs := bridge(t, "connected", append(args, "--timeout", "20s")...)
		if code != exitError {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, out, errs)
			continue
		}
		for _, want := range []string{"platformctl: " + args[1] + "_capability: the answer is ", " bytes (1.0 MiB), above the 1048576 bytes (1.0 MiB)", "ask for less"} {
			if !strings.Contains(errs, want) {
				t.Errorf("%v: stderr %q lacks %q", args, errs, want)
			}
		}
	}
}
