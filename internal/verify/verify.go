// Package verify compares one installation against a capability's definition
// and answers the result grouped into the definition's features, each rolled
// up to one mark and expandable to its dimensions. Compare is verify_capability:
// the owning repositories' files against the render from the inputs on record
// and the definition's anonymous HTTP probes, as the person's GitHub token
// reads. CompareLive is verify_installation: the definition's probes of the
// running installation, read as the person through muster (live.go). Merge
// joins the two into the one result a person reads.
package verify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// Mark is what a dimension, and a feature rolled up from its dimensions, shows.
type Mark string

// The marks. A feature is drifted when any dimension is, differs by input
// when any does and none drifted, planned when its only differences are
// planned changes (the keys the capability's removals name), as defined
// when at least one dimension was checked and none differs, not checked
// otherwise.
const (
	AsDefined      Mark = "as defined"
	Planned        Mark = "planned"
	DiffersByInput Mark = "differs by input"
	Drifted        Mark = "drifted"
	NotChecked     Mark = "not checked"
)

// severity orders the marks the way a feature rolls up from its dimensions.
var severity = []Mark{Drifted, DiffersByInput, Planned, AsDefined}

// The reasons a dimension is not checked. A probe's own — no Dex client to
// run for, a target unreachable from the manager — are next to probe in probes.go.
const (
	ReasonAuthority  = "needs your session on the installation"
	ReasonNoRender   = "nothing rendered to compare against: the inputs are missing or refused"
	ReasonUnreadable = "a file of the dimension could not be read as the caller"
	ReasonNoFile     = "the definition renders no file of this kind for the inputs on record"
)

// Difference is one place a repository file, or a live object, is off the
// render: at path (a YAML path inside file; empty when the whole file
// differs). File names the repository file; Object the live object of a live
// dimension (resource namespace/name). Input names the input of the
// definition that drives the path — the file expresses another input than
// the one on record; empty, the path is drift. Planned is the reason of the
// capability's removal that names the path — a key the fleet still carries
// that the definition does not render: a planned change, not drift.
// Rendered and Current are the values on each side — Redacted for a file
// SOPS encrypted on record.
type Difference struct {
	File     string `json:"file,omitempty"`
	Object   string `json:"object,omitempty"`
	Path     string `json:"path,omitempty"`
	Input    string `json:"input,omitempty"`
	Planned  string `json:"planned,omitempty"`
	Rendered string `json:"rendered,omitempty"`
	Current  string `json:"current,omitempty"`
}

// Dimension is one observed aspect of a feature with its mark.
type Dimension struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Mark   Mark   `json:"mark"`
	Reason string `json:"reason,omitempty"`
	// Files are the repository files the dimension was compared in.
	Files       []string     `json:"files,omitempty"`
	Differences []Difference `json:"differences,omitempty"`
	Probe       *ProbeResult `json:"probe,omitempty"`
	// Live is what a live dimension's probes answered (CompareLive).
	Live *LiveResult `json:"live,omitempty"`
}

// Feature is one feature of the definition, rolled up.
type Feature struct {
	ID         string       `json:"id"`
	Title      string       `json:"title"`
	Mark       Mark         `json:"mark"`
	Marks      map[Mark]int `json:"marks"`
	Dimensions []Dimension  `json:"dimensions"`
}

// The sources of the inputs a comparison renders from. The record is the
// schema's defaults under the installation's facts; the read-back is what
// the definition read from the files on record; typed is the person's
// inputs over both. A live verify renders from the inputs it is given, else
// an action's on record, else none.
const (
	SourceRecord   = "record"
	SourceReadBack = "read-back"
	SourceTyped    = "typed"
	SourceNone     = "none"
)

// Source names the layers the inputs came from: record, then read-back and
// typed when they contributed.
func Source(readBack, typed bool) string {
	s := SourceRecord
	if readBack {
		s += " + " + SourceReadBack
	}
	if typed {
		s += " + " + SourceTyped
	}
	return s
}

