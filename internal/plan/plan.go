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
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Change is what a rendered file is against the repository now.
type Change string

// The changes.
const (
	ChangeCreate    Change = "create"
	ChangeUpdate    Change = "update"
	ChangeUnchanged Change = "unchanged"
	// ChangeUnknown: the current file could not be read as the caller; for
	// a kustomization the includes land in, also one that is absent or no
	// mapping.
	ChangeUnknown Change = "unknown"
)

// File is one rendered file in the plan.
type File struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Change     Change `json:"change"`
	// Content is the rendered file, plaintext with GENERATED(<name>) and
	// SUPPLIED(<field>) markers where the commit step puts values — or, for
	// a file other owners write into (a kustomization the includes land in
	// or the platform writes, the dex-app configmap patch, the platform
	// patch's audience lists, teleport-fleet's tunnelport values), the file
	// as the commit step writes it, with their part kept; omitted when the
	// caller asked for paths only.
	Content string `json:"content,omitempty"`
	// Current is the file as it is on record, read as the caller: the side
	// Content is against, for a diff of the two. Omitted when the caller
	// asked for paths only or the record lacks the file. A comparison shows
	// a file SOPS encrypted with its block dropped and the leaves it
	// encrypts redacted.
	Current string `json:"current,omitempty"`
	// Generated names the values the commit step generates into this file.
	Generated []string `json:"generated,omitempty"`
	// Kept is the part of the installation's current file that is not the
	// platform's, kept in the file as the plan writes it.
	Kept []Kept `json:"kept,omitempty"`
	// Unseen are the literals of the render that this file, kept unchanged
	// on record, holds encrypted — a client id beside a generated secret: the
	// comparison decrypts nothing, so the value on record stands whether or
	// not it is the render's, and a person reads here what the render
	// assumes it to be.
	Unseen []Unseen `json:"unseen,omitempty"`
	// Creates names the objects the file brings onto the installation, as
	// "<kind>/<name>": a Secret its manifests declare, a Teleport provision
	// token its tunnelport values list — none the file on record carries
	// already. References names the objects the file refers to the same
	// way: the Secret a secretRef, an existingSecret or a secretName names,
	// the ProvisionToken a tokenName names. The pull requests merge in the
	// order they imply: the one that creates before the one that references.
	Creates    []string `json:"creates,omitempty"`
	References []string `json:"references,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// Kept is one entry of a file with several owners that another owner carries;
// it stays. In a kustomization.yaml List is resources or components and Entry
// the entry, kept after the platform's. In the dex-app configmap patch List is
// the mapping the key is kept in (empty for the file's top level, oidc,
// oidc.staticClients) and Entry the key — or List is oidc.extraStaticClients
// and Entry the id of a client no definition declares. In the audience lists
// (the dex patch's ListTrustedPeers, the platform patch's
// ListTrustedAudiences, ListExtraAudience and ListEdgeAudiences) List is the
// list and Entry an id the installation trusts that the definition does not
// render, kept after the definition's in that list alone. In teleport-fleet's
// tunnelport values List is tunnelport.consumers, tunnelport.trustBundle.tokens
// or tunnelport.tunnels and Entry the consumer or the entry's name — or List is
// tunnelport.tunnels[<tunnel>].tokens and Entry another hub's token of a tunnel
// this hub shares — kept in place: there the platform's entries are the ones
// edited in.
type Kept struct {
	List  string `json:"list"`
	Entry string `json:"entry"`
}

// kustomizationFile is the base name of the files other owners add entries to.
const kustomizationFile = "kustomization.yaml"

// dexPatchFile ends the path of an installation's dex-app configmap patch:
// the one file the two definitions and the installation's own Dex settings
// share.
const dexPatchFile = "/apps/dex-app/configmap-values.yaml.patch"

// shared is how a file with several owners is written: edit merges the
// definition's render with the file on record and names what it kept. theirs
// marks a file the definition edits entries into and never writes whole
// (teleport-fleet's tunnelport values): absent on record, it is not created.
type shared struct {
	edit   func(rendered, current []byte) ([]byte, []Kept, error)
	theirs bool
}

// sharedFile is the shared file at path — a kustomization's lists, the dex
// patch's keys, clients and trusted peers, the platform patch's audience
// lists, the tunnelport values' entries, the installation's record — or nil
// for a file the definition owns whole.
func sharedFile(path string) *shared {
	switch {
	case filepath.Base(path) == kustomizationFile:
		return &shared{edit: keep}
	case strings.HasSuffix(path, dexPatchFile):
		return &shared{edit: keepDexPatch}
	case strings.HasSuffix(path, platformPatchFile):
		return &shared{edit: keepAudiences}
	case path == tunnelportValuesFile:
		return &shared{edit: keepTunnelportValues, theirs: true}
	case strings.HasPrefix(path, "installations/") && filepath.Base(path) == render.RecordFile:
		return &shared{edit: keepRecord, theirs: true}
	}
	return nil
}

// Shared says whether path is a file with several owners, written as the plan
// writes it: its content in the plan is never the render's bytes alone.
func Shared(path string) bool { return sharedFile(path) != nil }

// GeneratedSecret is one value the commit step generates, by name only.
type GeneratedSecret struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"`
	Length int      `json:"length"`
	Files  []string `json:"files"`
	// FrozenIn are the Files that exist encrypted on record: the value they
	// hold cannot be read back (the manager decrypts nothing), so no other
	// file can share it — the commit either keeps them or rotates.
	FrozenIn []string `json:"frozenIn,omitempty"`
	// Kept: every file of the name is on record as the render has it outside
	// the values, so the value on record stands and no file is written.
	Kept bool `json:"kept,omitempty"`
	// Rotates: a file of the name has to be written — ForcedBy names it: a
	// file to create, an existing file whose plaintext skeleton the render
	// changes, or a file rewritten for another rotating name — so the commit
	// draws a new value and writes it into every one of Files, the frozen
	// ones rewritten; both sides roll on the installation.
	Rotates  bool   `json:"rotates,omitempty"`
	ForcedBy string `json:"forcedBy,omitempty"`
	// Refusal is why a commit of this plan is refused before any write: the
	// name is frozen in a file the definition does not own whole.
	Refusal string `json:"refusal,omitempty"`
}

