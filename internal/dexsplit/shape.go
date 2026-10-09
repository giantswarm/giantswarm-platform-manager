// Package dexsplit moves an installation's Dex clients out of the encrypted
// dex-app values into the shape the definitions render: one SOPS Secret per
// client secret, referenced from the plaintext dex-app patch, which lists
// every client and the authenticator's trusted peers. The encrypted patch
// keeps what is still secret and no list.
//
// app-operator merges the encrypted values over the plaintext ones, a list
// replacing a list wholesale, so while the encrypted patch carries
// oidc.extraStaticClients or the authenticator's trustedPeers, no client the
// definitions render reaches Dex and the manager refuses the commit
// (plan.DexSecretRefusal). The manager decrypts nothing; this is the laptop
// side that does, through a Vault: sops under a person, beekeeper under an
// agent. No value is ever printed: the report names key paths, files and
// the non-secret fields (ids, names, redirect URIs, peers) that the
// plaintext patch carries anyway.
package dexsplit

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// The dex-app value keys the split reads and writes.
const (
	keyOIDC          = "oidc"
	keyStaticClients = "staticClients"
	keyExtraClients  = "extraStaticClients"
	keyAuthenticator = "dexK8SAuthenticator"
	keyTrustedPeers  = "trustedPeers"
	keyClientSecret  = "clientSecret"
	keyClientRef     = "clientSecretRef"
	keySecret        = "secret"
	keySecretRef     = "secretRef"
	keyID            = "id"
	keyName          = "name"
	keyPublic        = "public"
	keyRedirectURIs  = "redirectURIs"
	keyLogoURL       = "logoURL"
	keySOPS          = "sops"

	pathExtraClients = keyOIDC + "." + keyExtraClients
	pathStatic       = keyOIDC + "." + keyStaticClients
	pathPeers        = pathStatic + "." + keyAuthenticator + "." + keyTrustedPeers
)

// deadKeys are keys of the encrypted patch no dex-app value reads; the
// split drops them with the lists.
var deadKeys = []string{keyOIDC + ".muster"}

// clientFields are the fields of an extra static client the plaintext patch
// carries: configuration, never a secret. A field outside them and secret
// is refused: the split cannot tell whether it is a secret.
var clientFields = []string{keyID, keyName, keyPublic, keyRedirectURIs, keyTrustedPeers, keyLogoURL, keySecretRef}

// Shape is the encrypted dex-app patch as its plaintext keys show it: which
// client secrets are inline, how many entries each list has, what stays.
// Nothing in it is a value; Reveal names the non-secret leaves to decrypt.
type Shape struct {
	// BuiltIns are the built-in clients under oidc.staticClients whose
	// secret is inline (clientSecret), the authenticator excluded.
	BuiltIns []string
	// Extras are the extra static clients, in list order.
	Extras []ExtraShape
	// Peers is how many trusted peers the authenticator lists.
	Peers int
	// Drop are the key paths the split removes from the encrypted patch,
	// each the largest node that empties.
	Drop []string
	// Keep are the key paths the encrypted patch keeps: a leaf, or a list
	// with its entry count.
	Keep []string
}

// ExtraShape is one extra static client as the encrypted list shows it.
type ExtraShape struct {
	Index int
	// Fields are the client's keys, as listed; Inline whether one is
	// secret (the value moves to a Secret).
	Fields []string
	Inline bool
	// RedirectURIs and Peers count the entries of those lists.
	RedirectURIs, Peers int
}

// Path is the client's key path in the encrypted patch.
func (e ExtraShape) Path() string { return pathExtraClients + "." + strconv.Itoa(e.Index) }

// Split says whether the shape has anything to move.
func (s Shape) Split() bool { return len(s.Drop) == 0 }

