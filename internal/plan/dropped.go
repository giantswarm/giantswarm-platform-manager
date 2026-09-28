package plan

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// dropped answers, for a file SOPS encrypted on record that the commit writes
// over, what of the record the commit loses without the manager seeing it:
// SOPS keeps every key in plaintext and encrypts the values one by one, so
// the record names each value it holds without a decryption. A value the
// render carries no leaf for is gone once the commit merges (gone, by YAML
// path). A value the render writes a YAML document of its own in — a
// Secret's values, one encrypted text on record — is written anew whole
// (replaced): no key inside the record's text is readable, so every one the
// render does not carry goes with it. A value the render states itself, a
// literal, a marker the commit fills in, is the render's and neither. SOPS's
// own block takes no part.
func dropped(rendered, current string) (gone, replaced []string) {
	r, err := documents(rendered)
	if err != nil {
		return nil, nil
	}
	c, err := documents(current)
	if err != nil {
		return nil, nil
	}
	for i, doc := range c {
		var over *yaml.Node
		if i < len(r) {
			over = root(r[i])
		}
		path := ""
		if len(c) > 1 {
			path = "[doc" + strconv.Itoa(i) + "]"
		}
		gone, replaced = droppedNodes(over, root(doc), path, true, gone, replaced)
	}
	return gone, replaced
}

// droppedNodes walks the record's node beside the render's (nil where the
// render has none), appending each encrypted value of the record the commit
// loses under the path so far.
func droppedNodes(r, c *yaml.Node, path string, atRoot bool, gone, replaced []string) ([]string, []string) {
	if r != nil && r.Kind == yaml.AliasNode {
		r = r.Alias
	}
	if c.Kind == yaml.AliasNode {
		c = c.Alias
	}
	switch c.Kind {
	case yaml.ScalarNode:
		switch {
		case !strings.HasPrefix(c.Value, encPrefix):
		case r == nil:
			gone = append(gone, path)
		case r.Kind != yaml.ScalarNode || document(r.Value):
			replaced = append(replaced, path)
		}
	case yaml.MappingNode:
		var over map[string]*yaml.Node
		if r != nil && r.Kind == yaml.MappingNode {
			over = entries(r)
		}
		for i := 0; i+1 < len(c.Content); i += 2 {
			key := c.Content[i].Value
			if atRoot && key == SOPSKey {
				continue
			}
			gone, replaced = droppedNodes(over[key], c.Content[i+1], join(path, key), false, gone, replaced)
		}
	case yaml.SequenceNode:
		for i, entry := range c.Content {
			var over *yaml.Node
			if r != nil && r.Kind == yaml.SequenceNode && i < len(r.Content) {
				over = r.Content[i]
			}
			gone, replaced = droppedNodes(over, entry, path+"["+strconv.Itoa(i)+"]", false, gone, replaced)
		}
	}
	return gone, replaced
}

// document reports whether a rendered scalar is YAML text of keys: a
// mapping, what a Secret's values carry.
func document(text string) bool {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return false
	}
	return root(&doc).Kind == yaml.MappingNode
}
