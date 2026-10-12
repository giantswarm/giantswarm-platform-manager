package render

import (
	"testing"

	"github.com/Masterminds/semver/v3"
)

// A dex-app takes a 3.x Dex feature from the 3.x release that introduced it,
// and on the 2.x line from DexAppLine2; a pre-release of either takes nothing.
func TestDexAppTakes(t *testing.T) {
	const since = "3.2.3"
	for version, takes := range map[string]bool{
		"2.2.3": false, "2.3.0": false, "2.4.0-rc.1": false, "2.4.0": true, "2.5.1": true,
		"3.0.0": false, "3.2.2": false, "3.2.3-rc.1": false, "3.2.3": true, "v3.3.0": true, "4.0.0": true,
	} {
		if got := DexAppTakes(semver.MustParse(version), since); got != takes {
			t.Errorf("%s takes %v, want %v", version, got, takes)
		}
	}
	if got, want := DexAppNeeds(since), "dex-app 3.2.3 or later (2.4.0 or later on the 2.x line)"; got != want {
		t.Errorf("DexAppNeeds: %q, want %q", got, want)
	}
}

// A feature a 3.x release after DexAppLine2Carries introduced is the 3.x
// line's alone: no 2.x release takes it, and the refusal names no 2.x
// version.
func TestDexAppTakesBeyondTheLine2(t *testing.T) {
	const since = "3.3.0"
	for version, takes := range map[string]bool{
		"2.4.0": false, "2.5.0": false, "3.2.5": false, "3.3.0-rc.1": false, "3.3.0": true, "v3.4.0": true, "4.0.0": true,
	} {
		if got := DexAppTakes(semver.MustParse(version), since); got != takes {
			t.Errorf("%s takes %v, want %v", version, got, takes)
		}
	}
	if got, want := DexAppNeeds(since), "dex-app 3.3.0 or later"; got != want {
		t.Errorf("DexAppNeeds: %q, want %q", got, want)
	}
}
