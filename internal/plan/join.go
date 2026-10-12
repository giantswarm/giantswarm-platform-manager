package plan

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Join is what a reconcile takes off the installation's muster beyond what
// the shared defaults register: the entries of its agent-platform values
// patch on record that the render drops, and the public-registration
// redirect URIs it moves. An installation whose muster brokered for targets
// of its own — a hub of its organisation, or one that lists servers of its
// own — renders as a target of another hub without the broker's targets,
// the identity providers and the MCP server list, so the join takes those
// servers away from everyone who reaches them through its muster. The dry
// run prints the warning apart from the drift lines, naming each entry; the
// commit is refused while the muster still serves in-cluster servers of its
// own (JoinRefusal), unless the join is forced.
type Join struct {
	// ExchangeTargets are the broker's targets on record that the render
	// drops (muster.muster.oauth.server.tokenExchangeBroker.targets.<name>).
	ExchangeTargets []string `json:"exchangeTargets,omitempty"`
	// IdentityProviders are the identity providers on record that the
	// render drops (agent-platform-mcps.identityProviders.<name>).
	IdentityProviders []string `json:"identityProviders,omitempty"`
	// Servers are the MCP servers on record that the render drops
	// (agent-platform-mcps.mcpServers[]), by URL.
	Servers []JoinServer `json:"servers,omitempty"`
	// RedirectURIs are the public-registration redirect URIs
	// (muster.muster.oauth.server.trustedPublicRegistrationRedirectURIs) the
	// render takes off the patch on record, and the ones it puts on that
	// another plan of the set takes off: the registration moves between the
	// two (MoveRedirectURIs).
	RedirectURIs []JoinURI `json:"redirectURIs,omitempty"`
	// Forced: the join was asked for with the warning known (forceJoin), so
	// the commit proceeds (JoinRefusal is empty).
	Forced bool `json:"forced,omitempty"`
	// added are the redirect URIs the render puts on the patch; paired with
	// the plan of the set that takes them off, or dropped (MoveRedirectURIs).
	added []string
}

// JoinServer is one MCP server the render takes off muster's list.
type JoinServer struct {
	Cluster string `json:"cluster,omitempty"`
	Group   string `json:"group,omitempty"`
	URL     string `json:"url"`
	// Own says the server runs on the installation itself: its cluster is
	// the installation, or its URL is an in-cluster Service.
	Own bool `json:"own,omitempty"`
}

// String is the server as the warning names it.
func (s JoinServer) String() string {
	name := s.URL
	if s.Cluster != "" || s.Group != "" {
		name = s.Cluster + "/" + s.Group + " " + s.URL
	}
	if s.Own {
		return name + " (its own)"
	}
	return name
}

// JoinURI is one public-registration redirect URI the render takes off the
// patch (Added false) or puts on it (Added true); Peer is the installation
// of the set whose plan does the opposite, where one does.
type JoinURI struct {
	URI   string `json:"uri"`
	Added bool   `json:"added,omitempty"`
	Peer  string `json:"peer,omitempty"`
}

// String is the URI as the warning names it.
func (u JoinURI) String() string {
	switch {
	case u.Peer != "" && u.Added:
		return u.URI + " (moved from " + u.Peer + ")"
	case u.Peer != "":
		return u.URI + " (moved to " + u.Peer + ")"
	}
	return u.URI
}

// mcpsValuesKey is the agent-platform-mcps chart's key in the agent-platform
// values patch: muster's server list and the identity providers.
const mcpsValuesKey = "agent-platform-mcps"

// musterServerPath is the path of muster's OAuth server values in the
// agent-platform values patch.
var musterServerPath = []string{"muster", "muster", "oauth", "server"}

// The paths in the agent-platform values patch a join changes.
var (
	brokerTargetsPath     = append(slices.Clone(musterServerPath), "tokenExchangeBroker", "targets")
	identityProvidersPath = []string{mcpsValuesKey, "identityProviders"}
	mcpServersPath        = []string{mcpsValuesKey, "mcpServers"}
	redirectURIsPath      = append(slices.Clone(musterServerPath), "trustedPublicRegistrationRedirectURIs")
)

// join reads the installation's agent-platform values patch on record
// against the render, as the plan writes it, and records what the render
// takes off its muster, or nothing. A patch that does not decode on either
// side records nothing: the comparison names that by itself.
func (p *Installation) join(current, rendered string, forced bool) {
	cur, ren := patchRoot(current), patchRoot(rendered)
	if cur == nil || ren == nil {
		return
	}
	j := &Join{Forced: forced}
	j.ExchangeTargets = missingKeys(at(cur, brokerTargetsPath...), at(ren, brokerTargetsPath...))
	j.IdentityProviders = missingKeys(at(cur, identityProvidersPath...), at(ren, identityProvidersPath...))
	j.Servers = missingServers(at(cur, mcpServersPath...), at(ren, mcpServersPath...), p.Name)
	for _, u := range missingScalars(at(cur, redirectURIsPath...), at(ren, redirectURIsPath...)) {
		j.RedirectURIs = append(j.RedirectURIs, JoinURI{URI: u})
	}
	j.added = missingScalars(at(ren, redirectURIsPath...), at(cur, redirectURIsPath...))
	if j.Pruned() == nil && len(j.added) == 0 {
		return
	}
	p.Join = j
}

// patchRoot is the mapping of a values patch, or nil.
func patchRoot(text string) *yaml.Node {
	docs, err := documents(text)
	if err != nil || len(docs) != 1 {
		return nil
	}
	if m := root(docs[0]); m.Kind == yaml.MappingNode {
		return m
	}
	return nil
}

