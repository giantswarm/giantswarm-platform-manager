package plan

import (
	"net/url"
	"path"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// What a disable takes out beside the definition's render: the files a
// person put under the capability's directories, the kustomization entries
// that name them, and the Dex clients of the installation's patch that
// belong to the capability — a client whose Secret is in a removed file, or
// whose redirect URIs name a host of the removed platform. What it cannot
// attribute stays and is named (Leftover), never dropped silently.

// Leftover is an entry of a file the disable edits that stays on record
// although nothing that stays renders it: the disable cannot tell it is the
// capability's, and says why.
type Leftover struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Entry      string `json:"entry"`
	Why        string `json:"why"`
}

// gone is what the disable removes, known before the files it edits are
// edited: the paths it deletes (repository:path), the Secrets those files
// declare and the hosts of the removed platform.
type gone struct {
	files   map[string]*Removal
	secrets map[string]bool
	hosts   []string
	// keeps says whether a path of a repository is a kept file or a
	// directory holding one.
	keeps func(repository, p string) bool
}

// keptEntries is the part of a kustomization that names a kept file or a
// directory holding one, as a kustomization of its own: what stays of the
// file whatever the definition renders into it.
func (g gone) keptEntries(ref fileRef, content []byte) []byte {
	if g.keeps == nil || path.Base(ref.path) != kustomizationFile {
		return nil
	}
	_, m, err := mapping(content)
	if err != nil {
		return nil
	}
	out := &yaml.Node{Kind: yaml.MappingNode}
	for _, list := range keptLists {
		seq := entry(m, list)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		kept := &yaml.Node{Kind: yaml.SequenceNode}
		for _, e := range seq.Content {
			if e.Kind == yaml.ScalarNode && g.keeps(ref.repository, path.Clean(path.Join(path.Dir(ref.path), e.Value))) {
				kept.Content = append(kept.Content, e)
			}
		}
		if len(kept.Content) > 0 {
			out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: list}, kept)
		}
	}
	if len(out.Content) == 0 {
		return nil
	}
	b, err := encode(&yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{out}})
	if err != nil {
		return nil
	}
	return b
}

// unlistRemoved takes every entry out of a kustomization that names a file
// the disable deletes, or a directory whose kustomization it deletes, and
// records the entry on that file's removal (ListedIn). A remote base and an
// entry naming something that stays are left as they are.
func (g gone) unlistRemoved(ref fileRef, content []byte) ([]byte, error) {
	if path.Base(ref.path) != kustomizationFile {
		return content, nil
	}
	_, m, err := mapping(content)
	if err != nil {
		return nil, err
	}
	out := content
	for _, list := range keptLists {
		seq := entry(m, list)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, e := range seq.Content {
			if e.Kind != yaml.ScalarNode || strings.Contains(e.Value, "://") {
				continue
			}
			target := path.Clean(path.Join(path.Dir(ref.path), e.Value))
			r := g.files[fileRef{ref.repository, target}.key()]
			if r == nil {
				r = g.files[fileRef{ref.repository, path.Join(target, kustomizationFile)}.key()]
			}
			if r == nil {
				continue
			}
			if out, err = UnlistEntry(out, list, e.Value); err != nil {
				return nil, err
			}
			r.ListedIn = append(r.ListedIn, ref.path+" "+list+"["+e.Value+"]")
		}
	}
	return out, nil
}

// patchClient is one client of a Dex patch: an entry of extraStaticClients,
// or of staticClients by its key.
type patchClient struct {
	list string // extraStaticClients or staticClients
	key  string // the staticClients key; "" in extraStaticClients
	node *yaml.Node
}

func (c patchClient) id() string {
	if id := entry(c.node, keyClientID); id != nil && id.Value != "" {
		return id.Value
	}
	return c.key
}

func (c patchClient) secret() string {
	for _, ref := range []string{"secretRef", "clientSecretRef"} {
		if n := entry(entry(c.node, ref), "name"); n != nil {
			return n.Value
		}
	}
	return ""
}