// DexClient is one Dex client the rendered dex patch touches. An extra static
// client is declared whole: id, name, redirect URIs and the reference to its
// Secret. A built-in client of the dex-app chart (muster, mcpKubernetes) has
// its id and redirect URI rendered by the fleet's shared template; the patch
// adds only the Secret reference, so Client names the chart's key and ID is
// filled where the installation's record knows the client id.
type DexClient struct {
	ID           string   `json:"id,omitempty"`
	Client       string   `json:"client,omitempty"`
	Name         string   `json:"name,omitempty"`
	Public       bool     `json:"public,omitempty"`
	SecretRef    string   `json:"secretRef,omitempty"`
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

// The lists of a kustomization.yaml an Include lands in.
const (
	ListResources  = "resources"
	ListComponents = "components"
)

// Include is one entry the commit step lists in a kustomization.yaml other
// owners write: Resource under List (resources, or components for a
// kustomize Component). Change is the entry's: unchanged when it is listed,
// update when it is to be added, unknown when the kustomization could not
// be read or is absent. The kustomization itself is among the Files, as
// the commit step writes it.
type Include struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	List       string `json:"list"`
	Resource   string `json:"resource"`
	Change     Change `json:"change"`
	Error      string `json:"error,omitempty"`
}

// Installation is the dry run of one installation.
type Installation struct {
	Name string `json:"name"`
	// State is the capability's state read from the repositories now.
	State installations.State `json:"state"`
	// Inputs are the effective inputs: the record, the typed inputs over it.
	Inputs map[string]any `json:"inputs"`
	// MissingInputs names, by field, the required person inputs no layer
	// of the document holds: rendered as Missing markers, compared as not
	// checked, refused by a commit.
	MissingInputs []string `json:"missingInputs,omitempty"`
	// Refused is the definition's refusal of these inputs (an unknown key, a
	// policy, an input this version does not render), the sentence a person
	// reads; the rest is empty then.
	Refused string `json:"refused,omitempty"`
	// CommitRefused says why a commit of this dry run would be refused (the
	// definition's refusal, the same sentence as Refused; a choice not on
	// record; the record's dex-app too old for a referenced Dex client; a
	// generated value frozen where it cannot rotate); empty when a commit
	// could go ahead.
	CommitRefused    string            `json:"commitRefused,omitempty"`
	Files            []File            `json:"files"`
	Includes         []Include         `json:"includes"`
	GeneratedSecrets []GeneratedSecret `json:"generatedSecrets"`
	// SuppliedSecrets names, by field, the values the person supplies at
	// commit: the secrets, and an input that lives only in an encrypted file
	// (the portal's GitHub App id), which no read-back recovers — each in a
	// file the commit writes (created, or rewritten), or in no file the plan
	// knows.
	SuppliedSecrets []string `json:"suppliedSecrets"`
	// SuppliedOnRecord names, by field, the supplied values whose files all
	// stand on record unchanged: a secret file on record is never generated
	// again, so the value on record stands, the commit renders the field's
	// marker, writes none of its files and asks for no value.
	SuppliedOnRecord []string         `json:"suppliedOnRecord,omitempty"`
	DexClients       []DexClient      `json:"dexClients"`
	CustomerActions  []CustomerAction `json:"customerActions"`
	Probes           []Probe          `json:"probes"`
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
	// After names the pull requests this one merges after — their files
	// create an object this one's files reference — with the objects; empty
	// where the repositories' kind alone places it.
	After []Dependency `json:"after,omitempty"`
}

