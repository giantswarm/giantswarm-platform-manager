package installations

import (
	"strings"
	"testing"
)

func TestDexAppCollectionComponentsOverrideTheBaseTag(t *testing.T) {
	const root = "bases/collections/capa/stages/stable/kustomization.yaml"
	const component = "bases/collections/shared/component/kustomization.yaml"
	const namespace = "  namespace: giantswarm\n"
	manifest := "apiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: dex-app\n" + namespace + "spec:\n  ref:\n    tag: 3.4.24\n"
	for _, test := range []struct {
		name, component, pin, want, wantError string
	}{
		{name: "component semver wins over tag", component: "patches:\n  - target: {kind: OCIRepository}\n    patch: |\n      kind: OCIRepository\n      metadata: {name: patched-by-target-selector}\n      spec:\n        ref:\n          tag: null\n          semver: '>=0.0.0-0'\n", wantError: "not an exact version"},
		{name: "tag pin does not override component semver", component: "patches:\n  - target: {kind: OCIRepository}\n    patch: |\n      - op: add\n        path: /spec/ref/semver\n        value: '>=0.0.0-0'\n", pin: "      - op: add\n        path: /spec/ref/tag\n        value: 3.4.0\n", wantError: "not an exact version"},
		{name: "installation clears component semver", component: "patches:\n  - target: {kind: OCIRepository}\n    patch: |\n      - op: add\n        path: /spec/ref/semver\n        value: '>=0.0.0-0'\n", pin: "      - op: replace\n        path: /spec/ref\n        value: {tag: 3.4.0}\n", want: "3.4.0"},
		{name: "component pin", component: "patches:\n  - target: {kind: OCIRepository, name: dex-.*}\n    patch: |\n      - op: replace\n        path: /spec/ref/tag\n        value: 3.4.0\n", want: "3.4.0"},
		{name: "unrelated component pin", component: "patches:\n  - target: {kind: OCIRepository, name: other}\n    patch: |\n      - op: replace\n        path: /spec/ref/tag\n        value: 1.0.0\n", want: "3.4.24"},
		{name: "unresolved label selector holds the comparison", component: "patches:\n  - target: {kind: OCIRepository, labelSelector: team=identity}\n    patch: |\n      - op: replace\n        path: /spec/ref/tag\n        value: 3.4.0\n", wantError: "additional patch selectors"},
		{name: "opaque replacement holds the comparison", component: "replacements:\n  - source: {kind: ConfigMap, name: versions}\n", wantError: "cannot establish the exact Dex version"},
		{name: "cyclic component holds the comparison", component: "components: [./]\n", wantError: "cyclic collection"},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]string{
				fixtureBases + "@main:" + DexAppBasePath: manifest,
				fixtureBases + "@main:" + root:           "resources: [../../../shared/base/dex-app.yaml]\ncomponents: [../../../shared/component]\n",
				fixtureBases + "@main:" + component:      test.component,
			}
			customization := collectionsFixture("main", "")
			if test.pin != "" {
				customization += "patches:\n  - target: {kind: OCIRepository, name: dex-app, namespace: giantswarm}\n    patch: |\n" + test.pin
			}
			var reads []string
			version, err := DexAppVersionIn(t.Context(), filesAt(files, &reads), fixtureInstallation, fixtureMCs, customization)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("got %+v, %v; want %q", version, err, test.wantError)
				}
			} else if err != nil || version.Version != test.want {
				t.Fatalf("got %+v, %v; want %q", version, err, test.want)
			}
		})
	}
}
