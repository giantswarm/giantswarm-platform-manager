package plan

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// keptPlatformKeys are the key paths of the agent-platform configmap patch
// (platformPatchFile) the definition keeps as they are on record: its
// removals of kind kept — keys whose replacement (a shared default, an object
// under extras) is not on the fleet yet. The plan carries each from the file
// on record into the render where the render lacks it, so a reconcile never
// removes it and the comparison finds it as defined; the removal's reason
// says what has to land before the key goes.
var keptPlatformKeys = mustKeptKeys(installations.AgentPlatform, definitions.KindConfigMap)

// mustKeptKeys reads the definition's kept keys under the prefix; the
// definitions are embedded, so a removals.yaml that does not read fails the
// build's tests, never a call.
func mustKeptKeys(capability, prefix string) []string {
	keys, err := definitions.KeptKeys(capability, prefix)
	if err != nil {
		panic(err)
	}
	return keys
}

// keepSubtrees carries into the mapping ren every key path of paths that the
// mapping cur holds and ren lacks — the node as it is on record, comments
// included, under mappings created on the way — and records each under the
// path above it (List) by its last key (Entry). A path ren carries is the
// definition's: nothing is copied. A path that meets a scalar or a list on
// the way in ren has no place there and is left out.
func keepSubtrees(ren, cur *yaml.Node, paths []string, kept *[]Kept) {
	for _, p := range paths {
		keys := strings.Split(p, ".")
		c := at(cur, keys...)
		if c == nil || at(ren, keys...) != nil {
			continue
		}
		parent, ok := ren, true
		for _, k := range keys[:len(keys)-1] {
			next := entry(parent, k)
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				parent.Content = append(parent.Content, keyNode(k), next)
				parent.Style = 0
			} else if next.Kind != yaml.MappingNode {
				ok = false
				break
			}
			parent = next
		}
		if !ok {
			continue
		}
		// The record's key node goes with its value: a comment above the key
		// hangs on the key, not on the value.
		parent.Content = append(parent.Content, keyOf(at(cur, keys[:len(keys)-1]...), keys[len(keys)-1]), c)
		parent.Style = 0
		*kept = append(*kept, Kept{List: strings.Join(keys[:len(keys)-1], "."), Entry: keys[len(keys)-1]})
	}
}

// keyNode is a mapping key.
func keyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

// keyOf is the key node of key in the mapping m, comments and all; a fresh
// key node where m lacks it.
func keyOf(m *yaml.Node, key string) *yaml.Node {
	if m != nil {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i].Value == key {
				return m.Content[i]
			}
		}
	}
	return keyNode(key)
}