// Reader reads a file of a repository as the caller. Build calls it from
// several goroutines at once: every file a plan compares against is fetched
// in one phase.
type Reader func(ctx context.Context, repository, path string) (string, error)

// ReadConcurrency bounds the files of one plan in flight at once. A
// comparison reads every rendered file's current content and the
// kustomizations its includes land in — some thirty GitHub round trips —
// so reading them one after another cost the phase thirty times one round
// trip; fetched at once it costs about one. The GitHub client bounds the
// whole call further.
const ReadConcurrency = 16

// fileRef names a file of a repository the plan reads.
type fileRef struct{ repository, path string }

func (f fileRef) key() string { return f.repository + ":" + f.path }

// fetched is what the plan read as the caller, by fileRef key: every file
// the plan compares against, read at once before the files are walked.
type fetched struct {
	mu  sync.Mutex
	got map[string]readResult
}

type readResult struct {
	content string
	err     error
}

// fetch reads every file of files through read at once, ReadConcurrency in
// flight at most, each once whatever the duplicates among files. A context
// over before a file's turn is that file's error.
func fetch(ctx context.Context, read Reader, files []fileRef) *fetched {
	f := &fetched{got: make(map[string]readResult, len(files))}
	var distinct []fileRef
	for _, file := range files {
		if _, seen := f.got[file.key()]; !seen {
			f.got[file.key()] = readResult{}
			distinct = append(distinct, file)
		}
	}
	slots := make(chan struct{}, ReadConcurrency)
	var wg sync.WaitGroup
	for _, file := range distinct {
		wg.Go(func() {
			r := readResult{err: ctx.Err()}
			if r.err == nil {
				select {
				case slots <- struct{}{}:
					r.content, r.err = read(ctx, file.repository, file.path)
					<-slots
				case <-ctx.Done():
					r.err = ctx.Err()
				}
			}
			f.mu.Lock()
			f.got[file.key()] = r
			f.mu.Unlock()
		})
	}
	wg.Wait()
	return f
}

// read answers a fetched file as a Reader. A file the plan did not name
// before fetching is a defect of Build, named.
func (f *fetched) read(_ context.Context, repository, path string) (string, error) {
	got, ok := f.got[fileRef{repository, path}.key()]
	if !ok {
		return "", fmt.Errorf("%s:%s was not among the plan's files when they were fetched", repository, path)
	}
	return got.content, got.err
}

// Options shape one installation's plan.
type Options struct {
	// Definition is the capability rendered, from the registry.
	Definition   installations.Capability
	Installation installations.Installation
	Hub          installations.Installation
	Inputs       map[string]any
	Content      bool
	Read         Reader
}

