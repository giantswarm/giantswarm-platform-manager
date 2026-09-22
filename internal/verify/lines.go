package verify

// The node walk behind the comparison's leaves: every leaf of a YAML file by
// its flattened path with the line it sits on — a string that holds a YAML
// mapping (a ConfigMap's chart values, the portal's app-config inside them)
// being the mapping's leaves — and the text of an encrypted file redacted
// line by line, so a difference is placed in the file as the result shows it.

import (
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// document is one YAML document of a file: its node tree and its value.
type document struct {
	node  *yaml.Node
	value any
}

// parse reads every document of content, as nodes and as values; the error
// says content is not YAML.
func parse(content string) ([]document, error) {
	dec := yaml.NewDecoder(strings.NewReader(content))
	var out []document
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		var v any
		if err := n.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, document{node: &n, value: v})
	}
}

// textSep opens, in a leaf's path, the YAML document a string field holds:
// the field, then ":" and the path inside it — data.values:route.enabled,
// and data.values:backstage.appConfig:app.title for text inside text — the
// way render.Comparison names a live path that crosses a document. A key may
// carry a colon of its own (a portal's extension entry-card:catalog/labels),
// so the path is never read back for the boundary: flat records the
// documents a file holds, and innerPath and holder split by them.
const textSep = ":"

// flat is a file flattened to its leaves: the value and the line of each by
// path, and the documents its strings hold — the fields decoded into leaves
// (payload) — by path.
type flat struct {
	values    map[string]string
	lines     map[string]int
	documents map[string]bool
}

// textMapping is the document a string holds when it is YAML text of a
// mapping with entries spread over lines — chart values, an app-config; a
// one-line "key: value" is a value, a list (a patch) one text.
func textMapping(s string) ([]document, bool) {
	if !strings.Contains(s, "\n") {
		return nil, false
	}
	docs, err := parse(s)
	if err != nil || len(docs) != 1 {
		return nil, false
	}
	m, ok := docs[0].value.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, false
	}
	return docs, true
}

// payload says whether a leaf of the file itself sits in the data of a
// ConfigMap or the stringData of a Secret — the fields that hold a document
// as text: a chart's values, the portal's app-config. Inside such a document
// every string that holds a mapping is one too; a kustomization's patches
// and every other string are one leaf, whatever they hold.
func payload(path string) bool {
	segs := segments(path)
	if len(segs) > 0 && strings.HasPrefix(segs[0], "[") {
		segs = segs[1:] // the document's key in a file of several
	}
	return len(segs) > 0 && (segs[0] == "data" || segs[0] == "stringData")
}

// innerPath is a leaf's path inside the innermost of documents it sits in
// (the longest that opens it), the whole path for a leaf of the file itself.
// The dimension keys, the removals and the migrations name paths inside the
// document.
func innerPath(documents map[string]bool, p string) string {
	if d := holder(documents, p); d != "" {
		return p[len(d)+len(textSep):]
	}
	return p
}

// holder is the field of documents whose text a leaf sits in, the innermost
// where text holds text; empty for a leaf of the file itself.
func holder(documents map[string]bool, p string) string {
	best := ""
	for d := range documents {
		if len(d) > len(best) && strings.HasPrefix(p, d+textSep) {
			best = d
		}
	}
	return best
}

// flattenLines flattens every document of a YAML file to its leaves by
// dotted path the way flattenYAML does, and names the line each leaf sits
// on: a mapping entry's key line (the first of a multi-line scalar), a
// sequence entry's "- " line; 1 for a file that is not YAML. A leaf inside
// the text a string holds sits on its line of the file where the text is a
// literal block scalar (its lines are the file's from the one after the
// indicator), on the field's line where a quoted or folded scalar folds them.
func flattenLines(content string) flat {
	f := flat{values: map[string]string{}, lines: map[string]int{}, documents: map[string]bool{}}
	docs, err := parse(content)
	if err != nil {
		f.values[""], f.lines[""] = content, 1
		return f
	}
	collect(docs, "", false, func(l int) int { return l }, &f)
	return f
}

// collect flattens the leaves of docs under prefix into f, each line placed
// in the file by place (the file's line of a line of the text docs were
// parsed from); a string that holds a YAML mapping, in a payload field or
// inside a document already, is the mapping's leaves under the field's path
// and textSep, and the field is one of f's documents.
func collect(docs []document, prefix string, inDocument bool, place func(int) int, f *flat) {
	walkFile(docs, prefix, func(path string, line int, n *yaml.Node, value any) {
		if s, isText := value.(string); isText && n != nil && n.Kind == yaml.ScalarNode && (inDocument || payload(path)) {
			if inner, ok := textMapping(s); ok {
				at := place(line)
				within := func(int) int { return at }
				if n.Style == yaml.LiteralStyle {
					start := place(n.Line)
					within = func(l int) int { return start + l }
				}
				f.documents[path] = true
				collect(inner, path+textSep, true, within, f)
				return
			}
		}
		flattenIn(value, path, inDocument, f.values, f.documents)
		if n != nil {
			f.lines[path] = place(line)
		}
	})
}

