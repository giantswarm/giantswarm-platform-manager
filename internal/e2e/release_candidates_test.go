package e2e

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// hazelRunsReleaseCandidates puts gitops.prereleases into hazel's platform
// values on record and the release tag filter on its agent-platform
// OCIRepository, as the definition renders them together: hazel followed the
// release candidates, and the policy no longer runs them there (its
// organisation is not among releaseCandidates' customers).
func hazelRunsReleaseCandidates(t *testing.T, g *fakeGitHub) {
	t.Helper()
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	g.mu.Lock()
	current := g.files[hubConfigs][marker]
	g.mu.Unlock()
	if current == "" {
		t.Fatal("hazel's platform values are not a fixture")
	}
	g.addFile(hubConfigs, marker, current+"gitops:\n  prereleases: true\n")
	g.addFile(hubMCs, installations.PlatformExtrasKustomizationPath(hub), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\npatches:\n"+
		"  - patch: |-\n      - op: replace\n        path: /spec/ref/semver\n        value: \">=4.0.0-0 <5.0.0-0\"\n      - op: add\n        path: /spec/ref/semverFilter\n        value: \"^v?[0-9]+\\\\.[0-9]+\\\\.[0-9]+(-rc\\\\.[0-9]+)?$\"\n"+
		"    target:\n      kind: OCIRepository\n      name: agent-platform\n")
}

// Dropping the release candidates while agent-platform's newest candidate is
// ahead of the stable release would move the installation down to it: the
// comparison plans the drop, and the commit is held until the candidate is
// promoted.
func TestDroppingReleaseCandidatesWaitsForThePromotion(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	hazelRunsReleaseCandidates(t, st.ghs)
	st.ghs.setReleases(installations.PlatformRepository, fakeRelease{"v4.2.0-rc.2", true}, fakeRelease{"v4.2.0-rc.1", true}, fakeRelease{"v4.1.0", false}, fakeRelease{"v4.1.0-rc.1", true})
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	args := func() map[string]any {
		return map[string]any{tools.ArgInstallation: hub, tools.ArgInputs: minimalInputs(nil)}
	}

	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args())
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, hub)
	want := "v4.2.0-rc.2 is ahead of the latest stable release: dropping gitops.prereleases now would move it down to the latest stable release v4.1.0. Promote v4.2.0-rc.2 first"
	if !p.DropsPrereleases || !strings.Contains(p.CommitRefused, want) {
		t.Fatalf("the dry run: drops %v, commit refused %q", p.DropsPrereleases, p.CommitRefused)
	}

	_, text, isErr = commitCall(t, c, tools.ToolReconcileCapability, args())
	if !isErr || !strings.Contains(text, want) || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("the commit: %v %s", isErr, text)
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("the remote saw %d pull request(s)", len(prs))
	}

	// Promoted: the stable release has caught up, and nothing holds the drop.
	st.ghs.setReleases(installations.PlatformRepository, fakeRelease{"v4.2.0", false}, fakeRelease{"v4.2.0-rc.2", true}, fakeRelease{"v4.1.0", false})
	out, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, args())
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, out, hub); !p.DropsPrereleases || strings.Contains(p.CommitRefused, "release candidates") {
		t.Fatalf("after the promotion: drops %v, commit refused %q", p.DropsPrereleases, p.CommitRefused)
	}
}

// An installation that runs release candidates, whose platform's releases
// cannot be read, stays readable: a plan that keeps the candidates goes
// ahead, and only a commit that drops them is held, since a candidate ahead
// must not read as none.
func TestUnreadablePlatformReleasesHoldOnlyTheDrop(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	hazelRunsReleaseCandidates(t, st.ghs)
	st.ghs.failReleases(installations.PlatformRepository)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{hub}})
	if isErr {
		t.Fatal(text)
	}
	r := find(t, out, hub)
	if !r.Readable || r.Record == nil || !r.Record.PlatformCandidateUnread || !strings.Contains(strings.Join(r.Errors, " "), "agent-platform's release candidates") {
		t.Fatalf("hazel with the platform's releases unread: readable %v, %+v, %v", r.Readable, r.Record, r.Errors)
	}

	seedRemote(t, st)
	planned, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, planned, hub); !p.DropsPrereleases || !strings.Contains(p.CommitRefused, "could not be read") {
		t.Fatalf("the dry run: drops %v, commit refused %q", p.DropsPrereleases, p.CommitRefused)
	}
}