// Build renders the inputs through opts' definition and answers the plan for
// opts' installation. A refusal of the definition is the answer, not an error.
func Build(ctx context.Context, opts Options) Installation {
	p := Installation{Name: opts.Installation.Name, Inputs: opts.Inputs,
		Files: []File{}, Includes: []Include{}, GeneratedSecrets: []GeneratedSecret{}, SuppliedSecrets: []string{},
		DexClients: []DexClient{}, CustomerActions: []CustomerAction{}, Probes: []Probe{}, Diff: map[Change]int{}}
	in, err := opts.Definition.Parse(opts.Inputs)
	if err != nil {
		p.Refused = render.Reason(err)
		return p
	}
	p.MissingInputs = in.MissingInputs()
	p.Inputs = withSelected(opts.Inputs, in.Selected())
	res, err := opts.Definition.Render(opts.Inputs, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		p.Refused = render.Reason(err)
		return p
	}
	supplied := in.SuppliedSecretFields()
	suppliedIn := map[string][]int{} // a supplied field → the indexes into p.Files whose render holds its marker
	p.CustomerActions = customerActions(opts.Installation.Name, in)
	p.Probes = Probes(opts.Definition.Name)
	// Every file the plan compares against is read now, at once: the
	// rendered files' current content and the kustomizations the includes
	// land in. The walk below reads from what was fetched.
	var files []fileRef
	for repo, byPath := range res.Files {
		target := ResolveRepository(string(repo), opts.Installation, opts.Hub)
		for path := range byPath {
			files = append(files, fileRef{target, path})
		}
	}
	for _, inc := range res.Includes {
		files = append(files, fileRef{ResolveRepository(string(inc.Repository), opts.Installation, opts.Hub), inc.Path})
	}
	opts.Read = fetch(ctx, opts.Read, files).read
	generated := map[string]*GeneratedSecret{}
	var holders []holder
	held := map[string]int{} // a holder's file → its index in p.Files
	for _, repo := range SortedRepositories(res.Files) {
		target := ResolveRepository(string(repo), opts.Installation, opts.Hub)
		paths := make([]string, 0, len(res.Files[repo]))
		for path := range res.Files[repo] {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			f := res.Files[repo][path]
			pf := File{Repository: target, Path: path}
			h := holder{file: target + ":" + path, shared: Shared(path)}
			for _, g := range f.Generated {
				pf.Generated = append(pf.Generated, g.Name)
				gs := generated[g.Name]
				if gs == nil {
					gs = &GeneratedSecret{Name: g.Name, Kind: string(g.Kind), Length: g.Length}
					generated[g.Name] = gs
				}
				if !slices.Contains(gs.Files, h.file) {
					gs.Files = append(gs.Files, h.file)
				}
				if g.Half == render.Public {
					h.public = append(h.public, g.Name)
				} else {
					h.secret = append(h.secret, g.Name)
				}
			}
			content := string(f.Content)
			current, err := opts.Read(ctx, target, path)
			if s := sharedFile(path); s != nil {
				switch {
				case err == nil:
					edited, kept, kerr := s.edit(f.Content, []byte(current))
					if kerr != nil {
						err = fmt.Errorf("%s is on record but takes no entry: %w", path, kerr)
					} else {
						content, pf.Kept = string(edited), kept
					}
				case s.theirs && errors.Is(err, gh.ErrNotFound):
					err = errors.New("absent: a file other owners write is not created here")
				}
			}
			if opts.Content {
				pf.Content = content
				if err == nil {
					pf.Current = current
				}
			}
			pf.Change, pf.Error, pf.Unseen = change(current, err, content)
			pf.Creates, pf.References = introduced(content, current, err == nil), references(content)
			p.Diff[pf.Change]++
			if len(f.Generated) > 0 {
				h.change = pf.Change
				holders = append(holders, h)
				held[h.file] = len(p.Files)
			}
			for _, field := range supplied {
				if strings.Contains(string(f.Content), render.Supplied(field)) {
					suppliedIn[field] = append(suppliedIn[field], len(p.Files))
				}
			}
			p.Files = append(p.Files, pf)
			if strings.HasSuffix(path, dexPatchFile) {
				p.DexClients = DexClients(f.Content, in)
			}
		}
	}
	// A file kept as it is that holds a rotating name is rewritten with the
	// new value: an update after all.
	for file := range frozen(generated, holders) {
		if pf := &p.Files[held[file]]; pf.Change == ChangeUnchanged {
			pf.Change = ChangeUpdate
			p.Diff[ChangeUnchanged]--
			p.Diff[ChangeUpdate]++
		}
	}
	p.SuppliedSecrets, p.SuppliedOnRecord = splitSupplied(supplied, suppliedIn, p.Files)
	p.includes(ctx, opts, res.Includes)
	sort.SliceStable(p.Files, func(i, j int) bool {
		if p.Files[i].Repository != p.Files[j].Repository {
			return p.Files[i].Repository < p.Files[j].Repository
		}
		return p.Files[i].Path < p.Files[j].Path
	})
	for _, gs := range generated {
		p.GeneratedSecrets = append(p.GeneratedSecrets, *gs)
	}
	sort.Slice(p.GeneratedSecrets, func(i, j int) bool { return p.GeneratedSecrets[i].Name < p.GeneratedSecrets[j].Name })
	return p
}

