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

// listEntry answers current with entry listed under the top-level sequence
// list of a kustomization.yaml (resources or components), and whether that
// changed anything: an entry already listed leaves the file as it is. The
// edit is made on the YAML nodes, so every comment and the order of
// everything else stay; a missing list is appended, an empty one filled.
func listEntry(current []byte, list, entry string) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(current, &doc); err != nil {
		return nil, false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, errNoMapping
	}
	m := doc.Content[0]
	var seq *yaml.Node
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == list {
			seq = m.Content[i+1]
			break
		}
	}
	switch {
	case seq == nil:
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: list}, seq)
	case seq.Kind == yaml.ScalarNode && seq.Tag == "!!null":
		*seq = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	case seq.Kind != yaml.SequenceNode:
		return nil, false, fmt.Errorf("%s is not a list", list)
	}
	for _, item := range seq.Content {
		if item.Value == entry {
			return current, false, nil
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: entry})
	seq.Style = 0
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, err
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), true, nil
}
