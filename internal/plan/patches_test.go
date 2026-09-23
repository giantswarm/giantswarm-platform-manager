package plan

import (
	"strings"
	"testing"
)

// The portal's kustomization as the render writes it: the chart line's JSON
// 6902 patch with the range in single quotes, and a strategic merge patch.
const renderedPatches = `# Rendered by giantswarm-platform-manager, customer-portal definition.
resources:
  - app-config.yaml
patches:
  - patch: |
      - op: remove
        path: /spec/ref/tag
      - op: add
        path: /spec/ref/semver
        value: '>=0.244.7 <1.0.0'
    target:
      kind: OCIRepository
      name: backstage
  - patch: |
      kind: HelmRelease
      spec:
        valuesFrom:
          - kind: ConfigMap
            name: app-config-backstage
    target:
      kind: HelmRelease
`

// A kustomization on record whose patch texts spell the render's YAML
// another way — the range in double quotes, the JSON 6902 patch as a JSON
// list, the strategic merge patch in flow style — is unchanged; a patch with
// another value, or the file changed outside its patch texts (a quoting
// style, a comment), is an update, and a file without patch texts is
// compared by its bytes.
func TestChangeReadsAPatchTextByItsValue(t *testing.T) {
	for name, current := range map[string]string{
		"double quotes": strings.Replace(renderedPatches, `'>=0.244.7 <1.0.0'`, `">=0.244.7 <1.0.0"`, 1),
		"a JSON list": strings.Replace(renderedPatches, "  - patch: |\n      - op: remove\n        path: /spec/ref/tag\n      - op: add\n        path: /spec/ref/semver\n        value: '>=0.244.7 <1.0.0'\n",
			`  - patch: '[{"op": "remove", "path": "/spec/ref/tag"}, {"op": "add", "path": "/spec/ref/semver", "value": ">=0.244.7 <1.0.0"}]'`+"\n", 1),
		"flow style": strings.Replace(renderedPatches, "      spec:\n        valuesFrom:\n          - kind: ConfigMap\n            name: app-config-backstage\n", "      spec: {valuesFrom: [{kind: ConfigMap, name: app-config-backstage}]}\n", 1),
		"the file's own indentation": strings.Replace(renderedPatches, "  - app-config.yaml", "- app-config.yaml", 1),
	} {
		if current == renderedPatches {
			t.Fatalf("%s: nothing replaced", name)
		}
		if c, _, _ := change(current, nil, renderedPatches); c != ChangeUnchanged {
			t.Errorf("%s: %s, want unchanged:\n%s", name, c, current)
		}
	}
	for name, current := range map[string]string{
		"another range":        strings.Replace(renderedPatches, `'>=0.244.7 <1.0.0'`, `">=0.240.0 <1.0.0"`, 1),
		"another source":       strings.Replace(renderedPatches, "name: app-config-backstage", "name: user-values-backstage", 1),
		"a comment":            strings.Replace(renderedPatches, "resources:\n", "# kept by hand\nresources:\n", 1),
		"a quoting style":      strings.Replace(renderedPatches, "kind: OCIRepository", `kind: "OCIRepository"`, 1),
		"a patch that is text": strings.Replace(renderedPatches, "        value: '>=0.244.7 <1.0.0'\n", "        value: [unclosed\n", 1),
	} {
		if c, _, _ := change(current, nil, renderedPatches); c != ChangeUpdate {
			t.Errorf("%s: %s, want update", name, c)
		}
	}
	const plain = "apiVersion: v1\nkind: ConfigMap\ndata:\n  a: '1'\n"
	if c, _, _ := change(strings.Replace(plain, "'1'", `"1"`, 1), nil, plain); c != ChangeUpdate {
		t.Errorf("a file without patch texts is compared by its bytes: %s", c)
	}
}
