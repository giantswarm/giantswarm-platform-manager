package plan

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Summary is p's change as a teammate reads it in Slack, one line per
// component in the order of the plan's files: a component the plan creates
// whole reads "new"; otherwise the versions that move (old → new), the
// values keys that change (by path, no value), the resources and list
// entries added and removed, the keys of a Secret added and removed, and the
// credentials that rotate (by name, the installation and component dropped).
// A file whose shape cannot be read reads "rewritten". Never a secret value:
// a Secret's values and an encrypted file's leaves are not compared.
func Summary(p Installation) []string {
	type component struct {
		files, created int
		items          []string
	}
	var order []string
	comps := map[string]*component{}
	at := func(name string) *component {
		c, ok := comps[name]
		if !ok {
			c = &component{}
			comps[name] = c
			order = append(order, name)
		}
		return c
	}
	for _, f := range p.Files {
		if f.Change != ChangeCreate && f.Change != ChangeUpdate {
			continue
		}
		c := at(componentOf(f.Path))
		c.files++
		if f.Change == ChangeCreate {
			c.created++
			c.items = append(c.items, "adds "+path.Base(f.Path))
			continue
		}
		c.items = append(c.items, fileChanges(f)...)
	}
	requested, forced := p.Rotations()
	rotating := map[string]bool{}
	for _, n := range append(requested, forced...) {
		rotating[n] = true
	}
	onRequest := map[string]bool{}
	for _, n := range requested {
		onRequest[n] = true
	}
	rotations := map[string][]string{}
	for _, g := range p.GeneratedSecrets {
		if !rotating[g.Name] || len(g.Files) == 0 {
			continue
		}
		comp := componentOf(g.Files[0])
		short := shortSecretName(g.Name, p.Name, comp)
		if onRequest[g.Name] && !strings.HasSuffix(short, "credentials-revision") {
			short += " (on request)"
		}
		rotations[comp] = append(rotations[comp], short)
	}
	var out []string
	for _, name := range order {
		c := comps[name]
		items := joinAdds(uniq(c.items))
		if c.created == c.files && c.files > 0 {
			items = []string{"new"}
		} else if names := rotations[name]; len(names) > 0 {
			items = append(items, "rotates "+strings.Join(visibleRotations(names), ", "))
		}
		if len(items) == 0 {
			items = []string{"rewritten"}
		}
		out = append(out, name+": "+strings.Join(items, "; "))
	}
	for _, comp := range slices.Sorted(maps.Keys(rotations)) {
		if names := rotations[comp]; comps[comp] == nil {
			out = append(out, comp+": rotates "+strings.Join(visibleRotations(names), ", "))
		}
	}
	return out
}

// joinAdds folds the "adds <file>" items of a component into one.
func joinAdds(items []string) []string {
	var files, out []string
	for _, it := range items {
		if f, ok := strings.CutPrefix(it, "adds "); ok && !strings.Contains(f, " ") {
			files = append(files, f)
			continue
		}
		out = append(out, it)
	}
	if len(files) > 0 {
		out = append([]string{"adds " + strings.Join(files, ", ")}, out...)
	}
	return out
}

// componentOf names the component a file belongs to: the directory after
// extras/ or apps/ in the installation's tree, else the file's directory.
func componentOf(p string) string {
	parts := strings.Split(p, "/")
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "extras" || parts[i] == "apps" {
			return parts[i+1]
		}
	}
	if len(parts) > 1 {
		return parts[len(parts)-2]
	}
	return p
}

// shortSecretName drops the installation and the component from a generated
// value's name: jackal-mcp-kubernetes-valkey-password reads valkey-password.
func shortSecretName(name, installation, comp string) string {
	s := strings.TrimPrefix(name, installation+"-")
	if short := strings.TrimPrefix(s, comp+"-"); short != "" {
		s = short
	}
	return s
}

