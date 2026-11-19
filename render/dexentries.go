package render

import "gopkg.in/yaml.v3"

// EachByID calls fn with the id and the body of every entry of a dex-app
// values node holding clients or connectors: a map keyed by id, or a list of
// mappings carrying an id. An entry without an id, and any other node, is
// skipped.
func EachByID(n *yaml.Node, fn func(id string, body *yaml.Node) error) error {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if err := fn(n.Content[i].Value, n.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			var entry struct {
				ID string `yaml:"id"`
			}
			if item.Kind != yaml.MappingNode || item.Decode(&entry) != nil || entry.ID == "" {
				continue
			}
			if err := fn(entry.ID, item); err != nil {
				return err
			}
		}
	}
	return nil
}
