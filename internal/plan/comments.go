package plan

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// rehomeFootComments moves every foot comment the YAML decoder attached to a
// node deeper than it was written — a comment after a nested block, indented
// as a key of an outer mapping (the notes at the end of muster's
// oauth.server section, after its trustedIssuers) — onto the enclosing key
// it was written at, found by the comment's indentation in data. The encoder
// writes a foot comment at its node's indentation, so without it a file
// written back from its own nodes moves such a comment inward on every
// reconcile.
func rehomeFootComments(doc *yaml.Node, data []byte) {
	lines := strings.Split(string(data), "\n")
	var visit func(n *yaml.Node, keys []*yaml.Node)
	visit = func(n *yaml.Node, keys []*yaml.Node) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				visit(c, keys)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k := n.Content[i]
				chain := append(keys[:len(keys):len(keys)], k)
				visit(n.Content[i+1], chain)
				rehome(k, chain, lines)
			}
		}
	}
	visit(doc, nil)
}

// rehome moves the foot comment of the key k onto the key of chain — the
// keys enclosing k, k last — whose column the comment was written at; a
// comment at k's own column, or at none of theirs, stays.
func rehome(k *yaml.Node, chain []*yaml.Node, lines []string) {
	if k.FootComment == "" {
		return
	}
	indent, ok := commentIndent(k.FootComment, k.Line, lines)
	if !ok || indent == k.Column-1 {
		return
	}
	for i := len(chain) - 2; i >= 0; i-- {
		if chain[i].Column-1 == indent {
			if chain[i].FootComment != "" {
				k.FootComment += "\n" + chain[i].FootComment
			}
			chain[i].FootComment, k.FootComment = k.FootComment, ""
			return
		}
	}
}

// commentIndent is the indentation of the first line of comment in lines,
// looked for from line (1-based) on.
func commentIndent(comment string, line int, lines []string) (int, bool) {
	first := strings.TrimSpace(strings.SplitN(strings.TrimLeft(comment, "\n"), "\n", 2)[0])
	for i := max(line-1, 0); i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == first {
			return len(lines[i]) - len(strings.TrimLeft(lines[i], " ")), true
		}
	}
	return 0, false
}