// ReadShape reads the shape of an encrypted dex-app patch from its plaintext
// keys; the values stay ciphertext. A client with a field the split does not
// know is refused, naming the key path.
func ReadShape(patch []byte) (Shape, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(patch, &doc); err != nil {
		return Shape{}, fmt.Errorf("decode: %w", err)
	}
	if len(doc.Content) == 0 {
		return Shape{}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return Shape{}, fmt.Errorf("the document is no mapping")
	}
	var s Shape
	var drop []string
	static := child(child(root, keyOIDC), keyStaticClients)
	for i := 0; static != nil && static.Kind == yaml.MappingNode && i+1 < len(static.Content); i += 2 {
		key, client := static.Content[i].Value, static.Content[i+1]
		if key == keyAuthenticator {
			if peers := child(client, keyTrustedPeers); peers != nil && peers.Kind == yaml.SequenceNode && len(peers.Content) > 0 {
				s.Peers = len(peers.Content)
				drop = append(drop, pathPeers)
			}
			continue
		}
		if child(client, keyClientSecret) != nil {
			s.BuiltIns = append(s.BuiltIns, key)
			drop = append(drop, pathStatic+"."+key+"."+keyClientSecret)
		}
	}
	if extras := child(child(root, keyOIDC), keyExtraClients); extras != nil && extras.Kind == yaml.SequenceNode && len(extras.Content) > 0 {
		for i, item := range extras.Content {
			e, err := readExtra(i, item)
			if err != nil {
				return Shape{}, err
			}
			s.Extras = append(s.Extras, e)
		}
		drop = append(drop, pathExtraClients)
	}
	for _, k := range deadKeys {
		if lookup(root, k) != nil {
			drop = append(drop, k)
		}
	}
	if len(drop) == 0 {
		s.Keep = leaves(root, "")
		return s, nil
	}
	s.Drop = prune(root, "", drop)
	if slices.Contains(s.Drop, "") {
		return Shape{}, fmt.Errorf("nothing would remain in the encrypted patch: remove the file by hand")
	}
	s.Keep = keptLeaves(root, "", s.Drop)
	return s, nil
}

// readExtra reads one extra static client's keys.
func readExtra(i int, item *yaml.Node) (ExtraShape, error) {
	e := ExtraShape{Index: i}
	if item.Kind != yaml.MappingNode {
		return e, fmt.Errorf("%s: no mapping", e.Path())
	}
	for j := 0; j+1 < len(item.Content); j += 2 {
		key, value := item.Content[j].Value, item.Content[j+1]
		switch {
		case key == keySecret:
			e.Inline = true
		case !slices.Contains(clientFields, key):
			return e, fmt.Errorf("%s.%s: a field the split does not know as configuration or secret; move this client by hand", e.Path(), key)
		case key == keyRedirectURIs && value.Kind == yaml.SequenceNode:
			e.RedirectURIs = len(value.Content)
		case key == keyTrustedPeers && value.Kind == yaml.SequenceNode:
			e.Peers = len(value.Content)
		}
		e.Fields = append(e.Fields, key)
	}
	if !slices.Contains(e.Fields, keyID) {
		return e, fmt.Errorf("%s: a client without an id", e.Path())
	}
	if e.Inline && slices.Contains(e.Fields, keySecretRef) {
		return e, fmt.Errorf("%s: both an inline secret and a secretRef", e.Path())
	}
	return e, nil
}

// prune answers the key paths to unset so that every path of drop is gone:
// a mapping all of whose keys go is unset whole instead of key by key. The
// root's own path is "".
func prune(n *yaml.Node, path string, drop []string) []string {
	if slices.Contains(drop, path) {
		return []string{path}
	}
	if n.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	gone := 0
	keys := 0
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if path == "" && key == keySOPS {
			continue
		}
		keys++
		sub := prune(n.Content[i+1], join(path, key), drop)
		if len(sub) == 1 && sub[0] == join(path, key) {
			gone++
		}
		out = append(out, sub...)
	}
	if keys > 0 && gone == keys {
		return []string{path}
	}
	return out
}

// keptLeaves are the leaves of n outside the dropped paths: a scalar by its
// path, a list by its path and entry count.
func keptLeaves(n *yaml.Node, path string, drop []string) []string {
	if slices.Contains(drop, path) {
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		var out []string
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			if path == "" && key == keySOPS {
				continue
			}
			out = append(out, keptLeaves(n.Content[i+1], join(path, key), drop)...)
		}
		return out
	case yaml.SequenceNode:
		return []string{fmt.Sprintf("%s (%d entries)", path, len(n.Content))}
	}
	return []string{path}
}

// leaves is keptLeaves with nothing dropped.
func leaves(n *yaml.Node, path string) []string { return keptLeaves(n, path, nil) }

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// child is the value under key of the mapping m; nil when m is nil, no
// mapping or lacks the key.
func child(m *yaml.Node, key string) *yaml.Node {
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

// lookup is the node at the dotted path under n; a numeric segment indexes a
// sequence.
func lookup(n *yaml.Node, path string) *yaml.Node {
	for _, k := range strings.Split(path, ".") {
		switch {
		case n == nil:
			return nil
		case n.Kind == yaml.SequenceNode:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(n.Content) {
				return nil
			}
			n = n.Content[i]
		default:
			n = child(n, k)
		}
	}
	return n
}
