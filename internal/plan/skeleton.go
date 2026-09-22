package plan

import (
	"errors"
	"io"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// SOPSKey is the top-level key SOPS keeps its metadata under in a file it
// encrypted; encPrefix opens every value it encrypted.
const (
	SOPSKey   = "sops"
	encPrefix = "ENC["
)

// markerPrefixes open the scalars the commit fills in: a generated value and
// a supplied one.
var markerPrefixes = []string{strings.TrimSuffix(render.Placeholder(""), ")"), strings.TrimSuffix(render.Supplied(""), ")")}

// sameSkeleton reports whether the file on record is the render's outside
// the values the commit fills in — for a file SOPS encrypted (the values
// under the repository's encrypted_regex are ENC[...] on record; keys,
// metadata and every other field are plaintext) or a plain file the render
// puts a generated marker into (a key pair's public half). The two are
// compared as YAML: every key, every entry of a sequence, every scalar the
// record holds in plaintext against the render's — but a scalar the commit
// fills in and a scalar the record holds encrypted against nothing: the
// manager decrypts nothing, and the value on record stands. SOPS's own block,
// comments and formatting take no part. Any other file is compared by its
// bytes, so this answers false for it.
func sameSkeleton(rendered, current string) bool {
	r, err := documents(rendered)
	if err != nil {
		return false
	}
	c, err := documents(current)
	if err != nil || len(r) != len(c) || len(c) == 0 {
		return false
	}
	if !encrypted(c) && !strings.Contains(rendered, markerPrefixes[0]) {
		return false
	}
	for i := range r {
		if !sameNode(r[i], c[i], true) {
			return false
		}
	}
	return true
}

// Unseen is a literal the render puts under a field the record holds
// encrypted, in a file kept as unchanged: the comparison decrypts nothing,
// so the value on record stands whether or not it is the render's — a client
// id in a credentials Secret. Path is the leaf's YAML path, Value the render's.
type Unseen struct {
	Path  string `json:"path"`
	Value string `json:"value"`
}

// unseen are the literals of the render that the file on record holds
// encrypted, for a file sameSkeleton kept: every scalar the render states
// outright — no marker the commit fills in — against a scalar the record
// holds as ENC[...], by YAML path, in the render's order.
func unseen(rendered, current string) []Unseen {
	r, err := documents(rendered)
	if err != nil {
		return nil
	}
	c, err := documents(current)
	if err != nil || len(r) != len(c) {
		return nil
	}
	var out []Unseen
	for i := range r {
		out = unseenNodes(root(r[i]), root(c[i]), "", out)
	}
	return out
}

// unseenNodes walks the render's node beside the record's, appending each
// literal the record holds encrypted under the path so far.
func unseenNodes(r, c *yaml.Node, path string, out []Unseen) []Unseen {
	if r.Kind == yaml.AliasNode {
		r = r.Alias
	}
	if c.Kind == yaml.AliasNode {
		c = c.Alias
	}
	if r.Kind != c.Kind {
		return out
	}
	switch r.Kind {
	case yaml.ScalarNode:
		if strings.HasPrefix(c.Value, encPrefix) && !filledIn(r.Value) {
			out = append(out, Unseen{Path: path, Value: r.Value})
		}
	case yaml.SequenceNode:
		for i := range r.Content {
			if i < len(c.Content) {
				out = unseenNodes(r.Content[i], c.Content[i], path+"["+strconv.Itoa(i)+"]", out)
			}
		}
	case yaml.MappingNode:
		ck := entries(c)
		for i := 0; i+1 < len(r.Content); i += 2 {
			key := r.Content[i].Value
			if cv := ck[key]; cv != nil {
				out = unseenNodes(r.Content[i+1], cv, join(path, key), out)
			}
		}
	}
	return out
}

// join is the YAML path of key under path.
func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// documents parses every YAML document of text.
func documents(text string) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(text))
	var out []*yaml.Node
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, &doc)
	}
}

// Encrypted reports whether the file on record is one SOPS encrypted: a
// document of it carries SOPS's block.
func Encrypted(current string) bool {
	docs, err := documents(current)
	return err == nil && encrypted(docs)
}

// Ciphertext reports whether text carries a value SOPS encrypted — a file
// on record, or a shared file the plan edits that kept one.
func Ciphertext(text string) bool {
	return strings.Contains(text, encPrefix)
}

// Opaque reports whether a scalar of the render against one of the record
// takes no part in a comparison: the commit fills the render's in, or the
// record holds it encrypted — the manager decrypts nothing, and the value on
// record stands.
func Opaque(rendered, current string) bool {
	return strings.HasPrefix(current, encPrefix) || filledIn(rendered)
}

// encrypted reports whether a document of the file on record carries SOPS's
// block: the file is encrypted.
func encrypted(docs []*yaml.Node) bool {
	for _, doc := range docs {
		if entry(root(doc), SOPSKey) != nil {
			return true
		}
	}
	return false
}

// root is the document's node.
func root(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	return doc
}

// sameNode compares the render's node with the record's. atRoot skips the
// SOPS block the record's root mapping carries and the render's does not.
func sameNode(r, c *yaml.Node, atRoot bool) bool {
	if r.Kind == yaml.AliasNode {
		r = r.Alias
	}
	if c.Kind == yaml.AliasNode {
		c = c.Alias
	}
	if r.Kind == yaml.DocumentNode || c.Kind == yaml.DocumentNode {
		return r.Kind == c.Kind && len(r.Content) == 1 && len(c.Content) == 1 && sameNode(r.Content[0], c.Content[0], atRoot)
	}
	if r.Kind != c.Kind {
		return false
	}
	switch r.Kind {
	case yaml.ScalarNode:
		if Opaque(r.Value, c.Value) {
			return true
		}
		return r.Value == c.Value && r.ShortTag() == c.ShortTag()
	case yaml.SequenceNode:
		if len(r.Content) != len(c.Content) {
			return false
		}
		for i := range r.Content {
			if !sameNode(r.Content[i], c.Content[i], false) {
				return false
			}
		}
		return true
	case yaml.MappingNode:
		rk, ck := entries(r), entries(c)
		if atRoot && rk[SOPSKey] == nil {
			delete(ck, SOPSKey)
		}
		if len(rk) != len(ck) {
			return false
		}
		for key, rv := range rk {
			cv, ok := ck[key]
			if !ok || !sameNode(rv, cv, false) {
				return false
			}
		}
		return true
	}
	return false
}

// entries are a mapping node's values by key.
func entries(m *yaml.Node) map[string]*yaml.Node {
	out := make(map[string]*yaml.Node, len(m.Content)/2)
	for i := 0; i+1 < len(m.Content); i += 2 {
		out[m.Content[i].Value] = m.Content[i+1]
	}
	return out
}

// filledIn reports whether the render's scalar is one the commit fills in.
func filledIn(value string) bool {
	for _, p := range markerPrefixes {
		if strings.Contains(value, p) {
			return true
		}
	}
	return false
}
