package plan

import (
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// platformPatchFile ends the path of an installation's agent-platform
// configmap patch: the definition's file, whose audience lists the
// installation adds its own entries to.
const platformPatchFile = "/apps/agent-platform/configmap-values.yaml.patch"

// The audience lists the installation adds its own entries to, by the key
// path the plan names them under in Kept: three of the platform patch and
// one of the dex-app configmap patch. Each keeps the entries on record the
// render does not carry, in that list and nowhere else.
const (
	ListTrustedAudiences = "muster.muster.oauth.server.trustedAudiences"
	ListExtraAudience    = "kagent.oauth2-proxy.extraArgs.oidc-extra-audience"
	ListEdgeAudiences    = "agent-platform-mcps.agentgateway.jwt.extraProviders[*].audiences"
	ListTrustedPeers     = listStaticClients + "." + keyAuthenticator + "." + keyTrustedPeers
	// ListAllowedCallers is the chat gateway's list of the platform workloads
	// allowed to call its team reviews: the policy names the platform's own,
	// an installation the other teams' that run there (the hub's sweeps).
	ListAllowedCallers = "klausGateway.reviews.allowedCallers"

	keyAuthenticator = "dexK8SAuthenticator"
	keyTrustedPeers  = "trustedPeers"
	keyIssuer        = "issuer"
	keyAudiences     = "audiences"
)

// joinedLists are the lists of the platform patch the plan merges as a
// comma-separated set (keepJoined), by key path: one scalar on the file,
// whose entries the comparison addresses as <path>[<entry>].
var joinedLists = []string{ListExtraAudience}

// JoinedList says whether the leaf at yamlPath of the file at path is one
// the plan merges as a comma-separated set: the scalar's entries are
// compared as a set, each addressed as yamlPath[entry].
func JoinedList(path, yamlPath string) bool {
	return strings.HasSuffix(path, platformPatchFile) && slices.Contains(joinedLists, yamlPath)
}

// keepAudiences answers rendered with every entry of current's audience
// lists that rendered does not carry — muster's trustedAudiences, the kagent
// UI's comma-separated oidc-extra-audience (merged as a set, written back
// comma-joined), the audiences of the edge's JWT provider of the same issuer,
// the chat gateway's review callers — after the render's, in current's
// order, and with every key the definition keeps (the removals of kind kept:
// keptPlatformKeys) as it is on record, and names what it kept. The render
// carries the ids the definition knows: its own clients and the portals'. An
// id the installation trusts besides is its own and stays in the list it is
// in, nowhere else: a peer of the authenticator is no audience of the kagent
// UI. A list the render lacks (the UI's without kagent, the edge's off the 4
// line, the reviews' without a gateway) keeps nothing: the component does not
// run. Nothing kept leaves rendered as it is.
func keepAudiences(rendered, current []byte) ([]byte, []Kept, error) {
	_, cur, err := mapping(current)
	if err != nil {
		return nil, nil, err
	}
	doc, ren, err := mapping(rendered)
	if err != nil {
		return nil, nil, err
	}
	var kept []Kept
	trusted := []string{"muster", "muster", "oauth", "server", "trustedAudiences"}
	keepList(at(ren, trusted...), at(cur, trusted...), ListTrustedAudiences, &kept)
	for _, list := range joinedLists {
		keys := strings.Split(list, ".")
		keepJoined(at(ren, keys...), at(cur, keys...), list, &kept)
	}
	providers := []string{"agent-platform-mcps", "agentgateway", "jwt", "extraProviders"}
	keepProviders(at(ren, providers...), at(cur, providers...), &kept)
	callers := strings.Split(ListAllowedCallers, ".")
	keepList(at(ren, callers...), at(cur, callers...), ListAllowedCallers, &kept)
	keepSubtrees(ren, cur, keptPlatformKeys, &kept)
	if len(kept) == 0 {
		return rendered, nil, nil
	}
	out, err := encode(doc)
	return out, kept, err
}

// keepList appends to the sequence ren every scalar of the sequence cur that
// ren does not carry, in cur's order, each recorded under list; a side that
// is no sequence keeps nothing.
func keepList(ren, cur *yaml.Node, list string, kept *[]Kept) {
	if ren == nil || ren.Kind != yaml.SequenceNode || cur == nil || cur.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range cur.Content {
		if item.Kind != yaml.ScalarNode || item.Value == "" || contains(ren, item.Value) {
			continue
		}
		ren.Content = append(ren.Content, item)
		ren.Style = 0
		*kept = append(*kept, Kept{List: list, Entry: item.Value})
	}
}

// keepJoined merges into the scalar ren — oidc-extra-audience, the
// StringSlice flag the chart's extraArgs map takes as one comma-separated
// value — every id of cur that ren lacks: the same scalar, or a list in a
// hand-written patch. The set is written back comma-joined, ren's ids
// first, each kept id recorded under list.
func keepJoined(ren, cur *yaml.Node, list string, kept *[]Kept) {
	if ren == nil || ren.Kind != yaml.ScalarNode || cur == nil {
		return
	}
	ids := SplitJoined(ren.Value)
	for _, id := range audienceIDs(cur) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
			*kept = append(*kept, Kept{List: list, Entry: id})
		}
	}
	ren.Value = strings.Join(ids, ",")
}

// keepProviders merges into each JWT provider of the sequence ren the
// audiences of cur's provider with the same issuer: the audiences are the
// issuer's, another issuer's are not this provider's.
func keepProviders(ren, cur *yaml.Node, kept *[]Kept) {
	if ren == nil || ren.Kind != yaml.SequenceNode || cur == nil || cur.Kind != yaml.SequenceNode {
		return
	}
	for _, r := range ren.Content {
		issuer := entry(r, keyIssuer)
		if issuer == nil {
			continue
		}
		for _, c := range cur.Content {
			if i := entry(c, keyIssuer); i != nil && i.Value == issuer.Value {
				keepList(entry(r, keyAudiences), entry(c, keyAudiences), ListEdgeAudiences, kept)
			}
		}
	}
}

// audienceIDs are the ids a node of oidc-extra-audience carries: the
// comma-separated scalar split, or a list's scalars, each trimmed, none
// empty.
func audienceIDs(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return SplitJoined(n.Value)
	case yaml.SequenceNode:
		var ids []string
		for _, item := range n.Content {
			if item.Kind == yaml.ScalarNode {
				ids = append(ids, SplitJoined(item.Value)...)
			}
		}
		return ids
	}
	return nil
}

// SplitJoined splits a comma-separated list of ids, trimmed, none empty: the
// entries of a scalar the plan merges as a set.
func SplitJoined(s string) []string {
	var ids []string
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// contains says whether the sequence seq carries the scalar value.
func contains(seq *yaml.Node, value string) bool {
	for _, item := range seq.Content {
		if item.Kind == yaml.ScalarNode && item.Value == value {
			return true
		}
	}
	return false
}

// at is the node under the key path keys in the mapping m; nil where a key
// is absent or the node on the way is no mapping.
func at(m *yaml.Node, keys ...string) *yaml.Node {
	for _, key := range keys {
		if m = entry(m, key); m == nil {
			return nil
		}
	}
	return m
}