// Inputs are the inputs the render is compared from.
type Inputs struct {
	// Source names the layers (Source), an action, or none.
	Source string         `json:"source"`
	Values map[string]any `json:"values,omitempty"`
	// ReadBack is what the definition read back from the files on record,
	// by dotted input key; never a secret value.
	ReadBack map[string]any `json:"readBack,omitempty"`
}

// Result is the verify of one installation × capability.
type Result struct {
	Caller       string `json:"caller"`
	Installation string `json:"installation"`
	Capability   string `json:"capability"`
	Hub          string `json:"hub"`
	// State is the capability's state this result feeds: drifted when any
	// dimension is, else the state read from the repositories.
	State  installations.State `json:"state"`
	Inputs Inputs              `json:"inputs"`
	// Refused is the definition's refusal of the inputs on record, when it
	// refuses them; the file dimensions are not checked then.
	Refused  string       `json:"refused,omitempty"`
	Features []Feature    `json:"features"`
	Summary  map[Mark]int `json:"summary"`
	// LiveCaller is who the live reads ran as, in a merged result.
	LiveCaller string `json:"liveCaller,omitempty"`
	// The plan's view, from the one build the comparison ran: what a commit
	// would write for this installation (the dry run's entry, regrouped).
	OptIn *installations.OptIn `json:"optIn,omitempty"`
	// CommitRefused says why a commit of this plan would be refused.
	CommitRefused    string                 `json:"commitRefused,omitempty"`
	Files            []plan.File            `json:"files"`
	Includes         []plan.Include         `json:"includes"`
	Diff             map[plan.Change]int    `json:"diff"`
	PullRequests     []plan.PullRequest     `json:"pullRequests"`
	GeneratedSecrets []plan.GeneratedSecret `json:"generatedSecrets"`
	SuppliedSecrets  []string               `json:"suppliedSecrets"`
	DexClients       []plan.DexClient       `json:"dexClients"`
	CustomerActions  []plan.CustomerAction  `json:"customerActions"`
	Probes           []plan.Probe           `json:"probes"`
}

// view takes the plan's view into the result; the files' content only when
// asked for.
func (r *Result) view(p plan.Installation, content bool) {
	r.Files, r.Includes, r.Diff = p.Files, p.Includes, p.Diff
	r.GeneratedSecrets, r.SuppliedSecrets, r.DexClients, r.CustomerActions, r.Probes = p.GeneratedSecrets, p.SuppliedSecrets, p.DexClients, p.CustomerActions, p.Probes
	if !content {
		r.Files = make([]plan.File, len(p.Files))
		for i, f := range p.Files {
			f.Content = ""
			r.Files[i] = f
		}
	}
}

// Plan is the result regrouped as the dry run's entry: what a commit would
// write, without the marks.
func (r Result) Plan() plan.Installation {
	return plan.Installation{Name: r.Installation, State: r.State, OptIn: r.OptIn, Inputs: r.Inputs.Values, Refused: r.Refused, CommitRefused: r.CommitRefused,
		Files: r.Files, Includes: r.Includes, Diff: r.Diff, GeneratedSecrets: r.GeneratedSecrets, SuppliedSecrets: r.SuppliedSecrets,
		DexClients: r.DexClients, CustomerActions: r.CustomerActions, Probes: r.Probes}
}

// Options shape one verify.
type Options struct {
	// Definition is the capability verified, from the registry.
	Definition   installations.Capability
	Installation installations.Installation
	Hub          installations.Installation
	State        installations.State
	Inputs       Inputs
	Read         plan.Reader
	// Content keeps the rendered content on the result's files.
	Content bool
	// Probes sends the anonymous probes; nil is a client that does not follow redirects.
	Probes *http.Client
}

