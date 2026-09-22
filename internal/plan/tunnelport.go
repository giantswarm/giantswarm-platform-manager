package plan

import (
	"errors"
	"fmt"
	"reflect"

	"gopkg.in/yaml.v3"
)

// The tunnelport values file of giantswarm/teleport-fleet is the one file the
// agent-platform definition edits entries into rather than writes: the fleet's
// template renders every tunnel's Teleport objects from .Values.tunnelport,
// whose consumers, trust-bundle tokens and tunnels belong to several hubs. The
// definition renders a hub's entries as a values document of their own; the
// plan edits them into the file on record — a consumer or a trust-bundle token
// replaces the one of its name or is appended; a tunnel is shared by every hub
// that brokers into its target, each hub with a token of its own, so the tunnel
// of its name takes the hub's labels and the hub's token and keeps the other
// hubs' tokens — and everything else stays, comments included. The file is
// never created here: without the template on record its values mean nothing.

const (
	// tunnelportValuesFile is the values file of teleport-fleet's production chart.
	tunnelportValuesFile = "kubernetes/envs/prod/values.yaml"
	keyTunnelport        = "tunnelport"
	keyConsumers         = "consumers"
	keyTrustBundle       = "trustBundle"
	keyTokens            = "tokens"
	keyTunnels           = "tunnels"
	keyName              = "name"
	// The lists of the values file the plan names in Kept.
	listConsumers         = keyTunnelport + "." + keyConsumers
	listTrustBundleTokens = keyTunnelport + "." + keyTrustBundle + "." + keyTokens
	listTunnels           = keyTunnelport + "." + keyTunnels
)

// listTunnelTokens names a shared tunnel's tokens list in Kept:
// tunnelport.tunnels[<tunnel>].tokens, where the other hubs' tokens stay.
func listTunnelTokens(tunnel string) string { return listTunnels + "[" + tunnel + "]." + keyTokens }

// errNoTunnelport: a values file without tunnelport values has no template
// reading them; the definition's entries have nowhere to land.
var errNoTunnelport = errors.New("no tunnelport values: the tunnelport template is not on record")

// keepTunnelportValues answers current with rendered's tunnelport entries
// edited in — the consumers by key and the trust-bundle tokens by name, each
// replacing the entry of its name (the definition's entry is the definition's
// whole) or appended; the tunnels by name, a tunnel on record merged (tunnel)
// since the other hubs' tokens live in it — and names every other entry of
// those three lists, and every other hub's token of a merged tunnel, as kept.
// Nothing to edit leaves current byte for byte.
func keepTunnelportValues(rendered, current []byte) ([]byte, []Kept, error) {
	doc, cur, err := mapping(current)
	if err != nil {
		return nil, nil, err
	}
	_, ren, err := mapping(rendered)
	if err != nil {
		return nil, nil, err
	}
	curTP, renTP := entry(cur, keyTunnelport), entry(ren, keyTunnelport)
	if curTP == nil || curTP.Kind != yaml.MappingNode {
		return nil, nil, errNoTunnelport
	}
	if renTP == nil || renTP.Kind != yaml.MappingNode {
		return nil, nil, errors.New("the rendered file carries no tunnelport mapping")
	}
	var m merge
	consumers, err := nodeUnder(curTP, keyConsumers, yaml.MappingNode)
	if err != nil {
		return nil, nil, err
	}
	m.mapping(consumers, entry(renTP, keyConsumers), listConsumers)
	trustBundle, err := nodeUnder(curTP, keyTrustBundle, yaml.MappingNode)
	if err != nil {
		return nil, nil, err
	}
	tokens, err := nodeUnder(trustBundle, keyTokens, yaml.SequenceNode)
	if err != nil {
		return nil, nil, err
	}
	if err := m.sequence(tokens, entry(entry(renTP, keyTrustBundle), keyTokens), listTrustBundleTokens, m.whole); err != nil {
		return nil, nil, err
	}
	tunnels, err := nodeUnder(curTP, keyTunnels, yaml.SequenceNode)
	if err != nil {
		return nil, nil, err
	}
	if err := m.sequence(tunnels, entry(renTP, keyTunnels), listTunnels, m.tunnel); err != nil {
		return nil, nil, err
	}
	if !m.changed {
		return current, m.kept, nil
	}
	out, err := encode(doc)
	return out, m.kept, err
}

// merge edits one hub's entries into the lists of the values file and records
// whether anything changed and which entries of other owners stay.
type merge struct {
	changed bool
	kept    []Kept
}

