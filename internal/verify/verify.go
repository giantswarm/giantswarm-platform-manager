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
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Mark is what a dimension, and a feature rolled up from its dimensions, shows.
type Mark string

// The marks. A feature is drifted when any dimension is, differs by input
// when any does and none drifted, as defined when at least one dimension was
// checked and none differs, not checked otherwise.
const (
	AsDefined      Mark = "as defined"
	DiffersByInput Mark = "differs by input"
	Drifted        Mark = "drifted"
	NotChecked     Mark = "not checked"
)

// The reasons a dimension is not checked.
const (
	ReasonAuthority  = "needs the person's authority on the installation: verify_installation, the live registration's tool, checks it"
	ReasonNoInputs   = "no inputs on record: no action has rendered this capability for the installation yet"
	ReasonUnreadable = "a file of the dimension could not be read as the caller"
	ReasonNoFile     = "the definition renders no file of this kind for the inputs on record"
)

// Difference is one place a repository file, or a live object, is off the
// render: at path (a YAML path inside file; empty when the whole file
// differs). File names the repository file; Object the live object of a live
// dimension (resource namespace/name). Input names the input of the
// definition that drives the path — the file expresses another input than
// the one on record; empty, the path is drift.
type Difference struct {
	File     string `json:"file,omitempty"`
	Object   string `json:"object,omitempty"`
	Path     string `json:"path,omitempty"`
	Input    string `json:"input,omitempty"`
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

// Inputs are the inputs on record the render is compared from: the
// installation's record under the newest Action's inputs.
type Inputs struct {
	// Source is "action <name>", or "none" when no action holds inputs.
	Source string         `json:"source"`
	Values map[string]any `json:"values,omitempty"`
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
	// Probes sends the anonymous probes; nil is a client that does not follow redirects.
	Probes *http.Client
}

// Compare answers the verify of opts' installation.
func Compare(ctx context.Context, opts Options) Result {
	r := Result{Installation: opts.Installation.Name, Capability: opts.Definition.Name, Hub: opts.Hub.Name,
		State: opts.State, Inputs: opts.Inputs, Features: []Feature{}, Summary: map[Mark]int{}}
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
	var c *comparison
	if opts.Inputs.Values != nil {
		if c, err = compare(ctx, opts); err != nil {
			r.Refused = err.Error()
			c = nil
		}
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
	if r.Summary[Drifted] > 0 {
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
	for _, m := range []Mark{Drifted, DiffersByInput, AsDefined} {
		if seen[m] {
			return m
		}
	}
	return NotChecked
}

// fileKey is "repository:path" — how a file is named in the result.
func fileKey(repo, path string) string { return repo + ":" + path }

// comparison is the render from the inputs on record against the repositories.
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

// compare renders the inputs on record, reads every rendered file as the
// caller and names each difference an input or drift.
func compare(ctx context.Context, opts Options) (*comparison, error) {
	base, res, in, err := renderFlat(opts)
	if err != nil {
		return nil, err
	}
	driven := drivenPaths(opts.Inputs.Values, base, func(values map[string]any) (map[string]map[string]string, error) {
		other, _, _, err := renderFlat(Options{Definition: opts.Definition, Installation: opts.Installation, Hub: opts.Hub, Inputs: Inputs{Values: values}})
		return other, err
	})
	c := &comparison{files: map[string]*fileDiff{}}
	for _, files := range res.Files {
		for path, f := range files {
			if strings.HasSuffix(path, "/apps/dex-app/configmap-values.yaml.patch") {
				c.dexClients = plan.DexClients(f.Content, in)
			}
		}
	}
	for key, want := range base {
		repo, path, _ := strings.Cut(key, ":")
		fd := &fileDiff{key: key, path: path, kind: kindOf(path)}
		current, err := opts.Read(ctx, repo, path)
		got := map[string]string{}
		switch {
		case err == nil:
			got = flattenYAML(current)
		case errors.Is(err, gh.ErrNotFound):
		default:
			fd.unreadable = err.Error()
		}
		if fd.unreadable == "" {
			for _, p := range diffPaths(want, got) {
				fd.diffs = append(fd.diffs, Difference{File: key, Path: p, Rendered: want[p], Current: got[p], Input: driven[key+"#"+p]})
			}
		}
		c.files[key] = fd
	}
	return c, nil
}

// renderFlat renders values through the definition and flattens every file
// to its YAML leaves, by file key in the registry's repositories.
func renderFlat(opts Options) (map[string]map[string]string, *render.Result, render.Input, error) {
	in, err := opts.Definition.Parse(opts.Inputs.Values)
	if err != nil {
		return nil, nil, nil, err
	}
	res, err := opts.Definition.Render(opts.Inputs.Values, in.SuppliedMarkers())
	if err != nil {
		return nil, nil, nil, err
	}
	out := map[string]map[string]string{}
	for repo, files := range res.Files {
		target := plan.ResolveRepository(string(repo), opts.Installation, opts.Hub)
		for path, f := range files {
			out[fileKey(target, path)] = flattenYAML(string(f.Content))
		}
	}
	return out, res, in, nil
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
// path; a file that is not YAML is one leaf at the empty path.
func flattenYAML(content string) map[string]string {
	out := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(content))
	var docs []any
	for {
		var v any
		if err := dec.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return map[string]string{"": content}
		}
		docs = append(docs, v)
	}
	for i, d := range docs {
		prefix := ""
		if len(docs) > 1 {
			prefix = fmt.Sprintf("[doc%d]", i)
		}
		flatten(d, prefix, out)
	}
	return out
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
		for i, vv := range t {
			flatten(vv, fmt.Sprintf("%s[%d]", prefix, i), out)
		}
	case nil:
		out[prefix] = "null"
	default:
		out[prefix] = fmt.Sprint(t)
	}
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
				m.dim.Reason = ReasonNoInputs
				if refused != "" {
					m.dim.Reason = refused
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

// fileMark is a file dimension's mark from its differences.
func fileMark(diffs []Difference, hasFiles, unreadable bool) Mark {
	byInput := false
	for _, d := range diffs {
		if d.Input == "" {
			return Drifted
		}
		byInput = true
	}
	switch {
	case byInput:
		return DiffersByInput
	case unreadable || !hasFiles:
		return NotChecked
	}
	return AsDefined
}
