package tools

import (
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// The guard: apply is refused in every spelling, an unknown mode is refused,
// commit passes, and a write without dryRun needs a mode.
func TestCheckMode(t *testing.T) {
	for _, tc := range []struct {
		mode   string
		dryRun bool
		want   string // "" accepts
	}{
		{"commit", false, ""},
		{"Commit", true, ""},
		{"", true, ""},
		{"", false, "mode is required"},
		{"apply", false, ApplyRefusal},
		{" APPLY ", true, ApplyRefusal},
		{"merge", false, `mode "merge" is not known`},
	} {
		err := checkMode(tc.mode, tc.dryRun)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("mode %q dryRun %v: refused: %v", tc.mode, tc.dryRun, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("mode %q dryRun %v: got %v, want %q", tc.mode, tc.dryRun, err, tc.want)
		}
	}
}

// The capability argument: agent-platform by name or by default passes; a
// registered definition the tools do not take yet is refused as not
// implemented, naming the tools that do take it; an unknown name is refused
// with the registry's names.
func TestCapabilityArg(t *testing.T) {
	for _, name := range []string{"", installations.AgentPlatform} {
		got, err := capabilityArg(map[string]any{ArgCapability: name})
		if err != nil || got != installations.AgentPlatform {
			t.Errorf("capability %q: got %q, %v", name, got, err)
		}
	}
	_, err := capabilityArg(map[string]any{ArgCapability: installations.CustomerPortal})
	if !errors.Is(err, ErrNotImplemented) || !strings.Contains(err.Error(), installations.CustomerPortal) || !strings.Contains(err.Error(), "platformctl template") {
		t.Errorf("customer-portal: got %v", err)
	}
	_, err = capabilityArg(map[string]any{ArgCapability: "cluster"})
	if err == nil || errors.Is(err, ErrNotImplemented) || !strings.Contains(err.Error(), "not known") || !strings.Contains(err.Error(), installations.CustomerPortal) {
		t.Errorf("unknown: got %v", err)
	}
}
