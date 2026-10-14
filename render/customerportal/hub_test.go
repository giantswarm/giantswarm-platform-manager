package customerportal

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The hub's shape changes the chart line of the portal kustomization on
// record and no other byte of it — the record's comments, quoting, block
// style and the order of its entries stand — and refuses a kustomization
// whose OCIRepository patch it cannot move in place.
func TestWithChartLine(t *testing.T) {
	const onRecord = "resources:\n  - https://github.com/giantswarm/management-cluster-bases/extras/backstage/main?ref=main\n  - app-config.yaml\n" +
		"  - tunnelport-spiffe-bundle.yaml\npatches:\n  - patch: |-\n      - op: remove\n        path: /spec/ref/tag\n      - op: add\n        path: /spec/ref/semver\n" +
		"        value: \">=2.1.0 <3.0.0\"\n    target:\n      kind: OCIRepository\n      name: backstage\n      namespace: flux-giantswarm\n" +
		"  - patch: |\n      apiVersion: helm.toolkit.fluxcd.io/v2\n      kind: HelmRelease\n      metadata:\n        name: backstage\n    target:\n      kind: HelmRelease\n      name: backstage\n"
	got, err := withChartLine([]byte(onRecord), ">=2.1.0 <4.0.0")
	if want := strings.Replace(onRecord, `">=2.1.0 <3.0.0"`, `">=2.1.0 <4.0.0"`, 1); err != nil || string(got) != want {
		t.Fatalf("%v:\n%s\nwant:\n%s", err, got, want)
	}
	single := strings.Replace(onRecord, `">=2.1.0 <3.0.0"`, `'>=2.1.0 <3.0.0'`, 1)
	if got, err := withChartLine([]byte(single), ">=2.1.0 <4.0.0"); err != nil || string(got) != strings.Replace(single, "<3.0.0", "<4.0.0", 1) {
		t.Fatalf("single quotes on record: %v:\n%s", err, got)
	}
	if got, err := withChartLine([]byte(onRecord), ">=2.1.0 <3.0.0"); err != nil || string(got) != onRecord {
		t.Fatalf("the line on record: %v:\n%s", err, got)
	}

	for name, record := range map[string]string{
		"no semver operation":   strings.Replace(onRecord, "/spec/ref/semver", "/spec/ref/tag", 1),
		"no OCIRepository":      strings.Replace(onRecord, "kind: OCIRepository", "kind: GitRepository", 1),
		"a flow-style patch":    "patches:\n  - patch: '[{op: add, path: /spec/ref/semver, value: \">=2.1.0 <3.0.0\"}]'\n    target: {kind: OCIRepository, name: backstage}\n",
		"a comment on the line": strings.Replace(onRecord, `<3.0.0"`, `<3.0.0" # held`, 1),
	} {
		if got, err := withChartLine([]byte(record), ">=2.1.0 <4.0.0"); err == nil {
			t.Errorf("%s: no refusal:\n%s", name, got)
		}
	}
}

// The hub's Dev Portal kept by hand renders the hub's shape: the portal's
// kustomization edited on record and nothing else, no include, no secret
// asked for.
func TestHubShape(t *testing.T) {
	raw, _ := loadInput(t, "giantswarm-owned-with-platform")
	raw["hubPortal"] = true
	res, err := Render(raw, nil, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, byPath := range res.Files {
		for path, f := range byPath {
			files = append(files, path)
			if f.Edit == nil || f.Content != nil || len(f.Generated) > 0 {
				t.Errorf("%s is rendered whole, not edited on record", path)
			}
		}
	}
	if len(files) != 1 || !strings.HasSuffix(files[0], "/extras/backstage/backstage/kustomization.yaml") || len(res.Includes) != 0 || len(res.Probes) == 0 {
		t.Fatalf("files %v, includes %v, probes %d", files, res.Includes, len(res.Probes))
	}
	in, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if fields := in.SuppliedSecretFields(); len(fields) != 0 {
		t.Fatalf("the hub's shape asks for %v", fields)
	}
}