// Compare answers the verify of opts' installation.
func Compare(ctx context.Context, opts Options) Result {
	r := Result{Installation: opts.Installation.Name, Capability: opts.Definition.Name, Hub: opts.Hub.Name,
		State: opts.State, Inputs: opts.Inputs, Features: []Feature{}, Summary: map[Mark]int{},
		Files: []plan.File{}, Includes: []plan.Include{}, Diff: map[plan.Change]int{}, PullRequests: []plan.PullRequest{},
		GeneratedSecrets: []plan.GeneratedSecret{}, SuppliedSecrets: []string{}, DexClients: []plan.DexClient{}, CustomerActions: []plan.CustomerAction{}, Probes: []plan.Probe{}}
	feats, err := definitions.Features(opts.Definition.Name)
	if err != nil {
		r.Refused = err.Error()
		return r
	}
	probes, err := definitions.Probes(opts.Definition.Name)
	if err != nil {
		r.Refused = err.Error()
		return r
	}
	rms, err := definitions.Removals(opts.Definition.Name)
	if err != nil {
		r.Refused = err.Error()
		return r
	}
	// Without inputs nothing is rendered: the file dimensions read not
	// checked, the anonymous probes still run.
	var c *comparison
	if opts.Inputs.Values != nil {
		var p plan.Installation
		if c, p, err = compare(ctx, opts, readRemovals(rms)); err != nil {
			r.Refused = err.Error()
			c = nil
		}
		r.view(p, opts.Content)
	}
	dims := assign(c, feats, r.Refused)
	for _, fd := range feats {
		f := Feature{ID: fd.ID, Title: fd.Title, Marks: map[Mark]int{}, Dimensions: []Dimension{}}
		for _, d := range fd.Dimensions {
			f.Dimensions = append(f.Dimensions, *dims[d.ID])
		}
		for _, p := range probes {
			if p.Feature == fd.ID {
				var clients []plan.DexClient
				if c != nil {
					clients = c.dexClients
				}
				f.Dimensions = append(f.Dimensions, probe(ctx, opts.Probes, opts.Installation.BaseDomain, clients, c != nil, p))
			}
		}
		for _, d := range f.Dimensions {
			f.Marks[d.Mark]++
			r.Summary[d.Mark]++
		}
		f.Mark = rollUp(f.Dimensions)
		r.Features = append(r.Features, f)
	}
	// Drifted is an enabled installation off its definition; one not enabled
	// differs everywhere and keeps saying so.
	if r.Summary[Drifted] > 0 && r.State == installations.StateEnabled {
		r.State = installations.StateDrifted
	}
	return r
}

// rollUp is a feature's mark from its dimensions'.
func rollUp(dims []Dimension) Mark {
	seen := map[Mark]bool{}
	for _, d := range dims {
		seen[d.Mark] = true
	}
	for _, m := range severity {
		if seen[m] {
			return m
		}
	}
	return NotChecked
}

// fileKey is "repository:path" — how a file is named in the result.
func fileKey(repo, path string) string { return repo + ":" + path }

// comparison is the plan from the inputs on record against the repositories:
// what the plan would write, as leaf differences.
type comparison struct {
	// files by key: the kind, the differences (Input filled), or why unreadable.
	files      map[string]*fileDiff
	dexClients []plan.DexClient
}

type fileDiff struct {
	key, path, kind string
	unreadable      string
	diffs           []Difference
}

// Redacted stands for a value of an encrypted file in a difference: the
// path differs, the values are not shown.
const Redacted = "<encrypted>"

