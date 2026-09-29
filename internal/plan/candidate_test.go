package plan

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

func TestDropsPrereleases(t *testing.T) {
	on, off := "global:\n  domain: x\ngitops:\n  prereleases: true\n", "global:\n  domain: x\n"
	for _, c := range []struct {
		current, rendered string
		want              bool
	}{{on, off, true}, {on, on, false}, {off, off, false}, {off, on, false}, {"{", off, false}} {
		if got := dropsPrereleases(c.current, c.rendered); got != c.want {
			t.Errorf("%q -> %q: %v, want %v", c.current, c.rendered, got, c.want)
		}
	}
}

// A commit that drops the release candidates is held while a candidate is
// ahead of the stable release, and only then.
func TestReleaseCandidateRefusal(t *testing.T) {
	ahead := &installations.Record{PlatformCandidate: "v4.2.0-rc.2", PlatformStable: "v4.1.0"}
	drops := Installation{Name: hazel.Name, DropsPrereleases: true}
	got := drops.ReleaseCandidateRefusal(ahead)
	if !strings.Contains(got, "v4.2.0-rc.2 is ahead") || !strings.Contains(got, "down to the latest stable release v4.1.0") || !strings.Contains(got, "devctl release promote giantswarm/agent-platform") {
		t.Errorf("the refusal: %q", got)
	}
	if got := drops.ReleaseCandidateRefusal(&installations.Record{PlatformCandidate: "v4.0.0-rc.1"}); !strings.Contains(got, "no stable release at all") {
		t.Errorf("without a stable release: %q", got)
	}
	for name, c := range map[string]struct {
		p   Installation
		rec *installations.Record
	}{
		"keeps the candidates": {Installation{Name: hazel.Name}, ahead},
		"no candidate ahead":   {drops, &installations.Record{}},
		"without a record":     {drops, nil},
	} {
		if got := c.p.ReleaseCandidateRefusal(c.rec); got != "" {
			t.Errorf("%s: %q", name, got)
		}
	}
}
