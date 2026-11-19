package dexsplit

import (
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"
)

// listAsMap answers the list of mappings at path as a mapping keyed by each
// entry's id, the id field left out, entries in list order: the form dex-app
// merges entry by entry across values sources. An entry without a scalar id,
// with idEnv (the map's key names the client) or with an id another entry
// has is refused, naming its key path.
func listAsMap(list *yaml.Node, path string) (*yaml.Node, error) {
	out := &yaml.Node{Kind: yaml.MappingNode}
	seen := map[string]string{}
	for i, item := range list.Content {
		at := path + "." + strconv.Itoa(i)
		if item.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("%s: no mapping", at)
		}
		if child(item, keyIDEnv) != nil {
			return nil, fmt.Errorf("%s: %s cannot be kept in a map keyed by id; move this entry by hand", at, keyIDEnv)
		}
		id := child(item, keyID)
		if id == nil || id.Kind != yaml.ScalarNode || id.Value == "" {
			return nil, fmt.Errorf("%s.%s: empty", at, keyID)
		}
		if other, ok := seen[id.Value]; ok {
			return nil, fmt.Errorf("%s and %s have the same id; settle it by hand", other, at)
		}
		seen[id.Value] = at
		set(out, id.Value, withoutKey(item, keyID))
	}
	return out, nil
}

// withoutKey is a copy of the mapping m without key.
func withoutKey(m *yaml.Node, key string) *yaml.Node {
	out := *m
	out.Content = nil
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			out.Content = append(out.Content, m.Content[i], m.Content[i+1])
		}
	}
	return &out
}