// splitSupplied divides the supplied fields into the ones the commit needs a
// value for — the field's marker sits in a file the plan writes (created, or
// rewritten: its skeleton changed or a rotation rewrites it), or in no file
// the plan knows — and the ones on record: every file that holds the marker
// stands unchanged, a secret file on record is never generated again, so the
// value on record stands and the commit renders the marker and writes none
// of its files. Both keep the definition's order.
func splitSupplied(fields []string, in map[string][]int, files []File) (needed, onRecord []string) {
	needed = []string{}
	for _, field := range fields {
		indexes, known := in[field]
		write := !known
		for _, i := range indexes {
			if files[i].Change != ChangeUnchanged {
				write = true
				break
			}
		}
		if write {
			needed = append(needed, field)
		} else {
			onRecord = append(onRecord, field)
		}
	}
	return needed, onRecord
}

// withSelected is inputs with the definition's selections laid over — the
// effective inputs the plan answers (a fresh enable's chart line) — as a copy
// along the edited path, inputs untouched. Nothing selected answers inputs.
func withSelected(inputs, selected map[string]any) map[string]any {
	if len(selected) == 0 {
		return inputs
	}
	out := make(map[string]any, len(inputs))
	for k, v := range inputs {
		out[k] = v
	}
	for k, v := range selected {
		over, isMap := v.(map[string]any)
		base, baseIsMap := out[k].(map[string]any)
		if isMap && baseIsMap {
			out[k] = withSelected(base, over)
			continue
		}
		out[k] = v
	}
	return out
}

// change is what rendered is against the repository's file, read as the
// caller: current with err. A file on record that differs from the render
// only in the values the commit fills in — encrypted on record, or a plain
// file's public half of a key pair — is unchanged: the values stand, and the
// literals the render puts under an encrypted field are named as unseen. So
// is a kustomization that differs only in how its patch texts spell their
// YAML (samePatches): writing it would change nothing.
func change(current string, err error, rendered string) (Change, string, []Unseen) {
	switch {
	case err == nil && current == rendered:
		return ChangeUnchanged, "", nil
	case err == nil && sameSkeleton(rendered, current):
		return ChangeUnchanged, "", unseen(rendered, current)
	case err == nil && samePatches(rendered, current):
		return ChangeUnchanged, "", nil
	case err == nil:
		return ChangeUpdate, "", nil
	case errors.Is(err, gh.ErrNotFound):
		return ChangeCreate, "", nil
	default:
		return ChangeUnknown, err.Error(), nil
	}
}

// includes records every entry and files each kustomization they land in
// among the Files, read once as the caller and edited with its entries in
// order.
func (p *Installation) includes(ctx context.Context, opts Options, incs []render.Include) {
	byFile := map[string][]int{} // "<repository>:<path>" → indexes into p.Includes
	var files []string
	for _, inc := range incs {
		entry := Include{Repository: ResolveRepository(string(inc.Repository), opts.Installation, opts.Hub), Path: inc.Path, List: ListResources, Resource: inc.Resource}
		if inc.Component {
			entry.List = ListComponents
		}
		key := entry.Repository + ":" + entry.Path
		if _, seen := byFile[key]; !seen {
			files = append(files, key)
		}
		byFile[key] = append(byFile[key], len(p.Includes))
		p.Includes = append(p.Includes, entry)
	}
	for _, key := range files {
		entries := byFile[key]
		f := p.kustomization(ctx, opts, p.Includes[entries[0]].Repository, p.Includes[entries[0]].Path, entries)
		p.Diff[f.Change]++
		p.Files = append(p.Files, f)
	}
}

