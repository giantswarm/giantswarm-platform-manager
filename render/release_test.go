package render

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
)

// The release-candidate line and the tag filter beside it select what Flux
// resolves: every release and release candidate from the floor on, whatever
// the major, and no branch build, which shares the chart's repository with a
// pre-release of its own. Flux holds a tag to the semver range and the filter
// both, with Masterminds semver.
func TestReleaseCandidatesSelect(t *testing.T) {
	const line = ">=2.1.0-0"
	if !ReleaseCandidates(line) {
		t.Fatalf("%q is the release-candidate form", line)
	}
	constraint, err := semver.NewConstraint(line)
	if err != nil {
		t.Fatal(err)
	}
	filter := regexp.MustCompile(ReleaseTagFilter)
	for tag, want := range map[string]bool{
		"2.97.0":                                 true,
		"2.97.1-rc.6":                            true,
		"3.0.0":                                  true,
		"3.1.0-rc.6":                             true,
		"4.0.0-rc.1":                             true,
		"v3.1.0-rc.6":                            true,
		"2.0.0":                                  false,
		"3.0.1-ra7089124t20261014075518hfbbed24": false,
		"2.95.4-r8c1a190ft20261013072355h4a8f462": false,
	} {
		v, err := semver.NewVersion(strings.TrimPrefix(tag, "v"))
		if err != nil {
			t.Fatal(err)
		}
		if got := constraint.Check(v) && filter.MatchString(tag); got != want {
			t.Errorf("%s: selected %v, want %v", tag, got, want)
		}
	}
	for _, other := range []string{">=2.1.0", ">=2.1.0 <3.0.0", ">=2.1.0-rc.1", ">=2.1.0-0 <3.0.0-0", "2.1.0-0"} {
		if ReleaseCandidates(other) {
			t.Errorf("%q is no release-candidate form", other)
		}
	}
}
