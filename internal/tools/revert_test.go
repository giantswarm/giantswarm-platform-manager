package tools

import (
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// A merge is reverted when every file it changed carries its blob from
// before the merge again — a file it added absent, one it modified or
// removed as it was — and stands while any file carries what it wrote or a
// later change rewrote one.
func TestRevertedComparesEveryFileWithItsBlobBeforeTheMerge(t *testing.T) {
	const added, modified, removed = "added", "modified", "removed"
	ch := gh.MergeChange{Commit: "m", Files: []gh.FileChange{{Path: added, Blob: "a1"}, {Path: modified, Blob: "m2", Before: "m1"}, {Path: removed, Before: "r1"}}}
	for name, tc := range map[string]struct {
		blobs map[string]string
		want  bool
	}{
		"as merged":                     {map[string]string{added: "a1", modified: "m2"}, false},
		"reverted":                      {map[string]string{modified: "m1", removed: "r1"}, true},
		"one file still as merged":      {map[string]string{added: "a1", modified: "m1", removed: "r1"}, false},
		"a later change, not a revert":  {map[string]string{modified: "m3", removed: "r1"}, false},
		"the removed file not restored": {map[string]string{modified: "m1"}, false},
	} {
		if got := reverted(ch, tc.blobs); got != tc.want {
			t.Errorf("%s: reverted %v, want %v", name, got, tc.want)
		}
	}
	if reverted(gh.MergeChange{Commit: "empty"}, nil) {
		t.Error("a merge that changed nothing reads reverted")
	}
}
