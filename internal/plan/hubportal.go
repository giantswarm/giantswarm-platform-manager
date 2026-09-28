package plan

import (
	"fmt"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// HubRefusal says why a commit of p is refused for the hub's Dev Portal on
// record: the plan removes sections of it that the record carries with a
// value (HubSections) — the pages the hub serves over muster, its incident,
// CircleCI and scaffolder configuration, its Postgres database — which the
// definition does not render, so the commit would turn the hub's portal
// into a customer's. Empty where the plan removes none: a customer's portal,
// a section on record without a value. The comparison runs either way; only
// the commit is held.
func (p Installation) HubRefusal() string {
	if len(p.HubSections) == 0 {
		return ""
	}
	return fmt.Sprintf("the portal on record carries sections of the hub's Dev Portal that this definition does not render (%s), and a commit would remove them; the hub's shape has to become an input of the definition first — until then keep this portal by hand",
		byFile(p.HubSections))
}

// byFile names removal keys by the file they are in: each file with the
// paths of its keys, the files in the order they first appear.
func byFile(keys []string) string {
	var files []string
	paths := map[string][]string{}
	for _, k := range keys {
		file, path := splitKey(k)
		if _, seen := paths[file]; !seen {
			files = append(files, file)
		}
		paths[file] = append(paths[file], path)
	}
	parts := make([]string, len(files))
	for i, f := range files {
		parts[i] = f + ": " + strings.Join(paths[f], ", ")
	}
	return strings.Join(parts, "; ")
}

// splitKey is a removal key's file and the path in it: a backstage key
// names its fileset (backstage:<fileset>:<path>), every other key's file is
// its prefix.
func splitKey(key string) (file, path string) {
	prefix, rest, _ := strings.Cut(key, ":")
	if prefix == definitions.KindBackstage {
		if file, path, ok := strings.Cut(rest, ":"); ok {
			return file, path
		}
	}
	return prefix, rest
}
