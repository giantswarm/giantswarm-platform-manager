package verify

import (
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// valuesLines are a file's leaves and their lines.
func valuesLines(content string) (map[string]string, map[string]int) {
	f := flattenLines(content)
	return f.values, f.lines
}

// The leaves every manifest opens with, and the field a ConfigMap's chart
// values are the text of.
const (
	apiVersionPath = "apiVersion"
	kindPath       = "kind"
	valuesField    = "data.values"
)

// appConfigDocuments are the documents the portal's app-config ConfigMap
// holds: the chart values, and the app-config inside them.
var appConfigDocuments = map[string]bool{valuesField: true, valuesField + ":backstage.appConfig": true}

// The portal's kustomization patches as the comparison names them — the
// chart line's JSON 6902 patch and the HelmRelease's strategic merge patch —
// and the app-config's title.
const (
	chartPatch   = "patches[0].patch"
	releasePatch = "patches[1].patch"
	titlePath    = "app.title"
)

// Every leaf of a file sits on a line: a mapping entry's key line, a
// sequence entry's "- " line, the first line of a multi-line scalar, the
// line of the key that holds an empty mapping or sequence; the values are
// flattenYAML's.
func TestFlattenLinesPlacesEveryLeaf(t *testing.T) {
	content := strings.Join([]string{
		"apiVersion: v1",  // 1
		"kind: ConfigMap", // 2
		"metadata:",       // 3
		"  name: c",       // 4
		"  labels: {}",    // 5
		"data:",           // 6
		"  script: |",     // 7
		"    line one",    // 8
		"    line two",    // 9
		"  list:",         // 10
		"  - a",           // 11
		"  - b",           // 12
		"  objects:",      // 13
		"  - name: x",     // 14
		"    port: 1",     // 15
		"  - name: y",     // 16
		"    port: 2",     // 17
		"  empty: []",     // 18
		"  patches:",      // 19
		"  - path: p",     // 20
		"  - path: q",     // 21
		"  multi: 'one",   // 22
		"    two'",        // 23
		"  after: 1",      // 24
		"",
	}, "\n")
	values, lines := valuesLines(content)
	want := map[string]int{
		apiVersionPath: 1, kindPath: 2, "metadata.name": 4, "metadata.labels": 5,
		"data.script": 7, "data.list[a]": 11, "data.list[b]": 12,
		"data.objects[x].name": 14, "data.objects[x].port": 15, "data.objects[y].name": 16, "data.objects[y].port": 17,
		"data.empty": 18, "data.patches[0].path": 20, "data.patches[1].path": 21, "data.multi": 22, "data.after": 24,
	}
	for path, line := range want {
		if lines[path] != line {
			t.Errorf("%s on line %d, want %d", path, lines[path], line)
		}
	}
	if len(lines) != len(want) || len(values) != len(want) {
		t.Errorf("%d lines, %d values, want %d: %v", len(lines), len(values), len(want), lines)
	}
	if values["data.script"] != "line one\nline two\n" || values["metadata.labels"] != "{}" || values["data.empty"] != "[]" || values["data.multi"] != "one two" || values["data.objects[y].port"] != "2" {
		t.Errorf("values: %v", values)
	}
	_, lines = valuesLines("kind: A\nmetadata:\n  name: a\n---\nkind: B\nmetadata:\n  name: b\n")
	if lines["[A//a].kind"] != 1 || lines["[B//b].metadata.name"] != 7 {
		t.Errorf("several documents: %v", lines)
	}
	if values, lines := valuesLines("not: [yaml"); values[""] != "not: [yaml" || lines[""] != 1 {
		t.Errorf("not YAML: %v %v", values, lines)
	}
}

// encryptedRecord is a Secret as SOPS puts it on record: the leaves under
// encrypted_regex ciphertext, everything else plaintext, SOPS's block last,
// indented its way.
const encryptedRecord = `apiVersion: v1
kind: Secret
metadata:
    name: s
    namespace: ns
type: Opaque
# kept comment
stringData:
    token: ENC[AES256_GCM,data:x,iv:y,tag:z,type:str]
    extra: ENC[AES256_GCM,data:e,iv:y,tag:z,type:str]
data:
    blob: ENC[AES256_GCM,data:b,iv:y,tag:z,type:str]
sops:
    kms: []
    age:
        - recipient: age1abc
          enc: |
            -----BEGIN AGE ENCRYPTED FILE-----
            YWJj
            -----END AGE ENCRYPTED FILE-----
    lastmodified: "2026-09-21T10:00:00Z"
    mac: ENC[AES256_GCM,data:m,iv:y,tag:z,type:str]
    encrypted_regex: ^(data|stringData)$
    version: 3.9.0
`

// An encrypted record is shown with SOPS's block dropped and the leaves
// under its encrypted_regex redacted; every other line — keys, metadata,
// type, a comment, the indentation — stays as it is on record, and no
// ciphertext is left. Without the regex every value is redacted.
func TestRedactLeavesKeepsTheStructure(t *testing.T) {
	got := redactLeaves(encryptedRecord, encryptedLeaves(flattenYAML(encryptedRecord)))
	want := "apiVersion: v1\nkind: Secret\nmetadata:\n    name: s\n    namespace: ns\ntype: Opaque\n# kept comment\nstringData:\n    token: <encrypted>\n    extra: <encrypted>\ndata:\n    blob: <encrypted>\n"
	if got != want {
		t.Errorf("redacted:\n%s\nwant:\n%s", got, want)
	}
	if plan.Ciphertext(got) || strings.Contains(got, "sops") {
		t.Errorf("ciphertext or SOPS's block left:\n%s", got)
	}
	values, lines := valuesLines(got)
	if values["stringData.token"] != Redacted || values["type"] != "Opaque" || lines["data.blob"] != 12 || len(values) != 8 {
		t.Errorf("the redacted file's leaves: %v %v", values, lines)
	}
	all := redactLeaves("a: 1\nb:\n  c: x # note\n  d: |\n    two\n    lines\ne:\n- 2\n", func(string) bool { return true })
	if all != "a: <encrypted>\nb:\n  c: <encrypted>\n  d: <encrypted>\ne:\n- <encrypted>\n" {
		t.Errorf("every value: %q", all)
	}
	if got := redactLeaves("not: [yaml", func(string) bool { return true }); got != "not: [yaml" {
		t.Errorf("not YAML: %q", got)
	}
	if !payload("data.values") || !payload("stringData.values") || !payload("[ConfigMap/ns/c].data.values") || !payload(chartPatch) || !payload("[Kustomization/ns/k].patches[1].patch") ||
		payload("spec.patch") || payload("patches[0].target.kind") || payload("patches.patch") || payload("") {
		t.Error("payload is a ConfigMap's data, a Secret's stringData or a kustomization's patch")
	}
}

// The files of an encrypted record are shown redacted, the file as the plan
// writes it too where it kept ciphertext; a plain file and a file the record
// lacks are shown as they are.
func TestShownRedactsTheEncryptedFiles(t *testing.T) {
	rendered := "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: ns\ntype: Opaque\nstringData:\n  token: GENERATED(token)\n  extra: ENC[AES256_GCM,data:e,iv:y,tag:z,type:str]\n"
	files := []plan.File{
		{Path: "secret.yaml", Content: rendered, Current: encryptedRecord},
		{Path: "plain.yaml", Content: "a: 2\n", Current: "a: 1\n"},
		{Path: "new.yaml", Content: "a: ENC[x]\n"},
	}
	shown(files)
	if strings.Contains(files[0].Current, "sops") || plan.Ciphertext(files[0].Current) || !strings.Contains(files[0].Current, "type: Opaque") {
		t.Errorf("the record:\n%s", files[0].Current)
	}
	if files[0].Content != "apiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: ns\ntype: Opaque\nstringData:\n  token: <encrypted>\n  extra: <encrypted>\n" {
		t.Errorf("the plan's file with kept ciphertext:\n%s", files[0].Content)
	}
	if files[1].Content != "a: 2\n" || files[1].Current != "a: 1\n" || files[2].Content != "a: ENC[x]\n" || files[2].Current != "" {
		t.Errorf("a plain file and a new one stay: %+v", files[1:])
	}
}

// A kustomization's patch text that holds a mapping — a strategic merge
// patch, the portal's HelmRelease patch with its values sources — is the
// mapping's leaves under patches[n].patch, each on its line of the file and
// a list entry keyed by its name; a patch that holds a list (a JSON 6902
// patch) stays one leaf, as does the patch's target. levels names a leaf
// at every document it sits in, innermost first, then in the file.
func TestFlattenLinesReadsAKustomizationPatch(t *testing.T) {
	content := strings.Join([]string{
		"resources:",                  // 1
		"  - app-config.yaml",         // 2
		"patches:",                    // 3
		"  - patch: |",                // 4
		"      - op: remove",          // 5
		"        path: /spec/ref/tag", // 6
		"    target:",                 // 7
		"      kind: OCIRepository",   // 8
		"  - patch: |",                // 9
		"      apiVersion: helm.toolkit.fluxcd.io/v2", // 10
		"      kind: HelmRelease",                     // 11
		"      spec:",                                 // 12
		"        valuesFrom:",                         // 13
		"          - kind: ConfigMap",                 // 14
		"            name: app-config-backstage",      // 15
		"          - kind: Secret",                    // 16
		"            name: user-secrets-backstage",    // 17
		"    target:",                                 // 18
		"      kind: HelmRelease",                     // 19
		"",
	}, "\n")
	const hr, secretSource = "patches[1].patch:", "spec.valuesFrom[user-secrets-backstage]"
	values, lines := valuesLines(content)
	want := map[string]int{
		"resources[app-config.yaml]": 2, chartPatch: 4, "patches[0].target.kind": 8,
		hr + apiVersionPath: 10, hr + kindPath: 11, hr + "spec.valuesFrom[app-config-backstage].kind": 14, hr + "spec.valuesFrom[app-config-backstage].name": 15,
		hr + secretSource + ".kind": 16, hr + secretSource + ".name": 17, "patches[1].target.kind": 19,
	}
	for path, line := range want {
		if lines[path] != line {
			t.Errorf("%s on line %d, want %d", path, lines[path], line)
		}
	}
	if len(lines) != len(want) || len(values) != len(want) {
		t.Errorf("%d lines, %d values, want %d: %v", len(lines), len(values), len(want), values)
	}
	if values[chartPatch] != "- op: remove\n  path: /spec/ref/tag\n" || values[hr+secretSource+".kind"] != "Secret" || values[hr+kindPath] != "HelmRelease" {
		t.Errorf("values %v", values)
	}
	docs := flattenLines(content).documents
	if !reflect.DeepEqual(docs, map[string]bool{releasePatch: true}) {
		t.Errorf("the documents the file holds: %v", docs)
	}
	if got := levels(docs, hr+secretSource+".kind"); !reflect.DeepEqual(got, []string{secretSource + ".kind", hr + secretSource + ".kind"}) {
		t.Errorf("the levels of a leaf inside the patch: %v", got)
	}
	inText := "backstage.appConfig:" + titlePath
	if got := levels(appConfigDocuments, valuesField+":"+inText); !reflect.DeepEqual(got, []string{titlePath, inText, valuesField + ":" + inText}) {
		t.Errorf("the levels of text inside text: %v", got)
	}
	if got := levels(nil, "plain.leaf"); !reflect.DeepEqual(got, []string{"plain.leaf"}) {
		t.Errorf("the levels of a leaf of the file: %v", got)
	}
}

// A string that holds a YAML mapping over several lines is the mapping's
// leaves, each under the field's path and ":" — text inside text too — on
// its line of the file where the text is a literal block scalar, on the
// field's line where a quoted scalar folds its lines; a list held as text
// (a patch) and a one-line "key: value" stay one leaf, as does every string
// outside a ConfigMap's data, a Secret's stringData or a kustomization's
// patch. flattenYAML's leaves are the same, and innerPath and holder split
// a path at its last step into a document, outside brackets.
func TestFlattenLinesDescendsIntoText(t *testing.T) {
	content := strings.Join([]string{
		"apiVersion: v1",                            // 1
		"kind: ConfigMap",                           // 2
		"data:",                                     // 3
		"  values: |",                               // 4
		"    backstage:",                            // 5
		"      appConfig: |",                        // 6
		"        app:",                              // 7
		"          title: Dev Portal",               // 8
		"        grafana:",                          // 9
		"          domain: g.example.test",          // 10
		"      extraVolumeMounts: []",               // 11
		"  quoted: \"route:\\n  enabled: true\\n\"", // 12
		"  patch: |",                                // 13
		"    - op: replace",                         // 14
		"      path: /a",                            // 15
		"  note: 'Note: one line'",                  // 16
		"spec:",                                     // 17
		"  patch: |",                                // 18
		"    b: 2",                                  // 19
		"",
	}, "\n")
	values, lines := valuesLines(content)
	want := map[string]int{
		apiVersionPath: 1, kindPath: 2,
		"data.values:backstage.appConfig:app.title": 8, "data.values:backstage.appConfig:grafana.domain": 10,
		"data.values:backstage.extraVolumeMounts": 11, "data.quoted:route.enabled": 12, "data.patch": 13, "data.note": 16, "spec.patch": 18,
	}
	for path, line := range want {
		if lines[path] != line {
			t.Errorf("%s on line %d, want %d", path, lines[path], line)
		}
	}
	if len(lines) != len(want) || len(values) != len(want) {
		t.Errorf("%d lines, %d values, want %d: %v", len(lines), len(values), len(want), lines)
	}
	if values["data.values:backstage.appConfig:app.title"] != "Dev Portal" || values["data.values:backstage.extraVolumeMounts"] != "[]" || values["data.quoted:route.enabled"] != "true" || values["data.patch"] != "- op: replace\n  path: /a\n" || values["data.note"] != "Note: one line" || values["spec.patch"] != "b: 2\n" {
		t.Errorf("values %v", values)
	}
	if flat := flattenYAML(content); len(flat) != len(want) || flat["data.values:backstage.appConfig:grafana.domain"] != "g.example.test" {
		t.Errorf("flattenYAML: %v", flat)
	}
	decoded := map[string]string{}
	flatten(map[string]any{"data": map[string]any{"values": "backstage:\n  appConfig: |\n    app:\n      title: x\n"}}, "", decoded)
	if len(decoded) != 1 || decoded["data.values:backstage.appConfig:app.title"] != "x" {
		t.Errorf("a decoded value flattens the same way: %v", decoded)
	}
	if docs := flattenLines(content).documents; !reflect.DeepEqual(docs, map[string]bool{valuesField: true, valuesField + ":backstage.appConfig": true, "data.quoted": true}) {
		t.Errorf("the documents the file holds: %v", docs)
	}
	docs := appConfigDocuments
	for p, inner := range map[string]string{
		"data.values:backstage.appConfig:app.title":                                       titlePath,
		"data.values:backstage.appConfig:app.extensions[14].entity-card:catalog/labels":   "app.extensions[14].entity-card:catalog/labels",
		"data.values:backstage.appConfig:app.extensions[29].page:scaffolder.config.title": "app.extensions[29].page:scaffolder.config.title",
		"data.values:servers[http://x:8080/mcp].url":                                      "servers[http://x:8080/mcp].url",
		"data.values:backstage.other:key":                                                 "backstage.other:key",
		"a.b[http://x:8080/mcp].c":                                                        "a.b[http://x:8080/mcp].c",
		"spec.entity-card:catalog/labels":                                                 "spec.entity-card:catalog/labels",
		"":                                                                                "",
	} {
		if got := innerPath(docs, p); got != inner {
			t.Errorf("innerPath(%q) = %q, want %q", p, got, inner)
		}
	}
	if holder(docs, "data.values:backstage.appConfig:app.extensions[14].entity-card:catalog/labels") != "data.values:backstage.appConfig" || holder(docs, "data.values:route.enabled") != "data.values" || holder(docs, "a.b[http://x:8080/mcp].c") != "" {
		t.Error("holder is the field whose text the leaf sits in, a key's colon no step")
	}
}