// visibleRotations leaves out the credentials revision, which rolls with any
// rotated value of its component, unless it is all that rotates.
func visibleRotations(names []string) []string {
	var out []string
	for _, n := range names {
		if !strings.HasSuffix(n, "credentials-revision") {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return uniq(names)
	}
	return uniq(out)
}

// maxKeys bounds the values keys one line names; the rest are counted.
const maxKeys = 4

// fileChanges compares one updated file document by document.
func fileChanges(f File) []string {
	current, err1 := yamlDocs(f.Current)
	next, err2 := yamlDocs(f.Content)
	if err1 != nil || err2 != nil {
		return []string{path.Base(f.Path) + " rewritten"}
	}
	secret := strings.HasSuffix(f.Path, ".enc.yaml")
	var items []string
	seen := map[string]bool{}
	for _, d := range next {
		id := docID(d)
		seen[id] = true
		old, ok := findDoc(current, id)
		if !ok {
			items = append(items, "adds "+id)
			continue
		}
		items = append(items, docChanges(id, old, d, secret || kindOf(d) == "Secret")...)
	}
	for _, d := range current {
		if id := docID(d); !seen[id] {
			items = append(items, "removes "+id)
		}
	}
	return items
}

// docChanges is one document's change: a Secret's keys added and removed,
// or the versions, list entries and values keys of anything else.
func docChanges(id string, old, next map[string]any, secret bool) []string {
	if secret {
		return secretKeys(old, next)
	}
	d := differ{doc: id}
	for _, k := range sortedKeys(old, next) {
		if k == "apiVersion" || k == "kind" || k == "metadata" || k == "sops" {
			continue
		}
		d.walk(k, old[k], next[k])
	}
	return d.items()
}

// secretKeys names the keys of a Secret's data and stringData added and
// removed; values are never compared.
func secretKeys(old, next map[string]any) []string {
	var added, removed []string
	for _, field := range []string{"stringData", "data"} {
		o, _ := old[field].(map[string]any)
		n, _ := next[field].(map[string]any)
		for k := range n {
			if _, ok := o[k]; !ok {
				added = append(added, k)
			}
		}
		for k := range o {
			if _, ok := n[k]; !ok {
				removed = append(removed, k)
			}
		}
	}
	var out []string
	if len(added) > 0 {
		slices.Sort(added)
		out = append(out, "adds key "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		slices.Sort(removed)
		out = append(out, "drops key "+strings.Join(removed, ", "))
	}
	return out
}

// differ collects a document's change as it walks both sides.
type differ struct {
	// doc names the document a version is read of (HelmRelease mcp-kubernetes).
	doc                   string
	versions, lists, keys []string
}

// versionKeys are the leaves whose values a teammate reads: they name what runs.
var versionKeys = map[string]bool{"version": true, "tag": true, "semver": true}

func (d *differ) walk(at string, old, next any) {
	// A string holding a YAML document (a ConfigMap's values) is compared as one.
	if o, ok := yamlString(old); ok {
		old = o
	}
	if n, ok := yamlString(next); ok {
		next = n
	}
	switch n := next.(type) {
	case map[string]any:
		o, ok := old.(map[string]any)
		if !ok {
			d.keys = append(d.keys, at)
			return
		}
		for _, k := range sortedKeys(o, n) {
			d.walk(at+"."+k, o[k], n[k])
		}
		return
	case []any:
		o, ok := old.([]any)
		if !ok {
			d.keys = append(d.keys, at)
			return
		}
		d.list(at, o, n)
		return
	}
	if old == nil && next == nil {
		return
	}
	if fmt.Sprint(old) == fmt.Sprint(next) {
		return
	}
	if versionKeys[last(at)] && old != nil && next != nil {
		name := trimPath(at, 2)
		if d.doc != "values" {
			name = d.doc
		}
		d.versions = append(d.versions, fmt.Sprintf("%s %v → %v", name, old, next))
		return
	}
	d.keys = append(d.keys, at)
}

// list compares a list: scalars as a set (entries added and removed), maps
// by their name, id or path when every entry has one, else the list as a key.
func (d *differ) list(at string, old, next []any) {
	if scalars(old) && scalars(next) {
		o, n := strs(old), strs(next)
		var parts []string
		for _, v := range n {
			if !slices.Contains(o, v) {
				parts = append(parts, "+"+shortEntry(v))
			}
		}
		for _, v := range o {
			if !slices.Contains(n, v) {
				parts = append(parts, "−"+shortEntry(v))
			}
		}
		if len(parts) > 0 {
			d.lists = append(d.lists, trimPath(at, 3)+" "+strings.Join(parts, " "))
		}
		return
	}
	oi, okO := byIdentity(old)
	ni, okN := byIdentity(next)
	if !okO || !okN {
		if fmt.Sprint(old) != fmt.Sprint(next) {
			d.keys = append(d.keys, at)
		}
		return
	}
	for _, k := range sortedKeys(oi, ni) {
		d.walk(at+"."+k, oi[k], ni[k])
	}
}

func (d *differ) items() []string {
	out := slices.Clone(d.versions)
	out = append(out, d.lists...)
	keys := uniq(trimAll(d.keys, 4))
	if len(keys) > maxKeys {
		keys = append(keys[:maxKeys], fmt.Sprintf("%d more", len(keys)-maxKeys))
	}
	if len(keys) > 0 {
		out = append(out, "values "+strings.Join(keys, ", "))
	}
	return out
}

// shortEntry shortens a list entry for the line: a remote base reads as its
// path and ref (https://github.com/o/r//extras/x?ref=main reads extras/x?ref=main).
func shortEntry(v string) string {
	if i := strings.Index(v, "//"); i >= 0 && strings.HasPrefix(v, "http") {
		if j := strings.Index(v[i+2:], "//"); j >= 0 {
			return v[i+2+j+2:]
		}
	}
	return strings.TrimSuffix(strings.TrimPrefix(v, "./"), "/")
}

func yamlDocs(s string) ([]map[string]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(s))
	var out []map[string]any
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if d != nil {
			out = append(out, d)
		}
	}
}