func (c patchClient) redirectURIs() []string {
	var out []string
	if seq := entry(c.node, "redirectURIs"); seq != nil && seq.Kind == yaml.SequenceNode {
		for _, u := range seq.Content {
			out = append(out, u.Value)
		}
	}
	return out
}

func (c patchClient) String() string {
	if c.key != "" {
		return c.list + "[" + c.key + "]"
	}
	return c.list + "[" + c.id() + "]"
}

// clientsOf are the clients of a Dex patch's oidc mapping that carry a
// Secret or a redirect URI: an entry with neither (dexK8SAuthenticator's
// trusted peers) is no client the disable could attribute.
func clientsOf(oidc *yaml.Node) []patchClient {
	var out []patchClient
	if seq := entry(oidc, "extraStaticClients"); seq != nil && seq.Kind == yaml.SequenceNode {
		for _, n := range seq.Content {
			if n.Kind == yaml.MappingNode {
				out = append(out, patchClient{list: "extraStaticClients", node: n})
			}
		}
	}
	if m := entry(oidc, "staticClients"); m != nil && m.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(m.Content); i += 2 {
			if m.Content[i+1].Kind == yaml.MappingNode {
				out = append(out, patchClient{list: "staticClients", key: m.Content[i].Value, node: m.Content[i+1]})
			}
		}
	}
	return slices.DeleteFunc(out, func(c patchClient) bool { return c.secret() == "" && len(c.redirectURIs()) == 0 })
}

// dropClients takes out of a Dex patch every client that no capability that
// stays renders (keeps) and that is the removed capability's: its Secret in
// a removed file, or a redirect URI on a host of the removed platform; its
// id goes from every trustedPeers list with it. drops names each entry it
// took out and why; left every other client, which stays.
func (g gone) dropClients(content []byte, keeps [][]byte) (out []byte, drops []string, left []Leftover, err error) {
	doc, m, err := mapping(content)
	if err != nil {
		return nil, nil, nil, err
	}
	oidc := entry(m, "oidc")
	if oidc == nil || oidc.Kind != yaml.MappingNode {
		return content, nil, nil, nil
	}
	var stays []patchClient
	for _, k := range keeps {
		if _, km, err := mapping(k); err == nil {
			stays = append(stays, clientsOf(entry(km, "oidc"))...)
		}
	}
	dropped := map[string]bool{}
	for _, c := range clientsOf(oidc) {
		if slices.ContainsFunc(stays, func(s patchClient) bool { return s.list == c.list && s.id() == c.id() }) {
			continue
		}
		why := g.whose(c)
		if why == "" {
			left = append(left, Leftover{Entry: c.String(), Why: unattributed(c)})
			continue
		}
		drops = append(drops, c.String()+": "+why)
		dropped[c.id()] = true
		removeClient(oidc, c)
	}
	if len(dropped) == 0 {
		return content, nil, left, nil
	}
	for _, id := range sortedKeys(dropped, nil) {
		if removePeer(oidc, id) {
			drops = append(drops, "trustedPeers["+id+"]: the client it names leaves the patch")
		}
	}
	pruneOIDC(m)
	out, err = encode(doc)
	return out, drops, left, err
}

// whose says why a client is the removed capability's, or "" when the
// disable cannot tell.
func (g gone) whose(c patchClient) string {
	if s := c.secret(); s != "" && g.secrets[s] {
		return "its Secret " + s + " is in a removed file"
	}
	for _, u := range c.redirectURIs() {
		if h := hostOf(u); h != "" && slices.Contains(g.hosts, h) {
			return "its redirect URI " + u + " names " + h + ", a host of the removed platform"
		}
	}
	return ""
}

func unattributed(c patchClient) string {
	why := "no capability that stays renders it"
	if s := c.secret(); s != "" {
		why += ", its Secret " + s + " is in no file the disable removes"
	}
	if len(c.redirectURIs()) > 0 {
		why += ", its redirect URIs name no host of the removed platform"
	}
	return why
}