// kustomization reads repository's kustomization at path as the caller and
// lists the entries (indexes into Includes) in it: update when one was
// added, unchanged when every one was listed, unknown when the file could
// not be read — or is absent, because a kustomization other owners write is
// never created here — and every entry takes the file's change then.
func (p *Installation) kustomization(ctx context.Context, opts Options, repository, path string, entries []int) File {
	f := File{Repository: repository, Path: path, Change: ChangeUnchanged}
	current, err := opts.Read(ctx, repository, path)
	switch {
	case errors.Is(err, gh.ErrNotFound):
		f.Change, f.Error = ChangeUnknown, "absent: a kustomization other owners write is not created here"
	case err != nil:
		f.Change, f.Error = ChangeUnknown, err.Error()
	}
	content := []byte(current)
	for _, i := range entries {
		if f.Change == ChangeUnknown {
			break
		}
		edited, changed, err := listEntry(content, p.Includes[i].List, p.Includes[i].Resource)
		switch {
		case err != nil:
			f.Change, f.Error = ChangeUnknown, err.Error()
		case changed:
			content, f.Change = edited, ChangeUpdate
			p.Includes[i].Change = ChangeUpdate
		default:
			p.Includes[i].Change = ChangeUnchanged
		}
	}
	if f.Change == ChangeUnknown {
		for _, i := range entries {
			p.Includes[i].Change, p.Includes[i].Error = ChangeUnknown, f.Error
		}
		return f
	}
	if opts.Content {
		f.Content, f.Current = string(content), current
	}
	return f
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

// SortedRepositories are the repositories of fs in a stable order, by name:
// the order the files are walked in, not the pull requests' (PullRequests).
func SortedRepositories(fs render.Fileset) []render.Repository {
	repos := make([]render.Repository, 0, len(fs))
	for r := range fs {
		repos = append(repos, r)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i] < repos[j] })
	return repos
}

// PullRequests groups the files of every installation into one pull request
// per repository, in dependency order: a pull request whose files create an
// object another one's files reference merges before it, the rest by the
// repositories' kind (order). Files that are unchanged open no pull request;
// a repository whose files are all unchanged is left out.
func PullRequests(plans []Installation, byName map[string]installations.Installation, hub installations.Installation) []PullRequest {
	type acc struct {
		insts               map[string]bool
		files               []string
		changes             int
		generated           map[string]bool
		creates, references map[string]bool
		rank                int
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
				a = &acc{insts: map[string]bool{}, generated: map[string]bool{}, creates: map[string]bool{}, references: map[string]bool{}, rank: rank(f.Repository, inst, hub)}
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
			for _, o := range f.Creates {
				a.creates[o] = true
			}
			for _, o := range f.References {
				a.references[o] = true
			}
		}
	}
	out := make([]PullRequest, 0, len(prs))
	ranks, creates, references := map[string]int{}, map[string]map[string]bool{}, map[string]map[string]bool{}
	for repo, a := range prs {
		pr := PullRequest{Repository: repo, Installations: keys(a.insts), Files: a.files, Changes: a.changes, GeneratedSecrets: keys(a.generated)}
		sort.Strings(pr.Files)
		out = append(out, pr)
		ranks[repo], creates[repo], references[repo] = a.rank, a.creates, a.references
	}
	return order(out, ranks, creates, references)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// customerActions are the definition's customer actions, named for the
// installation.
func customerActions(installation string, in render.Input) []CustomerAction {
	out := []CustomerAction{}
	for _, a := range in.CustomerActions() {
		out = append(out, CustomerAction{Installation: installation, Action: a.Action, Why: a.Why})
	}
	return out
}

// DexClients reads the clients of the rendered dex patch: the built-in
// clients by the chart's key, with the id the input knows, and the extra
// static clients as declared.
func DexClients(patch []byte, in render.Input) []DexClient {
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
		key := doc.OIDC.StaticClients.Content[i].Value
		out = append(out, DexClient{ID: in.BuiltInDexClientID(key), Client: key, SecretRef: body.ClientSecretRef.Name, RedirectURIs: body.RedirectURIs, TrustedPeers: body.TrustedPeers})
	}
	for _, c := range doc.OIDC.ExtraStaticClients {
		out = append(out, DexClient{ID: c.ID, Name: c.Name, Public: c.Public, SecretRef: c.SecretRef.Name, RedirectURIs: c.RedirectURIs, TrustedPeers: c.TrustedPeers})
	}
	return out
}

// Probes are the live dimensions of the named definition's features: what
// the verify checks against the running installation.
func Probes(capability string) []Probe {
	feats, err := definitions.Features(capability)
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
