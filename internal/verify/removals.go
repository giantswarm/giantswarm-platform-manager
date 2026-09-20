package verify

import (
	"path"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// removals are a capability's removals as the comparison reads them: the
// keys the fleet still carries that the definition does not render, each a
// planned change (a migration, a key the template owns) rather than drift.
type removals []removal

// removal is one key of removals.yaml, parsed: the dimension kind its prefix
// names (the way a file is observed, kindOf), the file it names within the
// kind — a base name, or a path under extras/ that covers everything beneath
// it; empty for every file of the kind — and the YAML path's segments, where
// [*] stands for any list index and <x> for any map key. An empty path
// covers the whole file.
type removal struct {
	kind, file, reason string
	path               []string
}

// readRemovals parses a capability's removals; a key whose prefix names no
// file kind the comparison observes is left out.
func readRemovals(rs []definitions.Removal) removals {
	var out removals
	for _, r := range rs {
		if rm, ok := parseRemoval(r); ok {
			out = append(out, rm)
		}
	}
	return out
}

// The file names a removal's prefix resolves to.
const (
	configMapPatch = "configmap-values.yaml.patch"
	secretPatch    = "secret-values.yaml.patch"
)

func parseRemoval(r definitions.Removal) (removal, bool) {
	prefix, rest, _ := strings.Cut(r.Key, ":")
	rm := removal{reason: r.Reason}
	switch prefix {
	case definitions.KindConfigMap:
		rm.kind, rm.file, rm.path = definitions.KindConfigMap, configMapPatch, segments(rest)
	case definitions.KindDexConfigMap:
		rm.kind, rm.file, rm.path = definitions.KindDexSecret, configMapPatch, segments(rest)
	case definitions.KindDexSecret:
		rm.kind, rm.file, rm.path = definitions.KindDexSecret, secretPatch, segments(rest)
	case definitions.KindExtras:
		// extras:<path under extras/> [<YAML path>]
		file, yamlPath, _ := strings.Cut(rest, " ")
		rm.kind, rm.file, rm.path = definitions.KindExtras, strings.TrimSuffix(file, "/"), segments(yamlPath)
	case definitions.KindBackstage:
		// backstage:file:<name> covers the file; backstage:<fileset>:<YAML path>
		// names a path in <fileset>.yaml.
		fileset, yamlPath, _ := strings.Cut(rest, ":")
		rm.kind = definitions.KindBackstage
		if fileset == "file" {
			rm.file = yamlPath
		} else {
			rm.file, rm.path = fileset+".yaml", segments(yamlPath)
		}
	default:
		return removal{}, false
	}
	return rm, true
}

// reason is the reason of the first removal that names the difference at
// yamlPath in the file fd; empty when none does.
func (rs removals) reason(fd *fileDiff, yamlPath string) string {
	for _, r := range rs {
		if r.covers(fd, yamlPath) {
			return r.reason
		}
	}
	return ""
}

func (r removal) covers(fd *fileDiff, yamlPath string) bool {
	if r.kind != fd.kind || !r.coversFile(fd.path) {
		return false
	}
	got := segments(yamlPath)
	if len(got) < len(r.path) {
		return false
	}
	for i, want := range r.path {
		switch {
		case want == "[*]":
			if !strings.HasPrefix(got[i], "[") {
				return false
			}
		case strings.HasPrefix(want, "<") && strings.HasSuffix(want, ">"):
			if strings.HasPrefix(got[i], "[") {
				return false
			}
		case want != got[i]:
			return false
		}
	}
	return true
}

// coversFile says whether the removal names the rendered file at p: by
// base name, or for extras and backstage by its path under extras/ (under
// extras/backstage/), a directory covering everything beneath it.
func (r removal) coversFile(p string) bool {
	switch r.kind {
	case definitions.KindExtras, definitions.KindBackstage:
		rel := extrasPath(p)
		if r.kind == definitions.KindBackstage {
			rel = strings.TrimPrefix(rel, "backstage/")
		}
		return rel == r.file || strings.HasPrefix(rel, r.file+"/")
	}
	return path.Base(p) == r.file
}

// extrasPath is a file's path under its extras directory.
func extrasPath(p string) string {
	if i := strings.Index(p, "/extras/"); i >= 0 {
		return p[i+len("/extras/"):]
	}
	return p
}

// segments splits a flattened YAML path into its keys and indexes: a.b[k].c
// is a, b, [k], c. A bracketed index is one segment whatever it holds (an
// identity may carry dots).
func segments(p string) []string {
	var out []string
	for p != "" {
		switch p[0] {
		case '.':
			p = p[1:]
		case '[':
			end := strings.IndexByte(p, ']')
			if end < 0 {
				return append(out, p)
			}
			out, p = append(out, p[:end+1]), p[end+1:]
		default:
			end := strings.IndexAny(p, ".[")
			if end < 0 {
				return append(out, p)
			}
			out, p = append(out, p[:end]), p[end:]
		}
	}
	return out
}
