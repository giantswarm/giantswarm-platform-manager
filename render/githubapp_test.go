package render

import (
	"testing"
	"time"
)

// An App is due a successor GitHubAppRotation months after the date its
// commit learned it; without a readable date it is never due.
func TestGitHubAppRotationDue(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		created, due string
		ok           bool
	}{
		{"2026-04-01", "2026-10-01", true},
		{"2026-04-18", "2026-10-18", true},
		{"2026-05-01", "2026-11-01", false},
		{"", "", false},
		{"not a date", "", false},
	} {
		due, ok := GitHubApp{Name: "giantswarm-portal-maple", ID: 1, Created: c.created}.RotationDue(now)
		if due != c.due || ok != c.ok {
			t.Errorf("created %q: due %q %v, want %q %v", c.created, due, ok, c.due, c.ok)
		}
	}
}

func TestAppID(t *testing.T) {
	for value, want := range map[string]int{"123": 123, "0": 0, "-4": 0, "": 0, Supplied("plugins.github.appId"): 0} {
		if got := AppID(value); got != want {
			t.Errorf("AppID(%q) = %d, want %d", value, got, want)
		}
	}
}
