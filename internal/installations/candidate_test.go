package installations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// pages serves releases newest first, two to a page.
func pages(releases ...gh.Release) releasesPage {
	return func(_ context.Context, repository string, page int) ([]gh.Release, bool, error) {
		if repository != PlatformRepository {
			return nil, false, errors.New("not the platform's repository: " + repository)
		}
		from := (page - 1) * 2
		if from >= len(releases) {
			return nil, false, nil
		}
		to := min(from+2, len(releases))
		return releases[from:to], to < len(releases), nil
	}
}

func rc(tag string) gh.Release     { return gh.Release{Tag: tag, Prerelease: true} }
func stable(tag string) gh.Release { return gh.Release{Tag: tag} }

// The candidate ahead is the highest 4.x release candidate cut since the
// line's latest stable release, read newest first across pages; a candidate
// the stable release caught up with, a draft, another line and other tags do
// not count.
func TestPlatformCandidate(t *testing.T) {
	cases := []struct {
		name     string
		releases []gh.Release
		want     PlatformCandidate
	}{
		{"a candidate ahead", []gh.Release{rc("v4.2.0-rc.2"), rc("v4.2.0-rc.1"), stable("v4.1.0"), rc("v4.1.0-rc.1")}, PlatformCandidate{Candidate: "v4.2.0-rc.2", Stable: "v4.1.0"}},
		{"rc.10 after rc.9", []gh.Release{rc("v4.2.0-rc.9"), rc("v4.2.0-rc.10"), stable("v4.1.0")}, PlatformCandidate{Candidate: "v4.2.0-rc.10", Stable: "v4.1.0"}},
		{"promoted", []gh.Release{stable("v4.2.0"), rc("v4.2.0-rc.2"), stable("v4.1.0")}, PlatformCandidate{}},
		{"a draft, another line and other tags", []gh.Release{{Tag: "v4.3.0-rc.1", Draft: true, Prerelease: true}, rc("v5.0.0-rc.1"), rc("v4.2.0-dev.1"), {Tag: "v4.2.0-rc.1"}, stable("v4.1.0")}, PlatformCandidate{}},
		{"no stable release of the line", []gh.Release{rc("v4.0.0-rc.1"), stable("v3.9.0")}, PlatformCandidate{Candidate: "v4.0.0-rc.1"}},
		{"none at all", nil, PlatformCandidate{}},
	}
	for _, c := range cases {
		got, err := readPlatformCandidate(t.Context(), pages(c.releases...), releaseCandidateMajor)
		if err != nil || got != c.want {
			t.Errorf("%s: %+v %v, want %+v", c.name, got, err, c.want)
		}
	}

	failing := func(context.Context, string, int) ([]gh.Release, bool, error) { return nil, false, errors.New("502") }
	if _, err := readPlatformCandidate(t.Context(), failing, releaseCandidateMajor); err == nil || !strings.Contains(err.Error(), "agent-platform's release candidates") {
		t.Errorf("unreadable releases: %v, want an error naming what was read", err)
	}
}

// An installation follows the release candidates where its extras
// kustomization patches a semverFilter onto the agent-platform OCIRepository.
func TestPrereleasesOnRecord(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
	key := fixtureMCs + ":" + PlatformExtrasKustomizationPath(fixtureInstallation)
	patch := func(ops string) string {
		return "patches:\n  - patch: |-\n" + ops + "    target:\n      kind: OCIRepository\n      name: agent-platform\n"
	}
	cases := []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{"the release tag filter", map[string]string{key: patch("      - op: replace\n        path: /spec/ref/semver\n        value: \">=4.0.0-0 <5.0.0-0\"\n      - op: add\n        path: /spec/ref/semverFilter\n        value: \"^v?[0-9.]+$\"\n")}, true},
		{"the stable range", map[string]string{key: patch("      - op: replace\n        path: /spec/ref/semver\n        value: \">=4.0.0 <5.0.0\"\n")}, false},
		{"no kustomization", map[string]string{}, false},
	}
	for _, c := range cases {
		got, err := readPrereleasesOnRecord(t.Context(), files(c.files), inst)
		if err != nil || got != c.want {
			t.Errorf("%s: %v %v, want %v", c.name, got, err, c.want)
		}
	}
	if _, err := readPrereleasesOnRecord(t.Context(), files(map[string]string{key: "patches: [\n"}), inst); err == nil || !strings.Contains(err.Error(), PlatformExtrasKustomizationPath(fixtureInstallation)) {
		t.Errorf("a kustomization that does not parse: %v, want an error naming the file", err)
	}
}