// compare builds the plan from the inputs — the render, read against the
// repositories as the caller, every file once — and names each difference
// the plan would write an input or drift. The plan's outcome per file is
// the comparison: a file it leaves unchanged (an encrypted file whose
// plaintext skeleton is the render's, a shared file that differs only in
// the entries other owners keep or their order) has no difference; a file
// it creates or updates differs at the leaves of the file as the plan
// writes it that are off the record, each under a key the removals name
// marked planned. The plan is the second answer, the definition's refusal
// the error.
func compare(ctx context.Context, opts Options, rms removals) (*comparison, plan.Installation, error) {
	rs := &reads{read: opts.Read, got: map[string]read{}}
	p, base, err := build(ctx, opts, opts.Inputs.Values, rs.reader)
	if err != nil {
		return nil, p, err
	}
	driven := drivenPaths(opts.Inputs.Values, base, func(values map[string]any) (map[string]map[string]string, error) {
		_, other, err := build(ctx, opts, values, rs.recorded)
		return other, err
	})
	c := &comparison{files: map[string]*fileDiff{}, dexClients: p.DexClients}
	for _, f := range rendered(p) {
		key := fileKey(f.Repository, f.Path)
		fd := &fileDiff{key: key, path: f.Path, kind: kindOf(f.Path)}
		switch f.Change {
		case plan.ChangeUnknown:
			fd.unreadable = f.Error
		case plan.ChangeCreate, plan.ChangeUpdate:
			fd.diffs = differences(key, base[key], rs.got[key].content, driven)
			for i := range fd.diffs {
				fd.diffs[i].Planned = rms.reason(fd, fd.diffs[i].Path)
			}
		}
		c.files[key] = fd
	}
	return c, p, nil
}

// build is the plan of values for opts' installation, read through read,
// with the files the definition renders flattened to their leaves by key.
// The definition's refusal is the error.
func build(ctx context.Context, opts Options, values map[string]any, read plan.Reader) (plan.Installation, map[string]map[string]string, error) {
	p := plan.Build(ctx, plan.Options{Definition: opts.Definition, Installation: opts.Installation, Hub: opts.Hub, Inputs: values, Content: true, Read: read})
	if p.Refused != "" {
		return p, nil, errors.New(p.Refused)
	}
	flat := map[string]map[string]string{}
	for _, f := range rendered(p) {
		flat[fileKey(f.Repository, f.Path)] = flattenYAML(f.Content)
	}
	return p, flat, nil
}

// rendered are the plan's files the definition renders. The kustomizations
// its includes land in are other owners' files the plan lists an entry in,
// not the definition's: not compared.
func rendered(p plan.Installation) []plan.File {
	includes := map[string]bool{}
	for _, inc := range p.Includes {
		includes[fileKey(inc.Repository, inc.Path)] = true
	}
	out := make([]plan.File, 0, len(p.Files))
	for _, f := range p.Files {
		if !includes[fileKey(f.Repository, f.Path)] {
			out = append(out, f)
		}
	}
	return out
}

// differences are the leaves of the file as the plan writes it (want) that
// are off the file on record (current), each attributed to the input that
// drives it. A value the commit fills in or the record holds encrypted is
// never one; of an encrypted file the paths are the difference and the
// values are redacted, SOPS's own block taking no part.
func differences(key string, want map[string]string, current string, driven map[string]string) []Difference {
	got := flattenYAML(current)
	encrypted := plan.Encrypted(current)
	var out []Difference
	for _, p := range diffPaths(want, got) {
		w, okw := want[p]
		g, okg := got[p]
		if okw && okg && plan.Opaque(w, g) || encrypted && underSOPS(p) {
			continue
		}
		d := Difference{File: key, Path: p, Rendered: w, Current: g, Input: driven[key+"#"+p]}
		if encrypted {
			d.Rendered, d.Current = redacted(okw), redacted(okg)
		}
		out = append(out, d)
	}
	return out
}

// underSOPS says whether a leaf is of SOPS's block in an encrypted file.
func underSOPS(path string) bool {
	return path == plan.SOPSKey || strings.HasPrefix(path, plan.SOPSKey+".")
}

// redacted is a difference's value of an encrypted file: redacted when the
// side has the path, empty when it lacks it.
func redacted(present bool) string {
	if present {
		return Redacted
	}
	return ""
}

// reads is what the plan read as the caller, by file key: every file is read
// once, and the perturbed plans of drivenPaths read the record from here.
type reads struct {
	read plan.Reader
	got  map[string]read
}

type read struct {
	content string
	err     error
}

// reader reads a file as the caller, once.
func (r *reads) reader(ctx context.Context, repository, path string) (string, error) {
	key := fileKey(repository, path)
	if got, ok := r.got[key]; ok {
		return got.content, got.err
	}
	content, err := r.read(ctx, repository, path)
	r.got[key] = read{content: content, err: err}
	return content, err
}

