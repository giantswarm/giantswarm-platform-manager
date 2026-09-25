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
	"maps"
	"net/http"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Mark is what a dimension, and a feature rolled up from its dimensions, shows.
type Mark string

// The marks. A feature is drifted when any dimension is, differs by input
// when any does and none drifted, planned when its only differences are
// planned changes (the keys the capability's removals and migrations name),
// as defined when at least one dimension was checked and none differs, not
// checked otherwise.
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
// run for, a target unreachable from the manager — are next to probe in
// probes.go. ReasonUnreadable and ReasonNoFile open a reason that names,
// after the colon, the files the plan could not compare (GitHub's refusal
// as the caller, or a file on record that takes no entry) with the answer
// to each, or the kind of file the definition renders none of.
const (
	ReasonAuthority  = "needs your session on the installation"
	ReasonNoRender   = "nothing rendered to compare against: the inputs are missing or refused"
	ReasonUnreadable = "a file of the dimension could not be compared"
	ReasonNoFile     = "the definition renders no file of the dimension's kind for the inputs on record"
	// ReasonMissingChoice opens the reason of a dimension whose leaves carry
	// a Missing marker: a required person input no layer of the inputs
	// holds, named by field after the colon. Never a difference: a choice
	// not on record is nothing to compare against, only a commit refuses it.
	ReasonMissingChoice = "choice not on record"
)

// missingChoice is the reason a dimension is not checked for the choices
// not on record its leaves carry, by field.
func missingChoice(fields []string) string {
	return ReasonMissingChoice + ": " + strings.Join(fields, ", ")
}

// unreadable is the reason a dimension is not checked for the files of its
// kind the plan could not compare: each file with the answer, sorted.
func unreadable(files []string) string {
	return ReasonUnreadable + ": " + strings.Join(slices.Sorted(slices.Values(files)), "; ")
}

// noFile is the reason a dimension is not checked when the definition
// renders no file of its kind for the inputs on record.
func noFile(kind string) string {
	return ReasonNoFile + ": " + kind
}

// Difference is one place a repository file, or a live object, is off the
// render: at path (a YAML path inside file; empty when the whole file
// differs; a path that crosses the YAML document a string field holds names
// the field, then ":" and the path inside it: data.values:route.enabled,
// data.values:backstage.appConfig:app.title in the portal's app-config).
// File names the repository file; Object the live object of a live
// dimension (resource namespace/name). Input names the person's input that
// drives the path — a choice of the person the definition's schema names,
// or an input typed for the call — the file expresses another choice than
// the one on record, which a dry run with that input typed shows as the
// change asked for; empty, the path is drift: a leaf no input of the person
// drives, the ones the installation's facts derive included, which only a
// reconcile resolves. Planned is the reason of the
// capability's removal or migration that names the path — a key the fleet
// still carries that the definition does not render, or one the definition
// renders that the record lacks: a planned change, not drift. Rendered and
// Current are the values on each side — Redacted for a leaf the record
// holds encrypted (one under SOPS's encrypted_regex of a file SOPS
// encrypted); every other leaf of such a file shows its values. Line and
// CurrentLine are where the leaf sits in the file's Content and Current as
// the result shows them (1-based; a mapping entry's key line, a sequence
// entry's "- " line), 0 on a side that lacks it.
type Difference struct {
	File        string `json:"file,omitempty"`
	Object      string `json:"object,omitempty"`
	Path        string `json:"path,omitempty"`
	Input       string `json:"input,omitempty"`
	Planned     string `json:"planned,omitempty"`
	Rendered    string `json:"rendered,omitempty"`
	Current     string `json:"current,omitempty"`
	Line        int    `json:"line,omitempty"`
	CurrentLine int    `json:"currentLine,omitempty"`
	// absent says the record has no leaf at the path (the file is created,
	// or the leaf is new): what a migration adds.
	absent bool
	// dropped says the record holds the leaf encrypted and the render has
	// none: the commit drops a value the manager cannot carry over.
	dropped bool
}