// mapping edits ren's keys into the mapping cur, named list in Kept: a key cur
// carries with other content is replaced, a new key appended, a key of cur that
// ren lacks kept.
func (m *merge) mapping(cur, ren *yaml.Node, list string) {
	if ren != nil {
		for i := 0; i+1 < len(ren.Content); i += 2 {
			key, value := ren.Content[i], ren.Content[i+1]
			if c := entry(cur, key.Value); c != nil {
				m.replace(c, value)
				continue
			}
			cur.Content = append(cur.Content, key, value)
			cur.Style = 0
			m.changed = true
		}
	}
	for i := 0; i+1 < len(cur.Content); i += 2 {
		if entry(ren, cur.Content[i].Value) == nil {
			m.kept = append(m.kept, Kept{List: list, Entry: cur.Content[i].Value})
		}
	}
}

// sequence edits ren's items into the sequence cur by name, named list in Kept:
// an item cur carries under that name is handed to same with its rendering
// (whole or tunnel), a new name appended, a name of cur that ren lacks kept.
// An item without a name has no place in the file.
func (m *merge) sequence(cur, ren *yaml.Node, list string, same func(cur, ren *yaml.Node, name string) error) error {
	byName := map[string]*yaml.Node{}
	for _, item := range cur.Content {
		n := entry(item, keyName)
		if n == nil {
			return fmt.Errorf("%s: an entry without a name", list)
		}
		byName[n.Value] = item
	}
	rendered := map[string]bool{}
	if ren != nil {
		for _, item := range ren.Content {
			n := entry(item, keyName)
			if n == nil {
				return fmt.Errorf("%s: a rendered entry without a name", list)
			}
			rendered[n.Value] = true
			if c, ok := byName[n.Value]; ok {
				if err := same(c, item, n.Value); err != nil {
					return err
				}
				continue
			}
			cur.Content = append(cur.Content, item)
			cur.Style = 0
			m.changed = true
		}
	}
	for _, item := range cur.Content {
		if name := entry(item, keyName).Value; !rendered[name] {
			m.kept = append(m.kept, Kept{List: list, Entry: name})
		}
	}
	return nil
}

// whole is sequence's same for an entry that is the definition's whole: the
// rendering replaces it where the two differ.
func (m *merge) whole(cur, ren *yaml.Node, _ string) error {
	m.replace(cur, ren)
	return nil
}

// tunnel is sequence's same for a tunnel, which every hub that brokers into
// its target shares: the hub's rendering carries the tunnel's labels and the
// hub's one token, the tunnel on record the other hubs' tokens too. The tokens
// are edited by name — the hub's replaces the one of its name or is appended,
// every other stays and is kept under tunnelport.tunnels[<name>].tokens — and
// every other key is the definition's whole: the rendering's replace them
// where they differ, the merged tokens in their place.
func (m *merge) tunnel(cur, ren *yaml.Node, name string) error {
	tokens, err := nodeUnder(cur, keyTokens, yaml.SequenceNode)
	if err != nil {
		return fmt.Errorf("%s[%s]: %w", listTunnels, name, err)
	}
	if err := m.sequence(tokens, entry(ren, keyTokens), listTunnelTokens(name), m.whole); err != nil {
		return err
	}
	if equal(without(cur, keyTokens), without(ren, keyTokens)) {
		return nil
	}
	merged := without(ren, keyTokens)
	merged.Content = append(merged.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: keyTokens}, tokens)
	*cur = *merged
	m.changed = true
	return nil
}

// replace puts rendered in cur's place when the two differ in content; the
// current entry's comments go with it, the entry is the definition's whole.
func (m *merge) replace(cur, rendered *yaml.Node) {
	if equal(cur, rendered) {
		return
	}
	*cur = *rendered
	m.changed = true
}

// without is a shallow copy of the mapping n without its key entry.
func without(n *yaml.Node, key string) *yaml.Node {
	out := *n
	out.Content = nil
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != key {
			out.Content = append(out.Content, n.Content[i], n.Content[i+1])
		}
	}
	return &out
}

// equal says whether two nodes decode to the same value: comments, styles and
// order of nothing but mapping keys aside.
func equal(a, b *yaml.Node) bool {
	var av, bv any
	if a.Decode(&av) != nil || b.Decode(&bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// nodeUnder is the node of the given kind under key in the mapping m, appended
// empty when m lacks the key or carries null there; another kind there is an
// error.
func nodeUnder(m *yaml.Node, key string, kind yaml.Kind) (*yaml.Node, error) {
	tag := "!!map"
	if kind == yaml.SequenceNode {
		tag = "!!seq"
	}
	n := entry(m, key)
	switch {
	case n == nil:
		n = &yaml.Node{Kind: kind, Tag: tag}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: key}, n)
		m.Style = 0
	case n.Kind == yaml.ScalarNode && n.Tag == "!!null":
		*n = yaml.Node{Kind: kind, Tag: tag}
	case n.Kind != kind:
		return nil, fmt.Errorf("%s is not a %s", key, map[yaml.Kind]string{yaml.MappingNode: "mapping", yaml.SequenceNode: "list"}[kind])
	}
	return n, nil
}