// recorded answers what reader read; a file it did not read is absent.
func (r *reads) recorded(_ context.Context, repository, path string) (string, error) {
	if got, ok := r.got[fileKey(repository, path)]; ok {
		return got.content, got.err
	}
	return "", gh.ErrNotFound
}

// flatRender renders an inputs document to flat files, by file key.
type flatRender func(values map[string]any) (map[string]map[string]string, error)

// drivenPaths names, for every rendered leaf an input drives, the input that
// drives it: each leaf of the inputs on record is perturbed (left out, and
// its value changed) and the leaves whose render changes are its. A leaf
// several inputs drive is attributed to the most specific one — the input
// whose perturbation changes the fewest leaves.
func drivenPaths(values map[string]any, base map[string]map[string]string, render flatRender) map[string]string {
	type best struct {
		input string
		n     int
	}
	bests := map[string]best{}
	for _, l := range leaves(values, nil) {
		for _, alt := range perturbations(l.value) {
			raw := deepCopy(values)
			set(raw, l.path, alt)
			other, err := render(raw)
			if err != nil {
				continue
			}
			changed := changedLeaves(base, other)
			for _, k := range changed {
				if b, ok := bests[k]; !ok || len(changed) < b.n {
					bests[k] = best{input: strings.Join(l.path, "."), n: len(changed)}
				}
			}
		}
	}
	out := make(map[string]string, len(bests))
	for k, b := range bests {
		out[k] = b.input
	}
	return out
}

type leaf struct {
	path  []string
	value any
}

// leaves are the scalar and list leaves of a nested inputs map, by path.
func leaves(v any, path []string) []leaf {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return []leaf{{path: path, value: v}}
	}
	var out []leaf
	for k, vv := range m {
		out = append(out, leaves(vv, append(append([]string{}, path...), k))...)
	}
	return out
}

// removed marks a leaf left out of the inputs.
type removed struct{}

// perturbations are the alternatives a leaf is tried with: left out, and a
// value of its type that differs.
func perturbations(v any) []any {
	out := []any{removed{}}
	switch t := v.(type) {
	case bool:
		out = append(out, !t)
	case string:
		out = append(out, t+"-x")
	case float64:
		out = append(out, t+1)
	case int:
		out = append(out, t+1)
	}
	return out
}

func set(m map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	if _, ok := v.(removed); ok {
		delete(m, path[len(path)-1])
		return
	}
	m[path[len(path)-1]] = v
}

func deepCopy(m map[string]any) map[string]any {
	raw, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

// changedLeaves are the "file#path" leaves that differ between two flat renders.
func changedLeaves(a, b map[string]map[string]string) []string {
	var out []string
	for key := range union(a, b) {
		for _, p := range diffPaths(a[key], b[key]) {
			out = append(out, key+"#"+p)
		}
	}
	return out
}

func union[V any](a, b map[string]V) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// diffPaths are the leaves that differ between two flat files, sorted.
func diffPaths(want, got map[string]string) []string {
	var out []string
	for p := range union(want, got) {
		w, okw := want[p]
		g, okg := got[p]
		if okw != okg || w != g {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// flattenYAML flattens every document of a YAML file to its leaves by dotted
// path. The entries of a sequence are keyed by identity, not position — a
// scalar by its value, a mapping by its kind/namespace/name, id or name, the
// way the plan keys the objects, clients and tunnels it merges — and by index
// where an entry has none or two share one; a file of several documents keys
// each by its kind/namespace/name. A reordered list or file is so the same
// leaves, and an entry added is its own. A file that is not YAML is one leaf
// at the empty path.
func flattenYAML(content string) map[string]string {
	out := map[string]string{}
	docs, err := decodeAll(content)
	if err != nil {
		return map[string]string{"": content}
	}
	if len(docs) == 1 {
		flatten(docs[0], "", out)
		return out
	}
	for i, k := range keys(docs, "doc") {
		flatten(docs[i], "["+k+"]", out)
	}
	return out
}

// decodeAll parses every YAML document of content.
func decodeAll(content string) ([]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(content))
	var docs []any
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) {
				return docs, nil
			}
			return nil, err
		}
		docs = append(docs, v)
	}
}

