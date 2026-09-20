package verify

import (
	"path"
	"regexp"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// plannedKeys are the keys of a capability's removals or migrations as the
// comparison reads them, in file order: the first key that names a
// difference gives its reason, so a more specific key precedes one that
// covers it. A removal is a key the fleet still carries that the definition
// does not render; a migration a key the definition renders that the record
// lacks. Either is a planned change, not drift.
type plannedKeys []plannedKey

// plannedKey is one key, parsed: the dimension kind its prefix names (the
// way a file is observed, kindOf), the file it names within the kind — a
// base name, or a path under extras/ that covers everything beneath it;
// empty for every file of the kind — and the YAML path's segments, where
// [*] stands for any list index, <x> for any map key and, within a file
// name or a list entry's identity, for any part of it. An empty path covers
// the whole file.
type plannedKey struct {
	kind, reason string
	file         *regexp.Regexp
	path         []*regexp.Regexp
}

// readRemovals parses a capability's removals; a key whose prefix names no
// file kind the comparison observes is left out.
func readRemovals(rs []definitions.Removal) plannedKeys {
	var out plannedKeys
	for _, r := range rs {
		if k, ok := parseKey(r.Key, r.Reason); ok {
			out = append(out, k)
		}
	}
	return out
}

// readMigrations parses a capability's migrations the way readRemovals does.
func readMigrations(ms []definitions.Migration) plannedKeys {
	var out plannedKeys
	for _, m := range ms {
		if k, ok := parseKey(m.Key, m.Reason); ok {
			out = append(out, k)
		}
	}
	return out
}

// The file names a key's prefix resolves to.
const (
	configMapPatch = "configmap-values.yaml.patch"
	secretPatch    = "secret-values.yaml.patch"
)

func parseKey(key, reason string) (plannedKey, bool) {
	prefix, rest, _ := strings.Cut(key, ":")
	k := plannedKey{reason: reason}
	var file, yamlPath string
	switch prefix {
	case definitions.KindConfigMap:
		k.kind, file, yamlPath = definitions.KindConfigMap, configMapPatch, rest
	case definitions.KindDexConfigMap:
		k.kind, file, yamlPath = definitions.KindDexSecret, configMapPatch, rest
	case definitions.KindDexSecret:
		k.kind, file, yamlPath = definitions.KindDexSecret, secretPatch, rest
	case definitions.KindExtras:
		// extras:<path under extras/> [<YAML path>]
		k.kind = definitions.KindExtras
		file, yamlPath, _ = strings.Cut(rest, " ")
		file = strings.TrimSuffix(file, "/")
	case definitions.KindBackstage:
		// backstage:file:<name> covers the file (or the directory beneath
		// extras/backstage/); backstage:<fileset>:<YAML path> names a path
		// in <fileset>.yaml.
		k.kind = definitions.KindBackstage
		fileset, p, _ := strings.Cut(rest, ":")
		if fileset == "file" {
			file = p
		} else {
			file, yamlPath = fileset+".yaml", p
		}
	default:
		return plannedKey{}, false
	}
	k.file = wildcard(file, `[^/]+`)
	for _, s := range segments(yamlPath) {
		k.path = append(k.path, segmentPattern(s))
	}
	return k, true
}

// wildcard compiles a pattern whose <x> placeholders each stand for any
// (or, for a file, one segment of a) name; the rest is literal.
func wildcard(pattern, part string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for pattern != "" {
		start := strings.IndexByte(pattern, '<')
		end := strings.IndexByte(pattern, '>')
		if start < 0 || end < start {
			b.WriteString(regexp.QuoteMeta(pattern))
			break
		}
		b.WriteString(regexp.QuoteMeta(pattern[:start]))
		b.WriteString(part)
		pattern = pattern[end+1:]
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// segmentPattern is what one segment of a key matches: [*] any list index,
// <x> any map key (never an index), a name with <x> in it any identity or
// key with that part, anything else itself.
func segmentPattern(s string) *regexp.Regexp {
	switch {
	case s == "[*]":
		return regexp.MustCompile(`^\[.*\]$`)
	case strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"):
		return regexp.MustCompile(`^[^\[].*$`)
	}
	return wildcard(s, `.+`)
}

// reason is the reason of the first key that names the difference at
// yamlPath in the file fd; empty when none does.
func (ks plannedKeys) reason(fd *fileDiff, yamlPath string) string {
	for _, k := range ks {
		if k.covers(fd, yamlPath) {
			return k.reason
		}
	}
	return ""
}

func (k plannedKey) covers(fd *fileDiff, yamlPath string) bool {
	if k.kind != fd.kind || !k.coversFile(fd.path) {
		return false
	}
	got := segments(yamlPath)
	if len(got) < len(k.path) {
		return false
	}
	for i, want := range k.path {
		if !want.MatchString(got[i]) {
			return false
		}
	}
	return true
}

// coversFile says whether the key names the rendered file at p: by base
// name, or for extras and backstage by its path under extras/ (under
// extras/backstage/), a directory covering everything beneath it.
func (k plannedKey) coversFile(p string) bool {
	switch k.kind {
	case definitions.KindExtras, definitions.KindBackstage:
		rel := extrasPath(p)
		if k.kind == definitions.KindBackstage {
			rel = strings.TrimPrefix(rel, "backstage/")
		}
		for rel != "" {
			if k.file.MatchString(rel) {
				return true
			}
			i := strings.LastIndexByte(rel, '/')
			if i < 0 {
				return false
			}
			rel = rel[:i]
		}
		return false
	}
	return k.file.MatchString(path.Base(p))
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
