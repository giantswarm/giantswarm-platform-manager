package plan

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// samePatches reports whether the file on record is the render's but for
// how its kustomization patch texts (patches[n].patch) spell their YAML: a
// patch text is YAML inside a string, which the render writes in one
// spelling and a record written by hand or by an older manager in another —
// a semver range in double quotes where the render quotes it singly, a JSON
// 6902 patch as a JSON list. Each patch text is compared by its value, the
// rest of the file as the same YAML with the same comments and scalar
// styles; a file without patch texts, or one that does not parse, is not
// such a file, and change compares it by its bytes.
func samePatches(rendered, current string) bool {
	r, ok := canonicalPatches(rendered)
	if !ok {
		return false
	}
	c, ok := canonicalPatches(current)
	return ok && r == c
}

// canonicalPatches is a kustomization encoded again with each of its patch
// texts in the one spelling the text's value encodes to; false for a file
// that is not one kustomization document with a patch text, or a patch text
// that is not YAML.
func canonicalPatches(content string) (string, bool) {
	docs, err := documents(content)
	if err != nil || len(docs) != 1 {
		return "", false
	}
	patches := entry(root(docs[0]), "patches")
	if patches == nil || patches.Kind != yaml.SequenceNode {
		return "", false
	}
	found := false
	for _, p := range patches.Content {
		text := entry(p, "patch")
		if text == nil || text.Kind != yaml.ScalarNode {
			continue
		}
		var v any
		if err := yaml.Unmarshal([]byte(text.Value), &v); err != nil {
			return "", false
		}
		canonical, err := yaml.Marshal(v)
		if err != nil {
			return "", false
		}
		text.Value, text.Tag, text.Style = string(canonical), tagStr, yaml.LiteralStyle
		found = true
	}
	if !found {
		return "", false
	}
	var out strings.Builder
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(docs[0]); err != nil {
		return "", false
	}
	if err := enc.Close(); err != nil {
		return "", false
	}
	return out.String(), true
}
