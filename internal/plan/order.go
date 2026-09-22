package plan

import (
	"bytes"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// The pull requests of an action merge in dependency order, and the order
// follows what the files reference, not the repositories' kind: a pull
// request whose files create an object another pull request's files
// reference merges before it. On dex-app a referenced Dex client is a
// secretKeyRef on the Dex pod, so the configs patch that names the client
// must not land before the management-clusters Secret it names; a RemoteApp
// and the tunnelport release name Teleport provision tokens that
// teleport-fleet's tunnelport values create. The objects are named
// "<kind>/<name>": a Secret a manifest declares and a secretRef
// (clientSecretRef, apiKeySecretRef, secretKeyRef, …), an existingSecret or
// a secretName names; a ProvisionToken the tunnelport values list among
// their tokens and a tokenName names. Among pull requests no reference
// orders, the repositories' kind does (rank).

// The kinds of the objects the order follows, and the keys that create or
// name them.
const (
	kindSecret         = "Secret"
	kindProvisionToken = "ProvisionToken"
	keyKind            = "kind"
	keyMetadata        = "metadata"
	keyTokenName       = "tokenName"
	keyExistingSecret  = "existingSecret"
	keySecretName      = "secretName"
	keySecretKeyRef    = "secretkeyref"
	suffixSecretRef    = "secretref"
)

// Dependency is one pull request another merges after: its repository and
// the objects its files create that the other's files reference.
type Dependency struct {
	Repository string   `json:"repository"`
	Objects    []string `json:"objects"`
}

// AfterClause says which pull requests p merges after and why, for a
// person: "after <repository> (<objects>)", one per dependency; empty where
// the repositories' kind alone orders p.
func (p PullRequest) AfterClause() string {
	parts := make([]string, 0, len(p.After))
	for _, d := range p.After {
		parts = append(parts, d.Repository+" ("+strings.Join(d.Objects, ", ")+")")
	}
	if len(parts) == 0 {
		return ""
	}
	return "after " + strings.Join(parts, ", ")
}

// objects reads what a file creates and what it references, each name once
// and sorted: the Secrets its manifests declare and the provision tokens its
// tunnelport values list; the Secrets and tokens its keys name. A document
// that does not parse contributes nothing; the ones before it stand.
func objects(content []byte) (creates, references []string) {
	c, r := map[string]bool{}, map[string]bool{}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			break
		}
		root := &doc
		if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
			root = root.Content[0]
		}
		if kind := entry(root, keyKind); kind != nil && kind.Value == kindSecret {
			if name := entry(entry(root, keyMetadata), keyName); name != nil && name.Value != "" {
				c[kindSecret+"/"+name.Value] = true
			}
		}
		walk(entry(root, keyTunnelport), func(key string, value *yaml.Node) {
			if key != keyTokens || value.Kind != yaml.SequenceNode {
				return
			}
			for _, item := range value.Content {
				if name := entry(item, keyName); name != nil && name.Value != "" {
					c[kindProvisionToken+"/"+name.Value] = true
				}
			}
		})
		walk(root, func(key string, value *yaml.Node) {
			lower := strings.ToLower(key)
			switch {
			case key == keyTokenName && scalar(value):
				r[kindProvisionToken+"/"+value.Value] = true
			case (key == keyExistingSecret || key == keySecretName) && scalar(value):
				r[kindSecret+"/"+value.Value] = true
			case strings.HasSuffix(lower, suffixSecretRef) || lower == keySecretKeyRef:
				if name := entry(value, keyName); scalar(name) {
					r[kindSecret+"/"+name.Value] = true
				}
			}
		})
	}
	return keys(c), keys(r)
}

// references are the objects content names.
func references(content string) []string {
	_, r := objects([]byte(content))
	return r
}

// scalar says whether n is a non-empty scalar.
func scalar(n *yaml.Node) bool { return n != nil && n.Kind == yaml.ScalarNode && n.Value != "" }

// walk calls fn with every key of every mapping under n, sequences included;
// a nil n is nothing.
func walk(n *yaml.Node, fn func(key string, value *yaml.Node)) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			fn(n.Content[i].Value, n.Content[i+1])
			walk(n.Content[i+1], fn)
		}
	case yaml.SequenceNode, yaml.DocumentNode:
		for _, c := range n.Content {
			walk(c, fn)
		}
	}
}

// introduced is what a file brings onto the installation: the objects
// content creates that current, the file on record where it was read, does
// not; a file with no record introduces every one it creates.
func introduced(content, current string, onRecord bool) []string {
	creates, _ := objects([]byte(content))
	if !onRecord {
		return creates
	}
	were, _ := objects([]byte(current))
	return slices.DeleteFunc(creates, func(o string) bool { return slices.Contains(were, o) })
}

// rank is the kind order of repositories, the order among pull requests no
// reference orders: an installation's configs before its
// management-clusters, the hub's pair after the installation's,
// teleport-fleet after the hub's.
func rank(repo string, inst, hub installations.Installation) int {
	switch repo {
	case inst.Repositories.Configs:
		return 0
	case inst.Repositories.ManagementClusters:
		return 1
	case hub.Repositories.Configs:
		return 2
	case hub.Repositories.ManagementClusters:
		return 3
	case "giantswarm/teleport-fleet":
		return 4
	}
	if strings.HasSuffix(repo, "-configs") {
		return 5
	}
	return 6
}

// order sorts prs, one per repository, into the merge order and numbers
// them: a pull request whose files reference an object another one's files
// create follows it (After names every such pull request with the objects);
// among the pull requests ready to merge, the repositories' kind (rank) and
// name. A cycle, which no order satisfies, falls back to the kind order for
// the pull requests in it.
func order(prs []PullRequest, rank map[string]int, creates, references map[string]map[string]bool) []PullRequest {
	byRepo := make(map[string]*PullRequest, len(prs))
	dependents := map[string][]string{} // a repository → the ones that follow it
	waits := make(map[string]int, len(prs))
	for i := range prs {
		pr := &prs[i]
		byRepo[pr.Repository] = pr
		deps := map[string][]string{}
		for ref := range references[pr.Repository] {
			for other, made := range creates {
				if other != pr.Repository && made[ref] {
					deps[other] = append(deps[other], ref)
				}
			}
		}
		for other, objs := range deps {
			sort.Strings(objs)
			pr.After = append(pr.After, Dependency{Repository: other, Objects: objs})
			dependents[other] = append(dependents[other], pr.Repository)
		}
		sort.Slice(pr.After, func(i, j int) bool { return pr.After[i].Repository < pr.After[j].Repository })
		waits[pr.Repository] = len(deps)
	}
	before := func(a, b string) bool {
		if rank[a] != rank[b] {
			return rank[a] < rank[b]
		}
		return a < b
	}
	var ready, sorted []string
	for repo, n := range waits {
		if n == 0 {
			ready = append(ready, repo)
		}
	}
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return before(ready[i], ready[j]) })
		repo := ready[0]
		ready = ready[1:]
		sorted = append(sorted, repo)
		for _, d := range dependents[repo] {
			if waits[d]--; waits[d] == 0 {
				ready = append(ready, d)
			}
		}
	}
	var cycle []string
	for repo, n := range waits {
		if n > 0 {
			cycle = append(cycle, repo)
		}
	}
	sort.Slice(cycle, func(i, j int) bool { return before(cycle[i], cycle[j]) })
	out := make([]PullRequest, 0, len(prs))
	for i, repo := range append(sorted, cycle...) {
		pr := *byRepo[repo]
		pr.Order = i + 1
		out = append(out, pr)
	}
	return out
}