// Dimension is one observed aspect of a feature with its mark.
type Dimension struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
	Mark   Mark   `json:"mark"`
	Reason string `json:"reason,omitempty"`
	// Detail is what a reason rests on when that is more than the one
	// sentence: the transport's error behind a target unreachable from the
	// manager.
	Detail string `json:"detail,omitempty"`
	// Files are the repository files the dimension was compared in.
	Files       []string     `json:"files,omitempty"`
	Differences []Difference `json:"differences,omitempty"`
	Probe       *ProbeResult `json:"probe,omitempty"`
	// Live is what a live dimension's probes answered (CompareLive).
	Live *LiveResult `json:"live,omitempty"`
	// missing are the choices not on record the dimension's leaves carry.
	missing map[string]bool
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
	// Unset names, by field, every choice of the person no layer holds a
	// value for — no default, nothing read back, nothing typed: the choices
	// not on record. The required ones among them are Missing.
	Unset []string `json:"unset,omitempty"`
	// Missing names, by field, the required person inputs no layer holds:
	// rendered as Missing markers, every leaf that carries one compared as
	// not checked; a commit refuses them.
	Missing []string `json:"missing,omitempty"`
	// Typed is what the person typed for the call, laid over the record and
	// the read-back: the inputs of theirs beside the schema's choices, a
	// fact typed over for a dry run included. Every leaf it carries is
	// attributed to itself (Difference.Input). An action's are the inputs
	// its wave was called with.
	Typed map[string]any `json:"typed,omitempty"`
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
	// CommitRefused says why a commit of this plan would be refused.
	CommitRefused    string                 `json:"commitRefused,omitempty"`
	Files            []plan.File            `json:"files"`
	Includes         []plan.Include         `json:"includes"`
	Diff             map[plan.Change]int    `json:"diff"`
	PullRequests     []plan.PullRequest     `json:"pullRequests"`
	GeneratedSecrets []plan.GeneratedSecret `json:"generatedSecrets"`
	SuppliedSecrets  []string               `json:"suppliedSecrets"`
	SuppliedOnRecord []string               `json:"suppliedOnRecord,omitempty"`
	HubSections      []string               `json:"hubSections,omitempty"`
	DexClients       []plan.DexClient       `json:"dexClients"`
	CustomerActions  []plan.CustomerAction  `json:"customerActions"`
	Probes           []plan.Probe           `json:"probes"`
}

// view takes the plan's view into the result; the files' content, rendered
// and on record, only when asked for.
func (r *Result) view(p plan.Installation, content bool) {
	r.Files, r.Includes, r.Diff = p.Files, p.Includes, p.Diff
	r.GeneratedSecrets, r.SuppliedSecrets, r.SuppliedOnRecord, r.HubSections = p.GeneratedSecrets, p.SuppliedSecrets, p.SuppliedOnRecord, p.HubSections
	r.DexClients, r.CustomerActions, r.Probes = p.DexClients, p.CustomerActions, p.Probes
	if !content {
		r.Files = make([]plan.File, len(p.Files))
		for i, f := range p.Files {
			f.Content, f.Current = "", ""
			r.Files[i] = f
		}
	}
}

// Plan is the result regrouped as the dry run's entry: what a commit would
// write, without the marks.
func (r Result) Plan() plan.Installation {
	return plan.Installation{Name: r.Installation, State: r.State, Inputs: r.Inputs.Values, MissingInputs: r.Inputs.Missing, Refused: r.Refused, CommitRefused: r.CommitRefused,
		Files: r.Files, Includes: r.Includes, Diff: r.Diff, GeneratedSecrets: r.GeneratedSecrets, SuppliedSecrets: r.SuppliedSecrets, SuppliedOnRecord: r.SuppliedOnRecord,
		HubSections: r.HubSections, DexClients: r.DexClients, CustomerActions: r.CustomerActions, Probes: r.Probes}
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
	// Rotate names the generated values the plan rotates on request (plan.Options.Rotate).
	Rotate []string
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
	migs, err := definitions.Migrations(opts.Definition.Name)
	if err != nil {
		r.Refused = err.Error()
		return r
	}
	// Without inputs nothing is rendered: the file dimensions read not
	// checked, the anonymous probes still run.
	var c *comparison
	own := ownFacts(opts.Installation)
	if opts.Inputs.Values != nil {
		var p plan.Installation
		if c, p, err = compare(ctx, opts, readRemovals(rms, own), readMigrations(migs, own)); err != nil {
			r.Refused = err.Error()
			c = nil
		}
		r.Inputs.Missing = p.MissingInputs
		if p.Inputs != nil {
			// The plan's effective inputs: the values with the definition's
			// selections laid over (a fresh enable's chart line).
			r.Inputs.Values = p.Inputs
		}
		r.view(p, opts.Content)
	}
	dims, others := assign(c, feats, r.Refused, own)
	var clients []plan.DexClient
	if c != nil {
		clients = c.dexClients
	}
	probed := newProber(opts.Probes).probeAll(ctx, probeData(opts.Installation.Name, opts.Installation.BaseDomain, opts.Inputs.Values), clients, c != nil, probes)
	for _, fd := range feats {
		f := Feature{ID: fd.ID, Title: fd.Title, Marks: map[Mark]int{}, Dimensions: []Dimension{}}
		for _, d := range fd.Dimensions {
			f.Dimensions = append(f.Dimensions, *dims[d.ID])
		}
		for i, p := range probes {
			if p.Feature == fd.ID {
				f.Dimensions = append(f.Dimensions, probed[i])
			}
		}
		r.addFeature(f)
	}
	if len(others) > 0 {
		f := Feature{ID: OtherFeature, Title: "Other", Marks: map[Mark]int{}, Dimensions: []Dimension{}}
		for _, d := range others {
			f.Dimensions = append(f.Dimensions, *d)
		}
		r.addFeature(f)
	}
	// Drifted is an installation with the fileset on record off its
	// definition; one not enabled differs everywhere and keeps saying so.
	if r.Summary[Drifted] > 0 && r.State.OnRecord() {
		r.State = installations.StateDrifted
	}
	return r
}