func flatten(v any, prefix string, out map[string]string) {
	join := func(k string) string {
		if prefix == "" {
			return k
		}
		return prefix + "." + k
	}
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			out[prefix] = "{}"
		}
		for k, vv := range t {
			flatten(vv, join(k), out)
		}
	case []any:
		if len(t) == 0 {
			out[prefix] = "[]"
		}
		for i, k := range keys(t, "") {
			flatten(t[i], prefix+"["+k+"]", out)
		}
	case nil:
		out[prefix] = "null"
	default:
		out[prefix] = fmt.Sprint(t)
	}
}

// keys are the identities of a sequence's entries, one per entry; the
// indexes, opened by prefix, when an entry has none or two share one.
func keys(items []any, prefix string) []string {
	out := make([]string, len(items))
	seen := map[string]bool{}
	for i, item := range items {
		id := identity(item)
		if id == "" || seen[id] {
			for i := range out {
				out[i] = prefix + fmt.Sprint(i)
			}
			return out
		}
		seen[id] = true
		out[i] = id
	}
	return out
}

// identityKeys name a mapping entry, in order: a client by id, a tunnel or
// token by name, an MCP server by url.
var identityKeys = []string{"id", "name", "url"}

// identity is what names an entry of a sequence: a scalar its value, a
// mapping its kind/namespace/name (an object), else its id or name; empty
// for one with none.
func identity(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if meta, ok := t["metadata"].(map[string]any); ok {
			if name, _ := meta["name"].(string); name != "" {
				kind, _ := t["kind"].(string)
				namespace, _ := meta["namespace"].(string)
				return kind + "/" + namespace + "/" + name
			}
		}
		for _, k := range identityKeys {
			switch id := t[k].(type) {
			case string, int, float64, bool:
				return fmt.Sprint(id)
			}
		}
		return ""
	case []any, nil:
		return ""
	}
	return fmt.Sprint(v)
}

// kindOf is the dimension kind a rendered file is observed under.
func kindOf(path string) string {
	switch {
	case strings.Contains(path, "/apps/dex-app/"):
		return definitions.KindDexSecret
	case strings.Contains(path, "/apps/"):
		return definitions.KindConfigMap
	case strings.Contains(path, "/extras/backstage/"):
		return definitions.KindBackstage
	}
	return definitions.KindExtras
}

// relPath is a file's path under its extras directory, for matching the
// keys of extras and backstage dimensions.
func relPath(path string) string {
	if i := strings.Index(path, "/extras/"); i >= 0 {
		rest := path[i+len("/extras/"):]
		if _, after, ok := strings.Cut(rest, "/"); ok {
			return after
		}
	}
	return path
}

// matcher is what a file dimension's key says about where it is observed:
// the YAML paths (dotted words) and the files or directories under extras
// (words with a slash or a file suffix) its key names. A key that names
// neither is prose: the catch-all of its kind.
type matcher struct {
	dim      *Dimension
	kind     string
	prefixes []string
}

func newMatcher(d definitions.Dimension) matcher {
	m := matcher{dim: &Dimension{ID: d.ID, Kind: d.Kind, Key: d.Key, Mark: NotChecked}, kind: d.Kind}
	for _, word := range strings.Fields(d.Key) {
		if !strings.ContainsAny(word, "*()") && strings.ContainsFunc(word, unicode.IsLetter) && strings.ContainsAny(word, "./") {
			m.prefixes = append(m.prefixes, word)
		}
	}
	return m
}

// isFile says whether a prefix names a file or directory rather than a YAML path.
func isFile(p string) bool {
	return strings.Contains(p, "/") || strings.HasSuffix(p, ".yaml") || strings.HasSuffix(p, ".patch") || strings.HasSuffix(p, ".json")
}

