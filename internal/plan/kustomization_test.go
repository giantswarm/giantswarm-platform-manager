package plan

import (
	"strings"
	"testing"
)

// listEntry edits a kustomization on its nodes: comments and order stay, an
// entry already listed changes nothing, a missing list is appended, an
// empty or null one is filled, and anything but a mapping is refused.
func TestListEntry(t *testing.T) {
	const extras = "# The installation's extras.\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./monitoring/ # keep\n"
	cases := []struct {
		name, current, list, entry, want string
		changed                          bool
		err                              bool
	}{
		{name: "appends to the list", current: extras, list: ListResources, entry: "./agent-platform/", changed: true,
			want: "# The installation's extras.\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./monitoring/ # keep\n  - ./agent-platform/\n"},
		{name: "listed already", current: extras, list: ListResources, entry: "./monitoring/", want: extras},
		{name: "adds a missing list", current: extras, list: ListComponents, entry: "./agent-platform/", changed: true,
			want: "# The installation's extras.\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./monitoring/ # keep\ncomponents:\n  - ./agent-platform/\n"},
		{name: "fills an empty list", current: "kind: Kustomization\nresources: []\n", list: ListResources, entry: "./a/", changed: true, want: "kind: Kustomization\nresources:\n  - ./a/\n"},
		{name: "fills a null list", current: "kind: Kustomization\nresources:\n", list: ListResources, entry: "./a/", changed: true, want: "kind: Kustomization\nresources:\n  - ./a/\n"},
		{name: "not a mapping", current: "- a\n", list: ListResources, entry: "./a/", err: true},
		{name: "empty file", current: "", list: ListResources, entry: "./a/", err: true},
		{name: "the list is a mapping", current: "resources:\n  a: b\n", list: ListResources, entry: "./a/", err: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := listEntry([]byte(tc.current), tc.list, tc.entry)
			if (err != nil) != tc.err {
				t.Fatalf("error %v", err)
			}
			if err == nil && (changed != tc.changed || string(got) != tc.want) {
				t.Fatalf("changed %v, got:\n%s", changed, strings.ReplaceAll(string(got), "\n", "\\n\n"))
			}
		})
	}
}