// missingKeys are the keys of the mapping cur that the mapping ren lacks, in
// cur's order; a side that is no mapping has none.
func missingKeys(cur, ren *yaml.Node) []string {
	if cur == nil || cur.Kind != yaml.MappingNode {
		return nil
	}
	var out []string
	for i := 0; i+1 < len(cur.Content); i += 2 {
		if key := cur.Content[i].Value; entry(ren, key) == nil {
			out = append(out, key)
		}
	}
	return out
}

// missingScalars are the scalars of the sequence cur that the sequence ren
// lacks, in cur's order; a side that is no sequence has none.
func missingScalars(cur, ren *yaml.Node) []string {
	if cur == nil || cur.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, item := range cur.Content {
		if item.Kind != yaml.ScalarNode || item.Value == "" {
			continue
		}
		if ren != nil && ren.Kind == yaml.SequenceNode && contains(ren, item.Value) {
			continue
		}
		out = append(out, item.Value)
	}
	return out
}

// missingServers are the entries of muster's server list cur whose URL the
// list ren lacks, in cur's order, each marked as the installation's own
// where it runs there.
func missingServers(cur, ren *yaml.Node, installation string) []JoinServer {
	if cur == nil || cur.Kind != yaml.SequenceNode {
		return nil
	}
	rendered := map[string]bool{}
	if ren != nil && ren.Kind == yaml.SequenceNode {
		for _, item := range ren.Content {
			rendered[text(entry(item, "url"))] = true
		}
	}
	var out []JoinServer
	for _, item := range cur.Content {
		u := text(entry(item, "url"))
		if u == "" || rendered[u] {
			continue
		}
		s := JoinServer{Cluster: text(entry(item, "cluster")), Group: text(entry(item, "group")), URL: u}
		s.Own = s.Cluster == installation || inCluster(u)
		out = append(out, s)
	}
	return out
}

// text is the node's value, "" for none or no scalar.
func text(n *yaml.Node) string {
	if !scalar(n) {
		return ""
	}
	return n.Value
}

// inCluster says whether a server's URL names a Service of the cluster
// (<name>.<namespace>.svc, or with the cluster's domain).
func inCluster(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(u.Hostname(), ".cluster.local")
	return strings.HasSuffix(host, ".svc")
}

// Pruned is j, or nil where it tells nothing: a plan whose patch puts
// redirect URIs on that no plan of the set takes off warns of nothing.
func (j *Join) Pruned() *Join {
	if j == nil || len(j.ExchangeTargets) == 0 && len(j.IdentityProviders) == 0 && len(j.Servers) == 0 && len(j.RedirectURIs) == 0 {
		return nil
	}
	return j
}

// ownServers are the URLs of the servers the render drops that run on the
// installation itself.
func (j *Join) ownServers() []string {
	var out []string
	for _, s := range j.Servers {
		if s.Own {
			out = append(out, s.URL)
		}
	}
	return out
}

// JoinRefusal says why a commit of p is refused for the registry its muster
// still serves: the agent-platform values on record list in-cluster MCP
// servers of the installation's own, and the render drops the list whole —
// its own servers, every target's and the exchange behind them — so anyone
// reaching them through this muster loses them the moment the commit
// merges. Empty where the render drops no server of the installation's own,
// or the join is forced. The comparison runs either way; only the commit is
// held.
func (p Installation) JoinRefusal() string {
	if p.Join == nil || p.Join.Forced {
		return ""
	}
	own := p.Join.ownServers()
	if len(own) == 0 {
		return ""
	}
	return fmt.Sprintf("%s's muster serves a registry of its own: its agent-platform values list in-cluster servers of its own (%s), and this reconcile drops the list whole — %s, %s and %s go, muster registers the shared defaults' servers alone, and everyone who reaches the others through %s's muster loses them. Move the registry to the hub first, register a server of its own as an MCPServer object under extras/agent-platform/mcpservers/, which a reconcile keeps, or force the join (forceJoin; platformctl --force-join), which proceeds with the warning",
		p.Name, strings.Join(own, ", "), count(len(p.Join.Servers), "server entry", "server entries"), count(len(p.Join.ExchangeTargets), "exchange target", "exchange targets"), count(len(p.Join.IdentityProviders), "identity provider", "identity providers"), p.Name)
}

// count is n with its noun, singular or plural.
func count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// MoveRedirectURIs pairs the public-registration redirect URIs across the
// plans of one set: a URI one plan's patch takes off that another plan's
// puts on moves between the two, and both sides name the other (JoinURI's
// Peer). A URI put on that no plan of the set takes off is no join's: the
// plan's Join is dropped where that is all it held.
func MoveRedirectURIs(plans []*Installation) {
	from := map[string]*Installation{} // a URI → the plan whose patch takes it off
	for _, p := range plans {
		if p.Join == nil {
			continue
		}
		for _, u := range p.Join.RedirectURIs {
			if !u.Added {
				from[u.URI] = p
			}
		}
	}
	for _, p := range plans {
		if p.Join == nil {
			continue
		}
		for _, uri := range p.Join.added {
			other := from[uri]
			if other == nil || other == p {
				continue
			}
			p.Join.RedirectURIs = append(p.Join.RedirectURIs, JoinURI{URI: uri, Added: true, Peer: other.Name})
			for i := range other.Join.RedirectURIs {
				if u := &other.Join.RedirectURIs[i]; u.URI == uri && !u.Added {
					u.Peer = p.Name
				}
			}
		}
		p.Join = p.Join.Pruned()
	}
}