func removeClient(oidc *yaml.Node, c patchClient) {
	if c.key != "" {
		m := entry(oidc, c.list)
		if at := keyIndex(m, c.key); at >= 0 {
			m.Content = slices.Delete(m.Content, at, at+2)
		}
		return
	}
	seq := entry(oidc, c.list)
	seq.Content = slices.DeleteFunc(seq.Content, func(n *yaml.Node) bool { return n == c.node })
}

// removePeer takes id out of every trustedPeers list under n.
func removePeer(n *yaml.Node, id string) bool {
	changed := false
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			v := n.Content[i+1]
			if n.Content[i].Value == "trustedPeers" && v.Kind == yaml.SequenceNode {
				before := len(v.Content)
				v.Content = slices.DeleteFunc(v.Content, func(p *yaml.Node) bool { return p.Value == id })
				changed = changed || len(v.Content) != before
				continue
			}
			changed = removePeer(v, id) || changed
		}
	}
	if n.Kind == yaml.SequenceNode {
		for _, v := range n.Content {
			changed = removePeer(v, id) || changed
		}
	}
	return changed
}

// pruneOIDC drops what dropClients left empty under oidc: a trustedPeers
// list, a staticClients entry holding nothing else, the client lists, the
// oidc mapping itself. Every other empty value stays the file's.
func pruneOIDC(m *yaml.Node) {
	oidc := entry(m, "oidc")
	if static := entry(oidc, "staticClients"); static != nil && static.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(static.Content); {
			v := static.Content[i+1]
			dropEmpty(v, "trustedPeers")
			if v.Kind == yaml.MappingNode && len(v.Content) == 0 {
				static.Content = slices.Delete(static.Content, i, i+2)
				continue
			}
			i += 2
		}
	}
	dropEmpty(oidc, "staticClients")
	dropEmpty(oidc, "extraStaticClients")
	dropEmpty(m, "oidc")
}

// dropEmpty removes key from m when its value is an empty mapping or list.
func dropEmpty(m *yaml.Node, key string) {
	at := keyIndex(m, key)
	if at < 0 {
		return
	}
	if v := m.Content[at+1]; (v.Kind == yaml.MappingNode || v.Kind == yaml.SequenceNode) && len(v.Content) == 0 {
		m.Content = slices.Delete(m.Content, at, at+2)
	}
}

// hostsOf are the hosts a render serves: every redirect URI of its Dex
// patches and every HTTP probe's address — for a probe of Dex's /auth, the
// redirect URI it asks for, Dex's own host being the installation's.
func hostsOf(res *render.Result) []string {
	var out []string
	add := func(u string) {
		if h := hostOf(u); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	for _, files := range res.Files {
		for p, f := range files {
			if !strings.HasSuffix(p, dexPatchFile) {
				continue
			}
			if _, m, err := mapping(f.Content); err == nil {
				for _, c := range clientsOf(entry(m, "oidc")) {
					for _, u := range c.redirectURIs() {
						add(u)
					}
				}
			}
		}
	}
	for _, p := range res.Probes {
		if p.Kind != render.HTTP {
			continue
		}
		if !p.Expect.DexConnectorStep {
			add(p.URL)
			continue
		}
		if u, err := url.Parse(p.URL); err == nil {
			add(u.Query().Get("redirect_uri"))
		}
	}
	sort.Strings(out)
	return out
}

// platformHosts are the hosts of the definition's render no capability that
// stays serves too.
func platformHosts(res *render.Result, remaining []Remaining) []string {
	var theirs []string
	for _, r := range remaining {
		theirs = append(theirs, hostsOf(r.Result)...)
	}
	return slices.DeleteFunc(hostsOf(res), func(h string) bool { return slices.Contains(theirs, h) })
}

func hostOf(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
