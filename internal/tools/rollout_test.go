package tools

import "testing"

// A merge changes the values of a file when any of its YAML documents
// changes, not only the first (giantswarm-platform-manager#891, item 6); a
// comment or the layout changes none, and a file that is no YAML differs.
func TestSameDocumentsReadsEveryDocument(t *testing.T) {
	const before = "a: 1\n---\nb: 2\n"
	for _, c := range []struct {
		name  string
		after string
		same  bool
	}{
		{"the same documents", before, true},
		{"a comment and the layout", "# the values\na:   1\n---\n\nb: 2 # two\n", true},
		{"a trailing separator", before + "---\n", true},
		{"a document of a comment alone", before + "---\n# nothing here\n", true},
		{"the second document changed", "a: 1\n---\nb: 3\n", false},
		{"a document added", before + "---\nc: 4\n", false},
		{"a document removed", "a: 1\n", false},
		{"no YAML", "a: [1\n", false},
	} {
		if got := sameDocuments(before, c.after); got != c.same {
			t.Errorf("%s: same %v, want %v", c.name, got, c.same)
		}
	}
	if !sameDocuments("", "") || sameDocuments("", "a: 1\n") {
		t.Error("an empty file: the same as an empty one only")
	}
}
