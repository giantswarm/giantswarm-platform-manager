package verify

import (
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
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
// the whole file. names are the key's placeholders in the order their
// groups appear, the file's first, then each segment's: what a match
// captures, and what the reason's own <x> references are filled with — the
// concrete server, client or target. A placeholder the key declares twice
// stands for one value.
type plannedKey struct {
	kind, reason string
	file         *regexp.Regexp
	path         []*regexp.Regexp
	names        []string
}

// factPlaceholders are the placeholders that stand for a fact of the
// installation rather than for any name, never a wildcard: <domain> is its
// own base domain. A key with one names the installation's own form of an
// entry — its MCP server on its own host — and nothing else; without the
// fact on record the key names nothing.
var factPlaceholders = map[string]bool{"domain": true}

// facts are the installation's values of the fact placeholders.
type facts map[string]string

// ownFacts are inst's facts for the keys.
func ownFacts(inst installations.Installation) facts {
	return facts{"domain": inst.BaseDomain}
}

// readRemovals parses a capability's removals under the installation's
// facts; a key whose prefix names no file kind the comparison observes, or
// whose fact the installation lacks, is left out.
func readRemovals(rs []definitions.Removal, f facts) plannedKeys {
	var out plannedKeys
	for _, r := range rs {
		if k, ok := parseKey(r.Key, r.Reason, f); ok {
			out = append(out, k)
		}
	}
	return out
}

// readMigrations parses a capability's migrations the way readRemovals does.
func readMigrations(ms []definitions.Migration, f facts) plannedKeys {
	var out plannedKeys
	for _, m := range ms {
		if k, ok := parseKey(m.Key, m.Reason, f); ok {
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

func parseKey(key, reason string, f facts) (plannedKey, bool) {
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
	var ok bool
	if k.file, ok = k.wildcard(file, `[^/]+`, f); !ok {
		return plannedKey{}, false
	}
	for _, s := range segments(yamlPath) {
		re, ok := k.segmentPattern(s, f)
		if !ok {
			return plannedKey{}, false
		}
		k.path = append(k.path, re)
	}
	return k, true
}

// wildcard compiles a pattern whose <x> placeholders each stand for any
// (or, for a file, one segment of a) name — or, for a fact's placeholder,
// for the fact itself — each captured under its name; the rest is literal.
// A fact the installation lacks makes the key name nothing: false.
func (k *plannedKey) wildcard(pattern, part string, f facts) (*regexp.Regexp, bool) {
	var b strings.Builder
	b.WriteString("^")
	for pattern != "" {
		start := strings.IndexByte(pattern, '<')
		end := strings.IndexByte(pattern, '>')
		if start < 0 || end < start {
			b.WriteString(regexp.QuoteMeta(pattern))
			break
		}
		name := pattern[start+1 : end]
		group := part
		if factPlaceholders[name] {
			if f[name] == "" {
				return nil, false
			}
			group = regexp.QuoteMeta(f[name])
		}
		b.WriteString(regexp.QuoteMeta(pattern[:start]))
		b.WriteString("(" + group + ")")
		k.names = append(k.names, name)
		pattern = pattern[end+1:]
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()), true
}

// segmentPattern is what one segment of a key matches: [*] any list index,
// <x> any map key (never an index), a name with <x> in it any identity or
// key with that part, a fact's placeholder the fact, anything else itself.
func (k *plannedKey) segmentPattern(s string, f facts) (*regexp.Regexp, bool) {
	switch {
	case s == "[*]":
		return regexp.MustCompile(`^\[.*\]$`), true
	case strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") && !factPlaceholders[s[1:len(s)-1]]:
		k.names = append(k.names, s[1:len(s)-1])
		return regexp.MustCompile(`^([^\[].*)$`), true
	}
	return k.wildcard(s, `.+`, f)
}

// reason is the reason of the first key that names the difference at
// yamlPath in the file fd, its placeholders filled; empty when none does.
func (ks plannedKeys) reason(fd *fileDiff, yamlPath string) string {
	return ks.find(fd, yamlPath, false)
}

// entryReason is the reason of the first key that names the entry at
// yamlPath (path[entry] of a scalar the plan merges as a set) itself: a key
// of the scalar names its absence from the record, not every entry of it.
func (ks plannedKeys) entryReason(fd *fileDiff, yamlPath string) string {
	return ks.find(fd, yamlPath, true)
}

func (ks plannedKeys) find(fd *fileDiff, yamlPath string, exact bool) string {
	got := segments(yamlPath)
	for _, k := range ks {
		if values, ok := k.matches(fd, got, exact); ok {
			return k.fill(values)
		}
	}
	return ""
}

// joined is the reason a scalar the plan merges as a comma-separated set
// (plan.JoinedList) differs by planned additions alone: every entry the
// render carries that the record does not is named, as an entry of the
// scalar, by a migration key, and the record carries none the render
// lacks; the reasons joined, each once. An added entry no key names, or a
// removed one, leaves the difference as it is.
func joined(fd *fileDiff, d *Difference, migs plannedKeys) string {
	rendered, current := plan.SplitJoined(d.Rendered), plan.SplitJoined(d.Current)
	for _, id := range current {
		if !slices.Contains(rendered, id) {
			return ""
		}
	}
	var reasons []string
	for _, id := range rendered {
		if slices.Contains(current, id) {
			continue
		}
		reason := migs.entryReason(fd, entryPath(d.Path, id))
		if reason == "" {
			return ""
		}
		if !slices.Contains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}
	return strings.Join(reasons, "; ")
}

// entryPath addresses an entry of a scalar the plan merges as a set the way
// a list's entry is addressed: path[entry].
func entryPath(yamlPath, entry string) string {
	return yamlPath + "[" + entry + "]"
}

func (k plannedKey) covers(fd *fileDiff, yamlPath string) bool {
	_, ok := k.matches(fd, segments(yamlPath), false)
	return ok
}

// matches says whether the key names the path got in fd — the key's
// segments each match the path's leading ones and, exact, the path has no
// more — and, when it does, what its placeholders stand for there.
func (k plannedKey) matches(fd *fileDiff, got []string, exact bool) (map[string]string, bool) {
	if k.kind != fd.kind {
		return nil, false
	}
	captured, ok := k.coversFile(fd.path)
	if !ok || len(got) < len(k.path) || exact && len(got) != len(k.path) {
		return nil, false
	}
	for i, want := range k.path {
		m := want.FindStringSubmatch(got[i])
		if m == nil {
			return nil, false
		}
		captured = append(captured, m[1:]...)
	}
	return k.bind(captured)
}

// bind names what the key's groups captured, in their order; a placeholder
// the key declares twice must have captured one value, else the key does
// not name the path.
func (k plannedKey) bind(captured []string) (map[string]string, bool) {
	values := make(map[string]string, len(k.names))
	for i, name := range k.names {
		if v, seen := values[name]; seen && v != captured[i] {
			return nil, false
		}
		values[name] = captured[i]
	}
	return values, true
}

// fill is the key's reason with each placeholder it references replaced by
// what the match captured.
func (k plannedKey) fill(values map[string]string) string {
	reason := k.reason
	for name, value := range values {
		reason = strings.ReplaceAll(reason, "<"+name+">", value)
	}
	return reason
}

// coversFile says whether the key names the rendered file at p — by base
// name, or for extras and backstage by its path under extras/ (under
// extras/backstage/), a directory covering everything beneath it — with
// what the file's placeholders captured.
func (k plannedKey) coversFile(p string) ([]string, bool) {
	switch k.kind {
	case definitions.KindExtras, definitions.KindBackstage:
		rel := extrasPath(p)
		if k.kind == definitions.KindBackstage {
			rel = strings.TrimPrefix(rel, "backstage/")
		}
		for rel != "" {
			if m := k.file.FindStringSubmatch(rel); m != nil {
				return m[1:], true
			}
			i := strings.LastIndexByte(rel, '/')
			if i < 0 {
				return nil, false
			}
			rel = rel[:i]
		}
		return nil, false
	}
	m := k.file.FindStringSubmatch(path.Base(p))
	if m == nil {
		return nil, false
	}
	return m[1:], true
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
