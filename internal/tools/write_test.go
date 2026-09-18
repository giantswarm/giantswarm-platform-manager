package tools

import (
	"strings"
	"testing"
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