// visitor is called with every leaf of a file: its path, the line of the
// entry that holds it, its node and its value. The node is nil where the
// node tree and the value disagree in shape (a merge key): the value is
// then the whole subtree, without lines.
type visitor func(path string, line int, n *yaml.Node, value any)

// walkFile visits the leaves of every document of a file under prefix, a
// file of several documents keying each by its kind/namespace/name.
func walkFile(docs []document, prefix string, visit visitor) {
	if len(docs) == 1 {
		walkNode(docs[0].node, docs[0].value, prefix, docs[0].node.Line, visit)
		return
	}
	plain := make([]any, len(docs))
	for i, d := range docs {
		plain[i] = d.value
	}
	for i, k := range keys(plain, "doc") {
		walkNode(docs[i].node, docs[i].value, prefix+"["+k+"]", docs[i].node.Line, visit)
	}
}

// walkNode visits the leaves of node n, decoded as value, under prefix: a
// scalar, or an empty mapping or sequence, each with the line of the entry
// that holds it — line for n itself.
func walkNode(n *yaml.Node, value any, prefix string, line int, visit visitor) {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 1 {
			walkNode(n.Content[0], value, prefix, n.Content[0].Line, visit)
			return
		}
		visit(prefix, line, n, nil)
	case yaml.MappingNode:
		m, ok := value.(map[string]any)
		if !ok || len(m) != len(n.Content)/2 {
			visit(prefix, line, nil, value)
			return
		}
		if len(m) == 0 {
			visit(prefix, line, n, value)
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			walkNode(n.Content[i+1], m[k.Value], joinPath(prefix, k.Value), k.Line, visit)
		}
	case yaml.SequenceNode:
		s, ok := value.([]any)
		if !ok || len(s) != len(n.Content) {
			visit(prefix, line, nil, value)
			return
		}
		if len(s) == 0 {
			visit(prefix, line, n, value)
		}
		for i, k := range keys(s, "") {
			walkNode(n.Content[i], s[i], prefix+"["+k+"]", n.Content[i].Line, visit)
		}
	default:
		visit(prefix, line, n, value)
	}
}

// joinPath opens key under prefix in a flattened path: the first key of a
// document a string holds follows its textSep.
func joinPath(prefix, key string) string {
	if prefix == "" || strings.HasSuffix(prefix, textSep) {
		return prefix + key
	}
	return prefix + "." + key
}

// redactLeaves is an encrypted file's text as the result shows it: SOPS's
// block dropped, every scalar leaf whose path secret names replaced by
// Redacted, every other line as it is on record — the lines stay where they
// are, but for the block's. A file that is not YAML is shown as it is.
func redactLeaves(content string, secret func(path string) bool) string {
	docs, err := parse(content)
	if err != nil {
		return content
	}
	lines := strings.Split(content, "\n")
	drop := map[int]bool{} // the 1-based lines dropped
	for _, d := range docs {
		r := d.node
		if r.Kind == yaml.DocumentNode && len(r.Content) == 1 {
			r = r.Content[0]
		}
		if r.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(r.Content); i += 2 {
			if r.Content[i].Value == plan.SOPSKey {
				dropLines(drop, r.Content[i].Line, extent(r.Content[i+1]))
			}
		}
	}
	walkFile(docs, "", func(path string, _ int, n *yaml.Node, _ any) {
		if n == nil || n.Kind != yaml.ScalarNode || !secret(path) {
			return
		}
		redactScalar(lines, drop, n)
	})
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		if !drop[i+1] {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// redactScalar replaces the scalar's text on its line — the value after the
// key or the "- ", an inline comment with it — by Redacted, and drops the
// further lines of a block scalar.
func redactScalar(lines []string, drop map[int]bool, n *yaml.Node) {
	l := n.Line - 1
	if l < 0 || l >= len(lines) {
		return
	}
	head := []rune(lines[l])
	if n.Column-1 < len(head) {
		head = head[:n.Column-1]
	}
	lines[l] = string(head) + Redacted
	dropLines(drop, n.Line+1, extent(n))
}

// dropLines marks the lines from through to dropped.
func dropLines(drop map[int]bool, from, to int) {
	for l := from; l <= to; l++ {
		drop[l] = true
	}
}

// extent is the last line a node's text reaches: a block scalar's last
// content line, a mapping's or sequence's furthest entry, else its own.
func extent(n *yaml.Node) int {
	last := n.Line
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 && n.Value != "" {
			last += strings.Count(strings.TrimSuffix(n.Value, "\n"), "\n") + 1
		}
	case yaml.AliasNode:
	default:
		for _, c := range n.Content {
			last = max(last, extent(c))
		}
	}
	return last
}
