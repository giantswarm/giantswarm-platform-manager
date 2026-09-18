// Package plan turns a rendered fileset into the answer of a dry run: the
// files per repository with the change each one is against the repository
// as it is, the pull requests in dependency order, the generated secrets by
// name, the Dex clients with their redirect URIs, the secret values the
// person supplies at commit, the actions the customer has to take and the
// probes the verify will run. No value of any secret appears here.
package plan

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// Change is what a rendered file is against the repository now.
type Change string

// The changes.
const (
	ChangeCreate    Change = "create"
	ChangeUpdate    Change = "update"
	ChangeUnchanged Change = "unchanged"
	// ChangeUnknown: the current file could not be read as the caller.
	ChangeUnknown Change = "unknown"
)

// File is one rendered file in the plan.
type File struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Change     Change `json:"change"`
	// Content is the rendered file, plaintext with GENERATED(<name>) and
	// SUPPLIED(<field>) markers where the commit step puts values; omitted
	// when the caller asked for paths only.
	Content string `json:"content,omitempty"`
	// Generated names the values the commit step generates into this file.
	Generated []string `json:"generated,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// GeneratedSecret is one value the commit step generates, by name only.
type GeneratedSecret struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Length int      `json:"length"`
	Files  []string `json:"files"`
}

// DexClient is one client the dex patch declares, with its redirect URIs.
type DexClient struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Public       bool     `json:"public,omitempty"`
	Secret       string   `json:"secret,omitempty"`
	RedirectURIs []string `json:"redirectURIs,omitempty"`
	TrustedPeers []string `json:"trustedPeers,omitempty"`
}

// CustomerAction is something the rollout needs from the customer that no
// pull request of this manager delivers.
type CustomerAction struct {
	Installation string `json:"installation"`
	Action       string `json:"action"`
	Why          string `json:"why"`
}

// Probe is one live dimension of the definition: what the verify checks on
// the running installation.
type Probe struct {
	ID      string `json:"id"`
	Feature string `json:"feature"`
	Key     string `json:"key"`
}

// Include is a shared kustomization entry the commit step adds.
type Include struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Resource   string `json:"resource"`
}

// Installation is the dry run of one installation.
type Installation struct {
	Name string `json:"name"`
	// State is the capability's state read from the repositories now.
	State installations.State  `json:"state"`
	OptIn *installations.OptIn `json:"optIn,omitempty"`
	// Inputs are the effective inputs: the record, the typed inputs over it.
	Inputs map[string]any `json:"inputs"`
	// Refused is the render's refusal, when the definition refuses these
	// inputs (an unknown key, a policy, an input this version does not
	// render); the rest is empty then.
	Refused string `json:"refused,omitempty"`
	// CommitRefused says why a commit of this dry run would be refused (the
	// installation is not opted in); empty when a commit could go ahead.
	CommitRefused    string            `json:"commitRefused,omitempty"`
	Files            []File            `json:"files"`
	Includes         []Include         `json:"includes"`
	GeneratedSecrets []GeneratedSecret `json:"generatedSecrets"`
	SuppliedSecrets  []string          `json:"suppliedSecrets"`
	DexClients       []DexClient       `json:"dexClients"`
	CustomerActions  []CustomerAction  `json:"customerActions"`
	Probes           []Probe           `json:"probes"`
	// Diff counts the files by change; an empty diff is every file unchanged.
	Diff map[Change]int `json:"diff"`
}

// PullRequest is one pull request the commit would open: one per repository,
// in dependency order, over every installation of the set.
type PullRequest struct {
	Order            int      `json:"order"`
	Repository       string   `json:"repository"`
	Installations    []string `json:"installations"`
	Files            []string `json:"files"`
	Changes          int      `json:"changes"`
	GeneratedSecrets []string `json:"generatedSecrets"`
}

// Reader reads a file of a repository as the caller.
type Reader func(ctx context.Context, repository, path string) (string, error)

// Options shape one installation's plan.
type Options struct {
	Installation installations.Installation
	Hub          installations.Installation
	Inputs       map[string]any
	Content      bool
	Read         Reader
}

// Build renders raw through the definition and answers the plan for opts'
// installation. A refusal of the definition is the answer, not an error.
func Build(ctx context.Context, opts Options) Installation {
	p := Installation{Name: opts.Installation.Name, Inputs: opts.Inputs,
		Files: []File{}, Includes: []Include{}, GeneratedSecrets: []GeneratedSecret{}, SuppliedSecrets: []string{},
		DexClients: []DexClient{}, CustomerActions: []CustomerAction{}, Probes: []Probe{}, Diff: map[Change]int{}}
	in, err := agentplatform.Parse(opts.Inputs)
	if err != nil {
		p.Refused = err.Error()
		return p
	}
	res, err := agentplatform.Render(opts.Inputs, in.SuppliedMarkers())
	if err != nil {
		p.Refused = err.Error()
		return p
	}
	p.SuppliedSecrets = in.SuppliedSecretFields()
	p.CustomerActions = customerActions(opts.Installation.Name, in)
	p.Probes = Probes()
	generated := map[string]*GeneratedSecret{}
	for _, repo := range sortedRepositories(res.Files) {
		target := ResolveRepository(string(repo), opts.Installation, opts.Hub)
		paths := make([]string, 0, len(res.Files[repo]))
		for path := range res.Files[repo] {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			f := res.Files[repo][path]
			pf := File{Repository: target, Path: path}
			if opts.Content {
				pf.Content = string(f.Content)
			}
			for _, g := range f.Generated {
				pf.Generated = append(pf.Generated, g.Name)
				gs := generated[g.Name]
				if gs == nil {
					gs = &GeneratedSecret{Name: g.Name, Kind: string(g.Kind), Length: g.Length}
					generated[g.Name] = gs
				}
				gs.Files = append(gs.Files, target+":"+path)
			}
			pf.Change, pf.Error = change(ctx, opts.Read, target, path, string(f.Content))
			p.Diff[pf.Change]++
			p.Files = append(p.Files, pf)
			if strings.HasSuffix(path, "/apps/dex-app/configmap-values.yaml.patch") {
				p.DexClients = DexClients(f.Content)
			}
		}
	}
	for _, inc := range res.Includes {
		p.Includes = append(p.Includes, Include{Repository: ResolveRepository(string(inc.Repository), opts.Installation, opts.Hub), Path: inc.Path, Resource: inc.Resource})
	}
	for _, gs := range generated {
		p.GeneratedSecrets = append(p.GeneratedSecrets, *gs)
	}
	sort.Slice(p.GeneratedSecrets, func(i, j int) bool { return p.GeneratedSecrets[i].Name < p.GeneratedSecrets[j].Name })
	return p
}

// change compares the rendered content with the repository's file, as the caller.
func change(ctx context.Context, read Reader, repo, path, rendered string) (Change, string) {
	current, err := read(ctx, repo, path)
	switch {
	case err == nil && current == rendered:
		return ChangeUnchanged, ""
	case err == nil:
		return ChangeUpdate, ""
	case errors.Is(err, gh.ErrNotFound):
		return ChangeCreate, ""
	default:
		return ChangeUnknown, err.Error()
	}
}

// ResolveRepository maps a repository as the definition names it
// (giantswarm/<customer>-configs, giantswarm/<customer>-management-clusters)
// to the repository the registry has on record for the installation or the
// hub; every other repository (teleport-fleet) is as rendered.
func ResolveRepository(rendered string, inst, hub installations.Installation) string {
	for _, i := range []installations.Installation{inst, hub} {
		if !i.Repositories.Known() {
			continue
		}
		switch rendered {
		case "giantswarm/" + i.Customer + "-configs":
			return i.Repositories.Configs
		case "giantswarm/" + i.Customer + "-management-clusters":
			return i.Repositories.ManagementClusters
		}
	}
	return rendered
}

// rank is the dependency order of repositories: an installation's configs
// before its management-clusters, the hub's pair after the installation's,
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

func sortedRepositories(fs render.Fileset) []render.Repository {
	repos := make([]render.Repository, 0, len(fs))
	for r := range fs {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i] < repos[j] })
	return repos
}

// PullRequests groups the files of every installation into one pull request
// per repository, in dependency order. Files that are unchanged open no
// pull request; a repository whose files are all unchanged is left out.
func PullRequests(plans []Installation, byName map[string]installations.Installation, hub installations.Installation) []PullRequest {
	type acc struct {
		insts     map[string]bool
		files     []string
		changes   int
		generated map[string]bool
		rank      int
	}
	prs := map[string]*acc{}
	for _, p := range plans {
		inst := byName[p.Name]
		for _, f := range p.Files {
			if f.Change == ChangeUnchanged {
				continue
			}
			a := prs[f.Repository]
			if a == nil {
				a = &acc{insts: map[string]bool{}, generated: map[string]bool{}, rank: rank(f.Repository, inst, hub)}
				prs[f.Repository] = a
			}
			if r := rank(f.Repository, inst, hub); r < a.rank {
				a.rank = r
			}
			a.insts[p.Name] = true
			a.files = append(a.files, f.Path)
			a.changes++
			for _, g := range f.Generated {
				a.generated[g] = true
			}
		}
	}
	out := make([]PullRequest, 0, len(prs))
	for repo, a := range prs {
		pr := PullRequest{Repository: repo, Installations: keys(a.insts), Files: a.files, Changes: a.changes, GeneratedSecrets: keys(a.generated), Order: a.rank}
		sort.Strings(pr.Files)
		out = append(out, pr)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].Repository < out[j].Repository
	})
	for i := range out {
		out[i].Order = i + 1
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// customerActions names what the rollout needs from the customer beyond the
// pull requests: the provider-key Secrets of additional model configs, which
// the definition references and never renders.
func customerActions(installation string, in *agentplatform.Input) []CustomerAction {
	out := []CustomerAction{}
	for _, m := range in.Kagent.AdditionalModelConfigs {
		if m.APIKeySecret == "" {
			continue
		}
		out = append(out, CustomerAction{Installation: installation,
			Action: fmt.Sprintf("create the Secret %q (key %q) in namespace kagent with the provider key of model config %q", m.APIKeySecret, m.APIKeySecretKey, m.Name),
			Why:    "the definition references the Secret and renders no value for it; the model config stays unusable until it exists"})
	}
	return out
}

// DexClients reads the clients of the rendered dex patch: the static clients
// by key and the extra static clients by id, each with its redirect URIs.
func DexClients(patch []byte) []DexClient {
	var doc struct {
		OIDC struct {
			StaticClients      yaml.Node `yaml:"staticClients"`
			ExtraStaticClients []struct {
				ID           string   `yaml:"id"`
				Name         string   `yaml:"name"`
				Public       bool     `yaml:"public"`
				RedirectURIs []string `yaml:"redirectURIs"`
				TrustedPeers []string `yaml:"trustedPeers"`
				SecretRef    struct {
					Name string `yaml:"name"`
				} `yaml:"secretRef"`
			} `yaml:"extraStaticClients"`
		} `yaml:"oidc"`
	}
	if err := yaml.Unmarshal(patch, &doc); err != nil {
		return nil
	}
	var out []DexClient
	for i := 0; i+1 < len(doc.OIDC.StaticClients.Content); i += 2 {
		var body struct {
			ClientSecretRef struct {
				Name string `yaml:"name"`
			} `yaml:"clientSecretRef"`
			RedirectURIs []string `yaml:"redirectURIs"`
			TrustedPeers []string `yaml:"trustedPeers"`
		}
		_ = doc.OIDC.StaticClients.Content[i+1].Decode(&body)
		out = append(out, DexClient{ID: doc.OIDC.StaticClients.Content[i].Value, Secret: body.ClientSecretRef.Name, RedirectURIs: body.RedirectURIs, TrustedPeers: body.TrustedPeers})
	}
	for _, c := range doc.OIDC.ExtraStaticClients {
		out = append(out, DexClient{ID: c.ID, Name: c.Name, Public: c.Public, Secret: c.SecretRef.Name, RedirectURIs: c.RedirectURIs, TrustedPeers: c.TrustedPeers})
	}
	return out
}

// Probes are the live dimensions of the definition's features: what the
// verify checks against the running installation.
func Probes() []Probe {
	feats, err := definitions.Features(installations.AgentPlatform)
	if err != nil {
		return nil
	}
	var out []Probe
	for _, f := range feats {
		for _, d := range f.Dimensions {
			if d.Kind == definitions.KindLive {
				out = append(out, Probe{ID: d.ID, Feature: f.ID, Key: d.Key})
			}
		}
	}
	return out
}
