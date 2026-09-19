package plan

import (
	"bytes"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

// errNoMapping: a kustomization.yaml that is not a mapping document cannot
// take an entry.
var errNoMapping = errors.New("not a YAML mapping")

// The YAML tags of the nodes the plan creates.
const (
	tagStr  = "!!str"
	tagSeq  = "!!seq"
	tagMap  = "!!map"
	tagNull = "!!null"
)

// listEntry answers current with entry listed under the top-level sequence
// list of a kustomization.yaml (resources or components), and whether that
// changed anything: an entry already listed leaves the file as it is. The
// edit is made on the YAML nodes, so every comment and the order of
// everything else stay; a missing list is appended, an empty one filled.
func listEntry(current []byte, list, entry string) ([]byte, bool, error) {
	doc, m, err := mapping(current)
	if err != nil {
		return nil, false, err
	}
	var seq *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == list {
			seq = m.Content[i+1]
			break
		}
	}
	switch {
	case seq == nil:
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: tagSeq}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: list}, seq)
	case seq.Kind == yaml.ScalarNode && seq.Tag == "!!null":
		*seq = yaml.Node{Kind: yaml.SequenceNode, Tag: tagSeq}
	case seq.Kind != yaml.SequenceNode:
		return nil, false, fmt.Errorf("%s is not a list", list)
	}
	for _, item := range seq.Content {
		if item.Value == entry {
			return current, false, nil
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: entry})
	seq.Style = 0
	out, err := encode(doc)
	return out, true, err
}

// mapping parses data as one YAML document whose root is a mapping and
// answers the document and that mapping; anything else is errNoMapping.
func mapping(data []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, errNoMapping
	}
	return &doc, doc.Content[0], nil
}

// encode writes doc the way every file of the plan is written: two-space
// indent, the comments of the nodes kept.
func encode(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// keptLists are the lists of a kustomization.yaml other owners add entries to.
var keptLists = []string{ListResources, ListComponents}

// keep answers rendered with every entry of current's resources and
// components lists that rendered does not list, appended in current's
// order, and names what it kept. The platform writes the file; an entry
// another owner listed in it (an installation's agents, its MCP servers, a
// tunnel) is theirs and stays. Nothing kept leaves rendered as it is.
func keep(rendered, current []byte) ([]byte, []Kept, error) {
	_, m, err := mapping(current)
	if err != nil {
		return nil, nil, err
	}
	var kept []Kept
	for _, list := range keptLists {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value != list || m.Content[i+1].Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range m.Content[i+1].Content {
				if item.Kind != yaml.ScalarNode {
					continue
				}
				edited, changed, err := listEntry(rendered, list, item.Value)
				if err != nil {
					return nil, nil, err
				}
				if changed {
					rendered = edited
					kept = append(kept, Kept{List: list, Entry: item.Value})
				}
			}
		}
	}
	return rendered, kept, nil
}
