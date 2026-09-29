package installations

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The collection stages the cases read: the fleet's default and the one that
// runs release candidates.
const stableStage, testingStage = "stable", "testing"

// The collections stage on record is the stage of the collection base the
// installation's collections kustomization names as a remote resource; an
// installation without the kustomization, or whose resources name no stage,
// has none.
func TestCollectionsStageFromTheRecord(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
	kustomization := fixtureMCs + ":" + CollectionsKustomizationPath(fixtureInstallation)
	stageBase := func(stage string) string {
		return "resources:\n  - https://github.com/" + fixtureBases + "//bases/collections/capz/stages/" + stage + "?ref=main\n"
	}
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"the stable stage", map[string]string{kustomization: collectionsFixture("main", pinnedDexApp)}, stableStage},
		{"the testing stage", map[string]string{kustomization: stageBase(testingStage)}, testingStage},
		{"a stage without the scheme", map[string]string{kustomization: "resources:\n  - github.com/" + fixtureBases + "//bases/collections/capa/stages/staging?ref=v1.2.0\n"}, "staging"},
		{"a local resource before the base", map[string]string{kustomization: "resources:\n  - ./extras\n  - https://github.com/" + fixtureBases + "//bases/collections/capa/stages/testing\n"}, testingStage},
		{"a base that is no stage", map[string]string{kustomization: "resources:\n  - https://github.com/" + fixtureBases + "//bases/collections/shared/base?ref=main\n"}, ""},
		{"no kustomization on record", map[string]string{}, ""},
	}
	for _, c := range cases {
		var reads []string
		got, err := readCollectionsStage(context.Background(), filesAt(c.files, &reads), inst)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A kustomization that cannot be read or parsed is an error of the report,
// naming the file.
func TestCollectionsStageErrors(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: fixtureMCs}}
	kustomization := fixtureMCs + ":" + CollectionsKustomizationPath(fixtureInstallation)

	var reads []string
	_, err := readCollectionsStage(context.Background(), filesAt(map[string]string{kustomization: "resources: [\n"}, &reads), inst)
	if err == nil || !strings.Contains(err.Error(), CollectionsKustomizationPath(fixtureInstallation)) {
		t.Errorf("a kustomization that does not parse: %v, want an error naming the file", err)
	}

	failing := func(context.Context, string, string, string) (string, error) { return "", errors.New("rate limited") }
	if _, err := readCollectionsStage(context.Background(), failing, inst); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("an unreadable kustomization: %v, want the read's error", err)
	}
}
