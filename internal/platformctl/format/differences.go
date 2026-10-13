package format

import (
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// The markers of a key a dry run's file differs in.
const (
	keyChanged = "changed"
	keyAdded   = "added"
	keyRemoved = "removed"
)

// keyDiff is one key a file differs in: its path and marker, never a value.
type keyDiff struct {
	path   string
	marker string
	secret bool
}

// differences prints, under the files of a dry run, one block per file the
// commit changes with the keys it differs in — the comparison's differences
// (features[].dimensions[].differences of the JSON answer) by file, each key
// once, with its marker. A value is never printed: the files hold secrets
// and the person's values; a key the record holds encrypted is named and
// marked secret. An unchanged file, and one without a key-level difference,
// prints no block.
func (p *printer) differences(files []plan.File, features []verify.Feature) {
	byFile := keyDiffs(features)
	for _, f := range files {
		if f.Change == plan.ChangeUnchanged {
			continue
		}
		keys := byFile[fileKey(f.Repository, f.Path)]
		if len(keys) == 0 {
			continue
		}
		p.f("    Differs in %s:%s:\n", f.Repository, f.Path)
		rows := make([][]string, 0, len(keys))
		for _, k := range keys {
			path := k.path
			if k.secret {
				path += "  (secret)"
			}
			rows = append(rows, []string{k.marker, path})
		}
		p.table(6, rows)
	}
}

// keyDiffs are the differences of the features by file key, each path once,
// sorted by path. A live difference (an object, no file) is not a file's.
func keyDiffs(features []verify.Feature) map[string][]keyDiff {
	out := map[string][]keyDiff{}
	seen := map[string]bool{}
	for _, f := range features {
		for _, d := range f.Dimensions {
			for _, diff := range d.Differences {
				if diff.File == "" || diff.Path == "" || seen[diff.File+"#"+diff.Path] {
					continue
				}
				seen[diff.File+"#"+diff.Path] = true
				out[diff.File] = append(out[diff.File], keyDiff{
					path:   diff.Path,
					marker: marker(diff),
					secret: diff.Rendered == verify.Redacted || diff.Current == verify.Redacted,
				})
			}
		}
	}
	for _, keys := range out {
		slices.SortFunc(keys, func(a, b keyDiff) int { return strings.Compare(a.path, b.path) })
	}
	return out
}

// marker says whether the commit adds the key (the record lacks it),
// removes it (the plan renders none) or changes its value. A side holds the
// key when it shows a value or a line for it.
func marker(d verify.Difference) string {
	rendered := d.Rendered != "" || d.Line > 0
	current := d.Current != "" || d.CurrentLine > 0
	switch {
	case !current:
		return keyAdded
	case !rendered:
		return keyRemoved
	}
	return keyChanged
}

// fileKey is a difference's file: the repository and the path.
func fileKey(repository, path string) string { return repository + ":" + path }