func kindOf(d map[string]any) string {
	k, _ := d["kind"].(string)
	return k
}

// docID names a document by kind and name; a values file without either is "values".
func docID(d map[string]any) string {
	meta, _ := d["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	switch kind := kindOf(d); {
	case kind != "" && name != "":
		return kind + " " + name
	case kind != "":
		return kind
	}
	return "values"
}

func findDoc(docs []map[string]any, id string) (map[string]any, bool) {
	for _, d := range docs {
		if docID(d) == id {
			return d, true
		}
	}
	return nil, false
}

func yamlString(v any) (any, bool) {
	s, ok := v.(string)
	if !ok || !strings.Contains(s, "\n") {
		return nil, false
	}
	var out any
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		return nil, false
	}
	switch out.(type) {
	case map[string]any, []any:
		return out, true
	}
	return nil, false
}

// byIdentity keys a list of maps by the first of name, id or path every
// entry carries.
func byIdentity(list []any) (map[string]any, bool) {
	for _, key := range []string{"name", "id", "path"} {
		out := map[string]any{}
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[key].(string)
			if !ok {
				out = nil
				break
			}
			out[v] = m
		}
		if out != nil {
			return out, true
		}
	}
	return nil, false
}

func scalars(list []any) bool {
	for _, e := range list {
		switch e.(type) {
		case map[string]any, []any:
			return false
		}
	}
	return true
}

func strs(list []any) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, fmt.Sprint(e))
	}
	return out
}

func sortedKeys[V any](a, b map[string]V) []string {
	keys := make([]string, 0, len(a)+len(b))
	for k := range a {
		keys = append(keys, k)
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	return keys
}

func last(at string) string {
	return at[strings.LastIndex(at, ".")+1:]
}

// trimPath keeps the last n segments of a dotted path.
func trimPath(at string, n int) string {
	parts := strings.Split(at, ".")
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, ".")
}

// trimAll cuts each path to its first n segments, where a teammate still
// reads which setting moved.
func trimAll(paths []string, n int) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		parts := strings.Split(p, ".")
		if len(parts) > n {
			parts = parts[:n]
		}
		out = append(out, strings.Join(parts, "."))
	}
	return out
}

func uniq(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