// match is the length of the longest prefix of m that names the difference
// at yamlPath in the file at rel; 0 when none does.
func (m matcher) match(rel, yamlPath string) int {
	n := 0
	for _, p := range m.prefixes {
		hit := false
		if isFile(p) {
			dir := strings.TrimSuffix(p, "/")
			hit = rel == dir || strings.HasSuffix(rel, "/"+dir) || strings.HasPrefix(rel, dir+"/")
		} else {
			hit = yamlPath == p || strings.HasPrefix(yamlPath, p+".") || strings.HasPrefix(yamlPath, p+"[")
		}
		if hit && len(p) > n {
			n = len(p)
		}
	}
	return n
}

// assign routes every difference of the comparison to the dimension whose key
// names it most specifically — the kind's catch-all when none does — and
// marks every file dimension; live dimensions are not checked.
func assign(c *comparison, feats []definitions.Feature, refused string) map[string]*Dimension {
	var matchers []matcher
	dims := map[string]*Dimension{}
	for _, f := range feats {
		for _, d := range f.Dimensions {
			m := newMatcher(d)
			dims[d.ID] = m.dim
			if d.Kind == definitions.KindLive {
				m.dim.Reason = ReasonAuthority
				continue
			}
			if c == nil {
				m.dim.Reason = refused
				if refused == "" {
					m.dim.Reason = ReasonNoRender
				}
				continue
			}
			matchers = append(matchers, m)
		}
	}
	if c == nil {
		return dims
	}
	filesByKind := map[string][]string{}
	unreadable := map[string]bool{}
	for key, fd := range c.files {
		filesByKind[fd.kind] = append(filesByKind[fd.kind], key)
		if fd.unreadable != "" {
			unreadable[fd.kind] = true
		}
		for _, d := range fd.diffs {
			target := catchAll(matchers, fd.kind)
			bestN := 0
			for _, m := range matchers {
				if n := m.match(relPath(fd.path), d.Path); m.kind == fd.kind && n > bestN {
					target, bestN = m.dim, n
				}
			}
			if target != nil {
				target.Differences = append(target.Differences, d)
			}
		}
	}
	for _, m := range matchers {
		files := filesByKind[m.kind]
		sort.Strings(files)
		m.dim.Files = files
		m.dim.Mark = fileMark(m.dim.Differences, len(files) > 0, unreadable[m.kind])
		if m.dim.Mark == NotChecked {
			m.dim.Reason = ReasonNoFile
			if unreadable[m.kind] {
				m.dim.Reason = ReasonUnreadable
			}
		}
		sort.Slice(m.dim.Differences, func(i, j int) bool {
			return m.dim.Differences[i].File+m.dim.Differences[i].Path < m.dim.Differences[j].File+m.dim.Differences[j].Path
		})
	}
	return dims
}

// catchAll is where a difference no key names goes: the first dimension of
// kind whose key is prose, else the first whose key names a directory.
func catchAll(matchers []matcher, kind string) *Dimension {
	for _, m := range matchers {
		if m.kind == kind && len(m.prefixes) == 0 {
			return m.dim
		}
	}
	for _, m := range matchers {
		for _, p := range m.prefixes {
			if m.kind == kind && strings.HasSuffix(p, "/") {
				return m.dim
			}
		}
	}
	return nil
}

// fileMark is a file dimension's mark from its differences: drifted when
// any is drift, differs by input when any names an input and none is
// drift, planned when the rest are planned changes.
func fileMark(diffs []Difference, hasFiles, unreadable bool) Mark {
	mark := AsDefined
	for _, d := range diffs {
		switch {
		case d.Planned != "":
			mark = worse(mark, Planned)
		case d.Input == "":
			return Drifted
		default:
			mark = worse(mark, DiffersByInput)
		}
	}
	if mark == AsDefined && (unreadable || !hasFiles) {
		return NotChecked
	}
	return mark
}

// worse is the more severe of two marks.
func worse(a, b Mark) Mark {
	for _, m := range severity {
		if m == a || m == b {
			return m
		}
	}
	return a
}
