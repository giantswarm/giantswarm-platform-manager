package installations

import (
	"strings"
	"testing"
)

func TestDexAppOCIRepository(t *testing.T) {
	const baseVersion, pinnedVersion, inexact = "3.4.24", "3.4.0", "not an exact version"
	const header = "kind: OCIRepository\nmetadata:\n  name: dex-app\nspec:\n  ref:\n"
	const release = "---\nkind: HelmRelease\nmetadata:\n  name: dex-app\nspec:\n  chartRef:\n    kind: OCIRepository\n    name: dex-app\n"
	const target = "patches:\n  - target:\n      kind: OCIRepository\n      name: dex-app\n    patch: |\n"
	cases := []struct {
		name, manifest, patches, want, wantError string
	}{
		{name: "base tag with a release", manifest: header + "    tag: 3.4.24\n" + release, want: baseVersion},
		{name: "release before source", manifest: release + "---\n" + header + "    tag: 2.4.0\n", want: "2.4.0"},
		{name: "OCI pin ignores legacy App pin", manifest: header + "    tag: 2.2.3\n", patches: strings.TrimPrefix(collectionsFixture("main", "3.2.5"), collectionsFixture("main", "")) + "  - target:\n      kind: OCIRepository\n      name: dex-app\n    patch: |\n      - op: replace\n        path: /spec/ref/tag\n        value: 3.4.0\n", want: pinnedVersion},
		{name: "legacy pin alone does not pin an OCI source", manifest: header + "    tag: 3.4.24\n", patches: strings.TrimPrefix(collectionsFixture("main", "3.2.5"), collectionsFixture("main", "")), want: baseVersion},
		{name: "semver wins over tag", manifest: header + "    tag: 3.4.24\n    semver: '>=2.0.0'\n", wantError: inexact},
		{name: "digest wins over tag", manifest: header + "    tag: 3.4.24\n    digest: sha256:fixture\n", wantError: inexact},
		{name: "patch adds semver", manifest: header + "    tag: 3.4.24\n", patches: target + "      - op: add\n        path: /spec/ref/semver\n        value: '>=3.0.0'\n", wantError: inexact},
		{name: "patch clears semver", manifest: header + "    semver: '>=0.0.0-0'\n", patches: target + "      - op: add\n        path: /spec/ref/tag\n        value: 3.4.0\n      - op: remove\n        path: /spec/ref/semver\n", want: pinnedVersion},
		{name: "patch replaces reference", manifest: header + "    semver: '*'\n", patches: target + "      - op: replace\n        path: /spec/ref\n        value: {tag: 3.4.0}\n", want: pinnedVersion},
		{name: "strategic pin", manifest: header + "    tag: 2.2.3\n", patches: "patches:\n  - patch: |\n      kind: OCIRepository\n      metadata:\n        name: dex-app\n      spec:\n        ref:\n          tag: 3.4.0\n", want: pinnedVersion},
		{name: "targeted strategic pin clears semver", manifest: header + "    semver: '*'\n", patches: target + "      kind: OCIRepository\n      metadata:\n        name: patched-by-target-selector\n      spec:\n        ref:\n          tag: 3.4.0\n          semver: null\n", want: pinnedVersion},
		{name: "unrelated patch", manifest: header + "    tag: 3.4.24\n", patches: strings.ReplaceAll(target, "name: dex-app", "name: other") + "      - op: replace\n        path: /spec/ref/tag\n        value: 1.0.0\n", want: baseVersion},
		{name: "missing tag", manifest: header + "    tag: ''\n", wantError: "sets no spec.ref.tag"},
		{name: "non-semantic tag", manifest: header + "    tag: latest\n", wantError: "is no semantic version"},
		{name: "malformed patch", manifest: header + "    tag: 3.4.24\n", patches: target + "      [invalid\n", wantError: "yaml:"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{fixtureBases + "@main:" + DexAppBasePath: test.manifest}
			var reads []string
			got, err := DexAppVersionIn(t.Context(), filesAt(files, &reads), fixtureInstallation, fixtureMCs, collectionsFixture("main", "")+test.patches)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("got %+v, %v; want %q", got, err, test.wantError)
				}
				return
			}
			if err != nil || got.Version != test.want {
				t.Fatalf("got %+v, %v; want %s", got, err, test.want)
			}
		})
	}
}