// addFeature rolls f up from its dimensions and counts their marks into
// the summary.
func (r *Result) addFeature(f Feature) {
	for _, d := range f.Dimensions {
		f.Marks[d.Mark]++
		r.Summary[d.Mark]++
	}
	f.Mark = rollUp(f.Dimensions)
	r.Features = append(r.Features, f)
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
	// missing are the leaves of the render that carry a Missing marker, by
	// path: the choices not on record each names. Not checked, never a
	// difference.
	missing map[string][]string
	// documents are the fields of the file, as rendered or on record, whose
	// text holds a YAML document compared as its leaves (flat.documents):
	// how a leaf's path splits into the field and the path inside it.
	documents map[string]bool
}

// Redacted stands for a value the record holds encrypted in a difference:
// the path differs, the value is not shown.
const Redacted = "<encrypted>"

// compare builds the plan from the inputs — the render, read against the
// repositories as the caller, every file once — and names each difference
// the plan would write an input or drift. The plan's outcome per file is
// the comparison: a file it leaves unchanged (an encrypted file whose
// plaintext skeleton is the render's, a shared file that differs only in
// the entries other owners keep or their order) has no difference; a file
// it creates or updates differs at the leaves of the file as the plan
// writes it that are off the record, each under a key the removals name
// marked planned, as is each the record lacks under a key the migrations
// name; the plan names the sections of the hub's Dev Portal whose value on
// record those removals take (HubSections). The plan is the second answer,
// the definition's refusal the error.
func compare(ctx context.Context, opts Options, rms, migs plannedKeys) (*comparison, plan.Installation, error) {
	rs := &reads{read: opts.Read, got: map[string]read{}}
	p, base, err := build(ctx, opts, opts.Inputs.Values, rs.reader)
	if err != nil {
		return nil, p, err
	}
	inputs, err := drivenInputs(opts.Definition, opts.Inputs)
	if err != nil {
		return nil, p, err
	}
	driven := drivenPaths(opts.Inputs.Values, inputs, base, func(values map[string]any) (map[string]map[string]string, error) {
		_, other, err := build(ctx, opts, values, rs.recorded)
		return other, err
	})
	c := &comparison{files: map[string]*fileDiff{}, dexClients: p.DexClients}
	hub := map[string]bool{}
	for _, f := range rendered(p) {
		key := fileKey(f.Repository, f.Path)
		fd := &fileDiff{key: key, path: f.Path, kind: kindOf(f.Path), documents: flattenLines(f.Content).documents}
		switch f.Change {
		case plan.ChangeUnknown:
			fd.unreadable = f.Error
		case plan.ChangeCreate, plan.ChangeUpdate:
			var docs map[string]bool
			fd.diffs, docs = differences(key, f.Content, rs.got[key].content, driven)
			maps.Copy(fd.documents, docs)
			for i := range fd.diffs {
				fd.diffs[i].Planned = planned(fd, &fd.diffs[i], rms, migs)
				if section := rms.hubSection(fd, &fd.diffs[i]); section != "" {
					hub[section] = true
				}
			}
		}
		fd.missing = missingLeaves(base[key])
		c.files[key] = fd
	}
	p.HubSections = rms.hubSections(hub)
	shown(p.Files)
	return c, p, nil
}

// shown redacts the files of an encrypted record the way their differences
// are: the record with SOPS's block dropped and the leaves under its
// encrypted_regex redacted, and the file as the plan writes it the same way
// where it kept ciphertext (a shared file's entries), so both sides compare
// line by line. Every other file is shown as it is.
func shown(files []plan.File) {
	for i := range files {
		f := &files[i]
		if !plan.Encrypted(f.Current) {
			continue
		}
		secret := encryptedLeaves(flattenYAML(f.Current))
		f.Current = redactLeaves(f.Current, secret)
		if plan.Ciphertext(f.Content) {
			f.Content = redactLeaves(f.Content, secret)
		}
	}
}

// planned is the reason a difference is a planned change: the removal that
// names its path, or, for a leaf the record lacks, the migration that adds
// it, or, for a value the record holds encrypted that the render does not
// carry, its removal by the commit (droppedReason). A leaf the record holds
// with another value is no migration's — but for a scalar the plan merges as
// a comma-separated set, whose only change is the entries the migrations
// add (joined).
func planned(fd *fileDiff, d *Difference, rms, migs plannedKeys) string {
	if reason := rms.reason(fd, d.Path); reason != "" {
		return reason
	}
	switch {
	case d.dropped:
		return droppedReason(d.Path)
	case d.absent:
		return migs.reason(fd, d.Path)
	case plan.JoinedList(fd.path, d.Path):
		return joined(fd, d, migs)
	}
	return ""
}

