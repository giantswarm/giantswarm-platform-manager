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
	keyConnectors         = "connectors"
	keyClientID           = "id"
	listStaticClients     = keyOIDC + "." + keyStaticClients
	listExtraClients      = keyOIDC + "." + keyExtraStaticClients
	mapCustomer           = keyOIDC + "." + keyCustomer
	listConnectors        = mapCustomer + "." + keyConnectors
)

// keepDexPatch answers rendered with every part of current's dex-app
// configmap patch that no definition owns, and names what it kept. The patch
// is one file with several owners: a definition owns the clients and the
// connectors it renders — a built-in client under oidc.staticClients by its
// key, an extra static client by its id, a token-exchange connector under
// oidc.customer.connectors by its id — and nothing else. A top-level key
// rendered lacks (the installation's ingress tuning), a key under oidc it
// lacks, a key under oidc.customer it lacks (the installation's login
// connector), a built-in client it does not render, an extra static client
// or a connector whose id it does not declare are another owner's and stay,
// after the definition's, with their comments. A client or connector the
// definition renders is the definition's whole: its entry replaces the
// current one, so nothing stale inside it (an inline secret, an old issuer)
// survives — but for the authenticator's trusted peers: a peer on record the
// definition does not render is the installation's own and stays, after the
// definition's (keepList). Nothing kept leaves rendered as it is.
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
			case key == keyExtraStaticClients && r.Kind == yaml.SequenceNode && c.Kind == yaml.SequenceNode:
				keepClients(r, c, listExtraClients, &kept)
			case key == keyCustomer && r.Kind == yaml.MappingNode && c.Kind == yaml.MappingNode:
				keepKeys(r, c, mapCustomer, &kept, func(key string, r, c *yaml.Node) {
					if key == keyConnectors && r.Kind == yaml.SequenceNode && c.Kind == yaml.SequenceNode {
						keepClients(r, c, listConnectors, &kept)
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

// keepClients appends to the sequence ren every entry of the sequence cur
// whose id ren does not declare — an extra static client, a connector —
// recording each under list.
func keepClients(ren, cur *yaml.Node, list string, kept *[]Kept) {
	declared := map[string]bool{}
	for _, item := range ren.Content {
		if id := entry(item, keyClientID); id != nil {
			declared[id.Value] = true
		}
	}
	for _, item := range cur.Content {
		id := entry(item, keyClientID)
		if id == nil || declared[id.Value] {
			continue
		}
		ren.Content = append(ren.Content, item)
		*kept = append(*kept, Kept{List: list, Entry: id.Value})
	}
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
