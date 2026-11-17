package plan

import (
	"gopkg.in/yaml.v3"
)

// The dex-app configmap patch's shared parts, by the key path the plan names
// them under in Kept.
const (
	keyOIDC               = "oidc"
	keyStaticClients      = "staticClients"
	keyExtraStaticClients = "extraStaticClients"
	keyCustomer           = "customer"
	keyGiantswarm         = "giantswarm"
	keyConnectors         = "connectors"
	keyClientID           = "id"
	listStaticClients     = keyOIDC + "." + keyStaticClients
	listExtraClients      = keyOIDC + "." + keyExtraStaticClients
	mapCustomer           = keyOIDC + "." + keyCustomer
	mapGiantswarm         = keyOIDC + "." + keyGiantswarm
	listConnectors        = mapCustomer + "." + keyConnectors
	listStaffConnectors   = mapGiantswarm + "." + keyConnectors
)

// keepDexPatch answers rendered with every part of current's dex-app
// configmap patch that no definition owns, and names what it kept. The patch
// is one file with several owners: a definition owns the clients and the
// connectors it renders — a built-in client under oidc.staticClients by its
// key, an extra static client by its id, a hub's connector under
// oidc.<owner>.connectors by its id — and nothing else. A top-level key
// rendered lacks (the installation's ingress tuning), a key under oidc it
// lacks, a key under oidc.<owner> it lacks (the installation's login
// connector), a built-in client it does not render, an extra static client
// or a connector whose id it does not declare are another owner's and stay,
// after the definition's, with their comments. The definition renders
// extraStaticClients and connectors as maps keyed by id; another owner's
// entry of a list on record joins the map under its id. A client or
// connector the definition renders is the definition's whole: its entry
// replaces the current one, so nothing stale inside it (an inline secret, an
// old issuer) survives — but for the authenticator's trusted peers: a peer on
// record the definition does not render is the installation's own and stays,
// after the definition's (keepList). Nothing kept leaves rendered as it is.
func keepDexPatch(rendered, current []byte) ([]byte, []Kept, error) {
	_, cur, err := mapping(current)
	if err != nil {
		return nil, nil, err
	}
	doc, ren, err := mapping(rendered)
	if err != nil {
		return nil, nil, err
	}
	var kept []Kept
	keepKeys(ren, cur, "", &kept, func(key string, r, c *yaml.Node) {
		if key != keyOIDC || r.Kind != yaml.MappingNode || c.Kind != yaml.MappingNode {
			return
		}
		keepKeys(r, c, keyOIDC, &kept, func(key string, r, c *yaml.Node) {
			switch {
			case key == keyStaticClients && r.Kind == yaml.MappingNode && c.Kind == yaml.MappingNode:
				keepKeys(r, c, listStaticClients, &kept, func(key string, r, c *yaml.Node) {
					if key == keyAuthenticator {
						keepList(entry(r, keyTrustedPeers), entry(c, keyTrustedPeers), ListTrustedPeers, &kept)
					}
				})
			case key == keyExtraStaticClients:
				keepByID(r, c, listExtraClients, &kept)
			case (key == keyCustomer || key == keyGiantswarm) && r.Kind == yaml.MappingNode && c.Kind == yaml.MappingNode:
				owner := keyOIDC + "." + key
				keepKeys(r, c, owner, &kept, func(field string, r, c *yaml.Node) {
					if field == keyConnectors {
						keepByID(r, c, owner+"."+keyConnectors, &kept)
					}
				})
			}
		})
	})
	if len(kept) == 0 {
		return rendered, nil, nil
	}
	out, err := encode(doc)
	return out, kept, err
}

// keepKeys appends to the mapping ren every key of the mapping cur that ren
// lacks, recording each under list, and hands a key both carry to shared.
func keepKeys(ren, cur *yaml.Node, list string, kept *[]Kept, shared func(key string, r, c *yaml.Node)) {
	for i := 0; i+1 < len(cur.Content); i += 2 {
		key, value := cur.Content[i], cur.Content[i+1]
		r := entry(ren, key.Value)
		switch {
		case r == nil:
			ren.Content = append(ren.Content, key, value)
			*kept = append(*kept, Kept{List: list, Entry: key.Value})
		case shared != nil:
			shared(key.Value, r, value)
		}
	}
}

// keepByID adds to ren every entry of cur whose id ren does not declare (an
// extra static client, a connector), recording each under list. Either side
// is a map keyed by id or a list of entries carrying an id; a list entry
// joins a map under its id, without the id.
func keepByID(ren, cur *yaml.Node, list string, kept *[]Kept) {
	declared := map[string]bool{}
	for _, e := range byID(ren) {
		declared[e.id] = true
	}
	for _, e := range byID(cur) {
		if e.id == "" || declared[e.id] {
			continue
		}
		switch ren.Kind {
		case yaml.MappingNode:
			key := e.key
			if key == nil {
				key = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.id, HeadComment: e.body.HeadComment}
			}
			ren.Content = append(ren.Content, key, withoutID(e.body))
		case yaml.SequenceNode:
			ren.Content = append(ren.Content, withID(e.id, e.body))
		default:
			continue
		}
		declared[e.id] = true
		*kept = append(*kept, Kept{List: list, Entry: e.id})
	}
}

// idEntry is an entry of a map or list of clients or connectors: its id, its
// key node in a map, and its mapping.
type idEntry struct {
	id   string
	key  *yaml.Node
	body *yaml.Node
}

// byID are the entries of n, a map keyed by id or a list of mappings with an
// id, in order; nil for any other node.
func byID(n *yaml.Node) []idEntry {
	var out []idEntry
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, idEntry{id: n.Content[i].Value, key: n.Content[i], body: n.Content[i+1]})
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			if id := entry(item, keyClientID); id != nil {
				out = append(out, idEntry{id: id.Value, body: item})
			}
		}
	}
	return out
}

// withoutID is body without its id key, body itself when it has none.
func withoutID(body *yaml.Node) *yaml.Node {
	if entry(body, keyClientID) == nil {
		return body
	}
	out := *body
	out.Content = nil
	for i := 0; i+1 < len(body.Content); i += 2 {
		if body.Content[i].Value != keyClientID {
			out.Content = append(out.Content, body.Content[i], body.Content[i+1])
		}
	}
	return &out
}

// withID is body as a list entry: id first, then body's keys but its own id.
func withID(id string, body *yaml.Node) *yaml.Node {
	if body.Kind != yaml.MappingNode {
		return body
	}
	out := *withoutID(body)
	out.Content = append([]*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: keyClientID}, {Kind: yaml.ScalarNode, Tag: "!!str", Value: id}}, out.Content...)
	return &out
}

// entry is the value node under key in the mapping m; nil when m is nil or no
// mapping, or lacks the key.
func entry(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