// droppedReason is the planned change of a value the record holds encrypted
// that no input renders: the manager decrypts nothing, so the commit cannot
// carry it over and drops it — the key names the value, readable on record.
func droppedReason(path string) string {
	return "Removed: " + path + " is held encrypted on record and rendered by no input, and the manager decrypts nothing, so the commit drops it; a value still needed is supplied at commit where the definition asks for it, or kept by hand."
}

// build is the plan of values for opts' installation, read through read,
// with the files the definition renders flattened to their leaves by key.
// The definition's refusal is the error.
func build(ctx context.Context, opts Options, values map[string]any, read plan.Reader) (plan.Installation, map[string]map[string]string, error) {
	p := plan.Build(ctx, plan.Options{Definition: opts.Definition, Installation: opts.Installation, Hub: opts.Hub, Inputs: values, Content: true, Read: read, Rotate: opts.Rotate})
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

// differences are the leaves of the file as the plan writes it (rendered)
// that are off the file on record (current), each attributed to the input
// that drives it and placed on its line of each side. A value the commit
// fills in or the record holds encrypted is never one — nor a leaf inside
// text the record holds encrypted (a Secret's values: the record's stand) —
// nor is a leaf that carries a choice not on record (a Missing marker: not
// checked); an empty mapping or list on one side where the other holds
// entries under the key is the key without its entries, and the entries
// are the differences. Of an encrypted file SOPS's own block takes no part, and a leaf SOPS encrypts
// (one under its encrypted_regex) is the difference with the values
// redacted; every other leaf of it — type, the metadata, apiVersion, kind —
// shows its values like a plain file's. The lines of such a file are those
// of the file as the result shows it, redacted (shown).
func differences(key string, rendered, current string, driven map[string]string) ([]Difference, map[string]bool) {
	rendered_, current_ := flattenLines(rendered), flattenLines(current)
	want, wantLines := rendered_.values, rendered_.lines
	got, gotLines := current_.values, current_.lines
	encrypted := plan.Encrypted(current)
	var secret func(string) bool
	if encrypted {
		secret = encryptedLeaves(got)
		gotLines = flattenLines(redactLeaves(current, secret)).lines
		if plan.Ciphertext(rendered) {
			wantLines = flattenLines(redactLeaves(rendered, secret)).lines
		}
	}
	// documents are the fields whose text holds leaves, on either side.
	documents := maps.Clone(rendered_.documents)
	maps.Copy(documents, current_.documents)
	var out []Difference
	for _, p := range diffPaths(want, got) {
		w, okw := want[p]
		g, okg := got[p]
		if okw && okg && plan.Opaque(w, g) || encrypted && underSOPS(p) || okw && len(missingFields(w)) > 0 || encryptedText(documents, got, p) || emptied(want, got, p) {
			continue
		}
		d := Difference{File: key, Path: p, Rendered: w, Current: g, Line: wantLines[p], CurrentLine: gotLines[p], Input: driven[key+"#"+p], absent: !okg,
			dropped: encrypted && !okw && plan.Ciphertext(g)}
		if encrypted && secret(p) {
			d.Rendered, d.Current = redacted(okw), redacted(okg)
		}
		out = append(out, d)
	}
	return out, documents
}

// emptied says whether a leaf is an empty mapping or list on the one side
// that has it while the other side holds entries under the key: the same
// key without its entries, no difference of its own — the entries are.
func emptied(want, got map[string]string, p string) bool {
	w, okw := want[p]
	g, okg := got[p]
	switch {
	case okw && !okg:
		return (w == "{}" || w == "[]") && under(got, p)
	case okg && !okw:
		return (g == "{}" || g == "[]") && under(want, p)
	}
	return false
}

// under says whether flat holds a leaf beneath p: a key, an entry or a
// document's leaf.
func under(flat map[string]string, p string) bool {
	for q := range flat {
		if len(q) > len(p) && strings.HasPrefix(q, p) && strings.ContainsRune(".["+textSep, rune(q[len(p)])) {
			return true
		}
	}
	return false
}

// encryptedText says whether a leaf is of a document a field holds as text
// (documents) that the record holds encrypted: the field itself, ciphertext
// on record where the render has the document's leaves, or a leaf inside
// it. The manager decrypts nothing, so the record's text stands and neither
// is a difference, like a value the record holds encrypted (Opaque).
func encryptedText(documents map[string]bool, got map[string]string, p string) bool {
	field := holder(documents, p)
	if field == "" {
		if !documents[p] {
			return false
		}
		field = p
	}
	return plan.Ciphertext(got[field])
}

// missingMarker matches a Missing marker in a rendered value, capturing
// the field.
var missingMarker = regexp.MustCompile(regexp.QuoteMeta(render.Missing("")[:len(render.Missing(""))-1]) + `([^)]+)\)`)

// missingFields names the choices not on record a rendered value carries:
// the fields of its Missing markers, sorted, each once; none for a value
// without one.
func missingFields(value string) []string {
	var fields []string
	for _, m := range missingMarker.FindAllStringSubmatch(value, -1) {
		if !slices.Contains(fields, m[1]) {
			fields = append(fields, m[1])
		}
	}
	sort.Strings(fields)
	return fields
}

// missingLeaves are the leaves of a flat render that carry a Missing
// marker, by path, each with the fields it names; nil when none does.
func missingLeaves(want map[string]string) map[string][]string {
	var out map[string][]string
	for p, w := range want {
		if fields := missingFields(w); len(fields) > 0 {
			if out == nil {
				out = map[string][]string{}
			}
			out[p] = fields
		}
	}
	return out
}

// underSOPS says whether a leaf is of SOPS's block in an encrypted file.
func underSOPS(path string) bool {
	return path == plan.SOPSKey || strings.HasPrefix(path, plan.SOPSKey+".")
}

// encryptedRegexKey is the leaf of SOPS's block that holds the regex of
// the keys it encrypts, as the repository's .sops.yaml set it.
const encryptedRegexKey = plan.SOPSKey + ".encrypted_regex"

// encryptedLeaves says, for the flattened record of an encrypted file, which
// leaves SOPS holds encrypted: those with a key on their path the block's
// encrypted_regex matches — ^(data|stringData)$ in the fleet, a Secret's
// values, whether or not the record has the leaf yet. A block without the
// regex encrypts every value: every leaf is redacted.
func encryptedLeaves(got map[string]string) func(path string) bool {
	pattern, ok := got[encryptedRegexKey]
	re, err := regexp.Compile(pattern)
	if !ok || err != nil {
		return func(string) bool { return true }
	}
	return func(path string) bool {
		return slices.ContainsFunc(segments(path), re.MatchString)
	}
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
// The plan fetches its files from several goroutines at once; the cache is
// safe for that.
type reads struct {
	read plan.Reader
	mu   sync.Mutex
	got  map[string]read
}

type read struct {
	content string
	err     error
}

// reader reads a file as the caller, once.
func (r *reads) reader(ctx context.Context, repository, path string) (string, error) {
	key := fileKey(repository, path)
	r.mu.Lock()
	got, ok := r.got[key]
	r.mu.Unlock()
	if ok {
		return got.content, got.err
	}
	content, err := r.read(ctx, repository, path)
	r.mu.Lock()
	r.got[key] = read{content: content, err: err}
	r.mu.Unlock()
	return content, err
}

// recorded answers what reader read; a file it did not read is absent.
func (r *reads) recorded(_ context.Context, repository, path string) (string, error) {
	r.mu.Lock()
	got, ok := r.got[fileKey(repository, path)]
	r.mu.Unlock()
	if ok {
		return got.content, got.err
	}
	return "", gh.ErrNotFound
}

// flatRender renders an inputs document to flat files, by file key.
type flatRender func(values map[string]any) (map[string]map[string]string, error)

// drivenInputs are the inputs whose leaves the attribution perturbs, by
// dotted key, sorted: the person's choices the definition's schema names
// (x-source person) and every leaf the call's typed inputs carry, so a fact
// typed over for a dry run reads as that person's input. The record's
// facts, the registry's and every other leaf nobody chose are left alone: a
// leaf they derive that is off the record is drift, not a choice.
func drivenInputs(def installations.Capability, in Inputs) ([]string, error) {
	out, err := def.PersonInputs()
	if err != nil {
		return nil, err
	}
	if len(in.Typed) > 0 {
		for _, l := range leaves(in.Typed, nil) {
			out = append(out, strings.Join(l.path, "."))
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}

// drivenPaths names, for every rendered leaf one of inputs drives, the input
// that drives it: each leaf on record under an input (by dotted key; one the
// record holds no value for perturbs nothing) is perturbed — left out, and
// its value changed — and the leaves whose render changes are its. A leaf
// several inputs drive is attributed to the most specific one — the input
// whose perturbation changes the fewest leaves. A rendered leaf none of them
// moves is nobody's: drift when it is off the record.
func drivenPaths(values map[string]any, inputs []string, base map[string]map[string]string, render flatRender) map[string]string {
	type best struct {
		input string
		n     int
	}
	bests := map[string]best{}
	for _, l := range inputLeaves(values, inputs) {
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

// inputLeaves are the leaves of values under the inputs named by dotted key,
// sorted by path so that a tie between two inputs falls the same way on
// every run: an input with one value is that leaf, one holding a mapping
// every leaf below it; an input values holds nothing for has none.
func inputLeaves(values map[string]any, inputs []string) []leaf {
	var out []leaf
	for _, in := range inputs {
		path := strings.Split(in, ".")
		if v, ok := lookup(values, path); ok {
			out = append(out, leaves(v, path)...)
		}
	}
	slices.SortFunc(out, func(a, b leaf) int { return slices.Compare(a.path, b.path) })
	return out
}

// lookup is the value at path in a nested inputs map, and whether it is there.
func lookup(m map[string]any, path []string) (any, bool) {
	var v any = m
	for _, k := range path {
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = next[k]; !ok {
			return nil, false
		}
	}
	return v, true
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
// leaves, and an entry added is its own. A payload string that holds a YAML
// mapping over several lines — a ConfigMap's chart values, the portal's
// app-config inside them — is the mapping's leaves, each under the field's
// path and textSep (payload, textMapping); a JSON 6902 patch is one leaf
// holding its value's one spelling (patchValue). A file that is not YAML is
// one leaf at the empty path. flattenLines also names each leaf's line.
func flattenYAML(content string) map[string]string {
	return flattenLines(content).values
}

// flatten flattens a decoded value to its leaves under prefix.
func flatten(v any, prefix string, out map[string]string) {
	flattenIn(v, prefix, false, out, nil)
}

// flattenIn flattens a decoded value to its leaves under prefix, inside a
// document a string holds or not; a payload string that holds a mapping is
// the mapping's leaves, its field recorded in documents when given, and a
// JSON 6902 patch's text its value's one spelling.
func flattenIn(v any, prefix string, inDocument bool, out map[string]string, documents map[string]bool) {
	switch t := v.(type) {
	case string:
		if docs, ok := textMapping(t); ok && (inDocument || payload(prefix)) {
			if documents != nil {
				documents[prefix] = true
			}
			flattenIn(docs[0].value, prefix+textSep, true, out, documents)
			return
		}
		if v, ok := patchValue(prefix, t, inDocument); ok {
			t = v
		}
		out[prefix] = t
	case map[string]any:
		if len(t) == 0 {
			out[prefix] = "{}"
		}
		for k, vv := range t {
			flattenIn(vv, joinPath(prefix, k), inDocument, out, documents)
		}
	case []any:
		if len(t) == 0 {
			out[prefix] = "[]"
		}
		for i, k := range keys(t, "") {
			flattenIn(t[i], prefix+"["+k+"]", inDocument, out, documents)
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

// kindOf is the dimension kind a rendered file is observed under: the
// dex-app's configmap patch dex-configmap and its secret patch dex-secret,
// another app's patch configmap, the installation's record record, a file
// under extras/backstage/ backstage, every other file extras.
func kindOf(p string) string {
	switch {
	case strings.HasPrefix(p, "installations/") && path.Base(p) == render.RecordFile:
		return definitions.KindRecord
	case strings.Contains(p, "/apps/dex-app/"):
		if path.Base(p) == secretPatch {
			return definitions.KindDexSecret
		}
		return definitions.KindDexConfigMap
	case strings.Contains(p, "/apps/"):
		return definitions.KindConfigMap
	case strings.Contains(p, "/extras/backstage/"):
		return definitions.KindBackstage
	}
	return definitions.KindExtras
}

// observedPath is a file's path as a dimension's file words name it: under
// extras/ for the extras kind, under extras/backstage/ for the backstage
// kind, under apps/ for a patch; whole for a file elsewhere.
func observedPath(kind, p string) string {
	switch kind {
	case definitions.KindBackstage:
		return strings.TrimPrefix(extrasPath(p), render.PortalDir+"/")
	case definitions.KindExtras:
		return extrasPath(p)
	}
	if i := strings.Index(p, "/apps/"); i >= 0 {
		return p[i+len("/apps/"):]
	}
	return p
}

// matcher is what a file dimension's key says about where it is observed:
// the key's alternatives (its parts around " / "), each the files and the
// YAML paths it names, and whether the dimension is its kind's catch-all.
//
// The key's grammar: a word with a slash or a file suffix names a file or
// directory (isFile), every other word a YAML path; a remark in parentheses
// is not read; an alternative without a file word is observed in the files
// of the alternative before it. A catch-all's key is prose and names
// nothing: the dimension takes every leaf of its kind no other names.
type matcher struct {
	dim      *Dimension
	kind     string
	catchAll bool
	alts     []alternative
}

// alternative is one part of a key. It names a leaf when one of its files
// matches the leaf's file (any file, when it names none) and one of its
// paths the leaf's path (any path, when it names none).
type alternative struct {
	files []word
	paths []word
}

// word is one file or path word of a key, compiled: a file word to one
// pattern over the observed path, a path word to one pattern per segment.
// n is the word's length: how specific it is. never marks a word with a
// fact's placeholder the installation lacks: it names nothing.
type word struct {
	text     string
	n        int
	never    bool
	file     *regexp.Regexp
	segments []*regexp.Regexp
}

// remarks matches the parenthesised remarks of a key, placeholders its <x>
// placeholders.
var (
	remarks      = regexp.MustCompile(`\([^)]*\)`)
	placeholders = regexp.MustCompile(`<[^>]+>`)
)

// newMatcher reads d's key under the installation's facts (a fact's
// placeholder, <domain>, stands for the fact itself, never for any name).
func newMatcher(d definitions.Dimension, f facts) matcher {
	m := matcher{dim: &Dimension{ID: d.ID, Kind: d.Kind, Key: d.Key, Mark: NotChecked}, kind: d.Kind, catchAll: d.CatchAll}
	if d.CatchAll || d.Kind == definitions.KindLive {
		return m
	}
	for _, part := range strings.Split(remarks.ReplaceAllString(d.Key, " "), " / ") {
		var a alternative
		for _, w := range strings.Fields(part) {
			if isFile(w) {
				a.files = append(a.files, fileWord(w, f))
			} else {
				a.paths = append(a.paths, pathWord(w, f))
			}
		}
		if len(a.files) == 0 && len(m.alts) > 0 {
			a.files = m.alts[len(m.alts)-1].files
		}
		if len(a.files)+len(a.paths) > 0 {
			m.alts = append(m.alts, a)
		}
	}
	return m
}

// words are the key's file and path words, as written: what the dimension
// names; none for prose.
func (m matcher) words() []string {
	var out []string
	for _, a := range m.alts {
		for _, w := range append(append([]word{}, a.files...), a.paths...) {
			if !slices.Contains(out, w.text) {
				out = append(out, w.text)
			}
		}
	}
	return out
}

// isFile says whether a word names a file or directory rather than a YAML
// path: a slash outside an index, or a file suffix.
func isFile(w string) bool {
	if strings.Contains(w, "[") {
		return false
	}
	return strings.Contains(w, "/") || strings.HasSuffix(w, ".yaml") || strings.HasSuffix(w, ".patch") || strings.HasSuffix(w, ".json")
}

// fileWord compiles a file word: it names the observed path when it is a
// run of whole segments of it — the file itself, a directory above it, or a
// name at any depth; <x> stands for a part of a segment.
func fileWord(w string, f facts) word {
	p := strings.TrimSuffix(w, "/")
	src, ok := pattern(p, `[^/]+`, f)
	return word{text: w, n: len(p), never: !ok, file: regexp.MustCompile(`(^|/)` + src + `(/|$)`)}
}

// pathWord compiles a path word to one pattern per segment (segmentRegexp).
// A trailing [*] or .* is dropped: a path covers everything beneath it.
func pathWord(w string, f facts) word {
	p := strings.TrimSuffix(w, ".*")
	for strings.HasSuffix(p, "[*]") {
		p = strings.TrimSuffix(p, "[*]")
	}
	out := word{text: w, n: len(p)}
	for _, s := range segments(p) {
		re, ok := segmentRegexp(s, f)
		out.never = out.never || !ok
		out.segments = append(out.segments, re)
	}
	return out
}

var (
	anyEntry = regexp.MustCompile(`^\[.*\]$`)
	anyKey   = regexp.MustCompile(`^[^\[].*$`)
)

// segmentRegexp is what one segment of a path word matches: [*] any list
// entry, <x> alone any map key, <x> within a name any part of it, a fact's
// placeholder the fact, anything else itself; false when the installation
// lacks the fact.
func segmentRegexp(s string, f facts) (*regexp.Regexp, bool) {
	switch {
	case s == "[*]":
		return anyEntry, true
	case placeholders.FindString(s) == s && !factPlaceholders[s[1:len(s)-1]]:
		return anyKey, true
	}
	src, ok := pattern(s, ".+", f)
	return regexp.MustCompile("^" + src + "$"), ok
}

// pattern is the regexp source of a word's text with its placeholders
// expanded: <x> to part, a fact's placeholder to the fact itself; false
// when the installation lacks the fact.
func pattern(text, part string, f facts) (string, bool) {
	ok := true
	src := placeholders.ReplaceAllStringFunc(regexp.QuoteMeta(text), func(ph string) string {
		name := ph[1 : len(ph)-1]
		if !factPlaceholders[name] {
			return part
		}
		if f[name] == "" {
			ok = false
		}
		return regexp.QuoteMeta(f[name])
	})
	return src, ok
}

// match is how specifically m names the leaf at yamlPath of the file at rel
// (its observed path): the lengths of the file and path words of the
// alternative that names it most specifically; 0 when none does.
func (m matcher) match(rel, yamlPath string) int {
	best := 0
	segs := segments(yamlPath)
	for _, a := range m.alts {
		if n, ok := a.match(rel, segs); ok && n > best {
			best = n
		}
	}
	return best
}

func (a alternative) match(rel string, segs []string) (int, bool) {
	n := 0
	if len(a.files) > 0 {
		f, ok := longest(a.files, func(w word) bool { return !w.never && w.file.MatchString(rel) })
		if !ok {
			return 0, false
		}
		n += f
	}
	if len(a.paths) > 0 {
		p, ok := longest(a.paths, func(w word) bool { return !w.never && w.covers(segs) })
		if !ok {
			return 0, false
		}
		n += p
	}
	return n, true
}

// longest is the length of the longest word of ws that hits; false when none does.
func longest(ws []word, hit func(word) bool) (int, bool) {
	n, ok := 0, false
	for _, w := range ws {
		if hit(w) && (!ok || w.n > n) {
			n, ok = w.n, true
		}
	}
	return n, ok
}

// covers says whether a path word names the leaf whose segments are segs:
// the word's segments match the leaf's leading ones.
func (w word) covers(segs []string) bool {
	if len(segs) < len(w.segments) {
		return false
	}
	for i, re := range w.segments {
		if !re.MatchString(segs[i]) {
			return false
		}
	}
	return true
}

// OtherFeature is the result's feature that carries what no dimension of
// the definition names: one dimension per kind of file, present only when a
// leaf of the kind was observed under no dimension, so nothing is dropped.
const OtherFeature = "other"

// other is the dimension of OtherFeature for a kind.
func other(kind string) *Dimension {
	return &Dimension{ID: OtherFeature + "-" + kind, Kind: kind, Key: "every leaf of the kind's files no dimension of the definition names", Mark: NotChecked}
}

// assign routes every difference and every choice not on record of the
// comparison to the dimension whose key names it most specifically — the
// kind's catch-all when none does, the kind's dimension of OtherFeature when
// it has none — and marks every file dimension; live dimensions are not
// checked. A leaf that carries a choice not on record makes its dimension
// not checked, with the reason naming the choice, where nothing else in it
// is off. The answer is the definition's dimensions by id and the
// dimensions of OtherFeature, by kind. The keys are read under the
// installation's facts (own).
func assign(c *comparison, feats []definitions.Feature, refused string, own facts) (map[string]*Dimension, []*Dimension) {
	var matchers []matcher
	dims := map[string]*Dimension{}
	for _, f := range feats {
		for _, d := range f.Dimensions {
			m := newMatcher(d, own)
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
		return dims, nil
	}
	others := map[string]*Dimension{}
	target := func(fd *fileDiff, yamlPath string) *Dimension {
		if d := route(matchers, fd, yamlPath); d != nil {
			return d
		}
		d, ok := others[fd.kind]
		if !ok {
			d = other(fd.kind)
			others[fd.kind] = d
			matchers = append(matchers, matcher{dim: d, kind: fd.kind})
		}
		return d
	}
	filesByKind := map[string][]string{}
	// refusals are, by kind, the files the caller could not read, each with
	// the reader's answer; GitHub's names the file, a reader's that does
	// not is prefixed with it.
	refusals := map[string][]string{}
	for key, fd := range c.files {
		filesByKind[fd.kind] = append(filesByKind[fd.kind], key)
		if fd.unreadable != "" {
			answer := fd.unreadable
			if !strings.Contains(answer, fd.path) {
				answer = key + ": " + answer
			}
			refusals[fd.kind] = append(refusals[fd.kind], answer)
		}
		for _, d := range fd.diffs {
			t := target(fd, d.Path)
			t.Differences = append(t.Differences, d)
		}
		for yamlPath, fields := range fd.missing {
			t := target(fd, yamlPath)
			if t.missing == nil {
				t.missing = map[string]bool{}
			}
			for _, f := range fields {
				t.missing[f] = true
			}
		}
	}
	for _, m := range matchers {
		files := filesByKind[m.kind]
		sort.Strings(files)
		m.dim.Files = files
		m.dim.Mark = fileMark(m.dim.Differences, len(files) > 0, len(refusals[m.kind]) > 0)
		switch {
		case m.dim.Mark == AsDefined && len(m.dim.missing) > 0:
			m.dim.Mark, m.dim.Reason = NotChecked, missingChoice(slices.Sorted(maps.Keys(m.dim.missing)))
		case m.dim.Mark == NotChecked && len(refusals[m.kind]) > 0:
			m.dim.Reason = unreadable(refusals[m.kind])
		case m.dim.Mark == NotChecked:
			m.dim.Reason = noFile(m.kind)
		}
		sort.Slice(m.dim.Differences, func(i, j int) bool {
			return m.dim.Differences[i].File+m.dim.Differences[i].Path < m.dim.Differences[j].File+m.dim.Differences[j].Path
		})
	}
	var rest []*Dimension
	for _, kind := range slices.Sorted(maps.Keys(others)) {
		rest = append(rest, others[kind])
	}
	return dims, rest
}

// route is the dimension a leaf at yamlPath of fd is observed under: the one
// of the file's kind whose key names it most specifically (the first, on a
// tie), else the kind's catch-all; nil when the kind has none. The keys name
// paths inside the document a string field holds, or the field that holds
// the text: the leaf is matched at every level of its path (levels).
func route(matchers []matcher, fd *fileDiff, yamlPath string) *Dimension {
	var target *Dimension
	best := 0
	rel, paths := observedPath(fd.kind, fd.path), levels(fd.documents, yamlPath)
	for _, m := range matchers {
		if m.kind != fd.kind {
			continue
		}
		for _, p := range paths {
			if n := m.match(rel, p); n > best {
				target, best = m.dim, n
			}
		}
	}
	if target == nil {
		target = catchAll(matchers, fd.kind)
	}
	return target
}

// catchAll is the dimension of kind declared as its catch-all; nil when none is.
func catchAll(matchers []matcher, kind string) *Dimension {
	for _, m := range matchers {
		if m.kind == kind && m.catchAll {
			return m.dim
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
