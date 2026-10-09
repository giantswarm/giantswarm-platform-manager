package plan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The disable: what removing a capability from an installation takes out of
// its repositories, the mirror of an enable's plan. The definition's render
// for the installation names its files; what the installation keeps on
// record of every other capability's render (Remaining) stays. A directory
// the definition's include lists in a kustomization other owners write
// (extras/agent-platform/) leaves whole, every file under it on record with
// it — the definition's render, and what was put there beside it. A file
// the definition writes whole goes; the definition's marker goes whatever
// else it carries, so the capability reads not enabled after the merge. A
// file with several owners — a kustomization the includes land in, the
// dex-app configmap patch, the installation's record — loses the
// definition's part and keeps the rest, every remaining definition's part
// written back as its own plan writes it; one left with nothing in it goes.

// ChangeDelete is a file the disable takes out of the repository.
const ChangeDelete Change = "delete"

// Removal is one file of the installation's repositories the disable
// changes: deleted, or updated to Content — a file with several owners
// without the definition's part.
type Removal struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Change     Change `json:"change"`
	// Content is the file as the commit writes it, for an update; Current is
	// the file on record, for a diff of the two. Neither for a delete.
	Content string `json:"content,omitempty"`
	Current string `json:"current,omitempty"`
	// Unrendered: a file the definition's render does not name, deleted
	// because its directory leaves whole — another owner's file, or one an
	// earlier shape of the definition wrote.
	Unrendered bool `json:"unrendered,omitempty"`
	// Pairings are the values this file holds that another installation
	// holds too (render.Peer), as "<installation>:<path>": removed here, the
	// other side's copy pairs with nothing.
	Pairings []string `json:"pairings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// Stay is a file of the definition's render that the disable leaves on
// record, and why: another capability on record renders it.
type Stay struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Why        string `json:"why"`
}

// Disablement is the disable of one capability on one installation: the
// files it removes or edits, the files of the definition that stay, the
// objects the fleet's Kustomization leaves on the cluster (Checklist) and
// the pull requests, one per repository.
type Disablement struct {
	Name        string    `json:"name"`
	Files       []Removal `json:"files"`
	Stays       []Stay    `json:"stays"`
	Directories []string  `json:"directories"`
	// Others are the installations whose trees the definition's render names
	// files in (a hub's side, a portal's section): the disable leaves them as
	// they are, and each one's reconcile aligns them.
	Others       []string      `json:"others"`
	Checklist    []Object      `json:"checklist"`
	PullRequests []PullRequest `json:"pullRequests"`
	// Bases are the remote kustomize bases (resources by URL) the deleted
	// kustomizations pulled in, whose objects the checklist names once read.
	Bases []string `json:"bases,omitempty"`
}

// Remaining is a capability on record on the installation besides the one
// disabled, rendered as it is once the disable merged.
type Remaining struct {
	Capability string
	Result     *render.Result
}

// DisableOptions shape one installation's disable.
type DisableOptions struct {
	Definition   installations.Capability
	Installation installations.Installation
	Hub          installations.Installation
	// Result is the definition's render for the installation (a comparison's).
	Result    *render.Result
	Remaining []Remaining
	Read      Reader
	// List names the files on record under a directory of a repository.
	List func(ctx context.Context, repository, dir string) ([]string, error)
}

// Disable answers the disable of opts' capability on opts' installation,
// every file read as the caller through opts.Read. A file that could not be
// read is a Removal of change unknown naming the error: nothing is removed
// blind.
func Disable(ctx context.Context, opts DisableOptions) Disablement {
	d := Disablement{Name: opts.Installation.Name, Files: []Removal{}, Stays: []Stay{}, Directories: []string{}, Others: []string{}, Checklist: []Object{}, PullRequests: []PullRequest{}}
	resolve := func(r render.Repository) string {
		return ResolveRepository(string(r), opts.Installation, opts.Hub)
	}
	marker := opts.Definition.Repository(opts.Installation.Repositories) + ":" + opts.Definition.EnabledMarker(opts.Installation.Name)

	// What the remaining capabilities render, by file, and the includes they list.
	remaining := map[string][]remainingFile{}
	remainingIncludes := map[string]bool{}
	for _, r := range opts.Remaining {
		for repo, files := range r.Result.Files {
			for p, f := range files {
				k := fileRef{resolve(repo), p}.key()
				remaining[k] = append(remaining[k], remainingFile{capability: r.Capability, file: f})
			}
		}
		for _, inc := range r.Result.Includes {
			remainingIncludes[includeKey(resolve(inc.Repository), inc.Path, inc)] = true
		}
	}

	// The directories that leave whole: each include of the definition the
	// remaining capabilities do not list too.
	var dirs []fileRef
	unlist, foreign := map[string][]render.Include{}, map[string][]render.Include{}
	for _, inc := range opts.Result.Includes {
		repo := resolve(inc.Repository)
		if remainingIncludes[includeKey(repo, inc.Path, inc)] {
			continue
		}
		k := fileRef{repo, inc.Path}.key()
		if owner(inc.Path) != d.Name {
			foreign[k] = append(foreign[k], inc)
			continue
		}
		unlist[k] = append(unlist[k], inc)
		dirs = append(dirs, fileRef{repo, path.Clean(path.Join(path.Dir(inc.Path), inc.Resource))})
	}
	inDir := func(repo, p string) bool {
		return slices.ContainsFunc(dirs, func(dir fileRef) bool {
			return dir.repository == repo && strings.HasPrefix(p, dir.path+"/")
		})
	}
	for _, dir := range dirs {
		d.Directories = append(d.Directories, dir.key()+"/")
	}

	// Every file the disable may touch, read at once: the definition's,
	// those on record under the directories that leave, the kustomizations
	// the includes land in.
	rendered := map[string]render.File{}
	var refs []fileRef
	for repo, files := range opts.Result.Files {
		for p, f := range files {
			ref := fileRef{resolve(repo), p}
			rendered[ref.key()] = f
			refs = append(refs, ref)
		}
	}
	var listErrs []Removal
	for _, dir := range dirs {
		paths, err := opts.List(ctx, dir.repository, dir.path)
		if err != nil {
			listErrs = append(listErrs, Removal{Repository: dir.repository, Path: dir.path + "/", Change: ChangeUnknown, Error: err.Error()})
			continue
		}
		for _, p := range paths {
			refs = append(refs, fileRef{dir.repository, p})
		}
	}
	for _, m := range []map[string][]render.Include{unlist, foreign} {
		for k := range m {
			repo, p, _ := strings.Cut(k, ":")
			refs = append(refs, fileRef{repo, p})
		}
	}
	got := fetch(ctx, opts.Read, refs)

	seen := map[string]bool{}
	var removed []render.File
	for _, ref := range sortedRefs(refs) {
		k := ref.key()
		if seen[k] {
			continue
		}
		seen[k] = true
		current, err := got.read(ctx, ref.repository, ref.path)
		switch {
		case errors.Is(err, gh.ErrNotFound):
			continue
		case err != nil:
			d.Files = append(d.Files, Removal{Repository: ref.repository, Path: ref.path, Change: ChangeUnknown, Error: err.Error()})
			continue
		}
		f, isRendered := rendered[k]
		others := remaining[k]
		if o := owner(ref.path); o != d.Name {
			if why := foreignStay(o, current, isRendered, foreign[k]); why != "" {
				d.Stays = append(d.Stays, Stay{Repository: ref.repository, Path: ref.path, Why: why})
				if !slices.Contains(d.Others, o) {
					d.Others = append(d.Others, o)
				}
			}
			continue
		}
		switch {
		case len(others) > 0 && (k == marker || !Shared(ref.path)):
			d.Stays = append(d.Stays, Stay{Repository: ref.repository, Path: ref.path, Why: "rendered by " + capabilitiesOf(others)})
		case k == marker || inDir(ref.repository, ref.path) || isRendered && !Shared(ref.path):
			d.Files = append(d.Files, Removal{Repository: ref.repository, Path: ref.path, Change: ChangeDelete, Unrendered: !isRendered, Pairings: pairings(f)})
			removed = append(removed, render.File{Content: []byte(current)})
		default:
			r := edit(ref, current, f, isRendered, unlist[k], others)
			if r.Change == ChangeUnchanged {
				continue
			}
			if r.Change == ChangeDelete {
				removed = append(removed, render.File{Content: []byte(current)})
			}
			d.Files = append(d.Files, r)
		}
	}
	d.Files = append(d.Files, listErrs...)
	d.Checklist = checklistOf(removed, opts.Remaining)
	d.Bases = basesOf(removed)
	d.PullRequests = PullRequests([]Installation{d.asPlan()}, map[string]installations.Installation{opts.Installation.Name: opts.Installation}, opts.Hub)
	return d
}

// owner is the installation whose tree a path is in — installations/<name>/
// of a configs repository, management-clusters/<name>/ of a
// management-clusters repository — or "" for a path of none (a fleet
// repository's).
func owner(p string) string {
	for _, tree := range []string{"installations/", "management-clusters/"} {
		if rest, ok := strings.CutPrefix(p, tree); ok {
			name, _, _ := strings.Cut(rest, "/")
			return name
		}
	}
	return ""
}

// foreignStay is why a file of another installation's tree (or a fleet
// repository's) that the definition's render names stays as it is, or ""
// when it names nothing of the definition: the disable writes only the
// installation's own trees, and a reconcile of the other installation
// aligns its files.
func foreignStay(o, current string, isRendered bool, includes []render.Include) string {
	whose := "a fleet repository's file"
	if o != "" {
		whose = o + "'s file"
	}
	var listed []string
	for _, inc := range includes {
		list := ListResources
		if inc.Component {
			list = ListComponents
		}
		if lists(current, list, inc.Resource) {
			listed = append(listed, list+"["+inc.Resource+"]")
		}
	}
	switch {
	case isRendered:
		return whose + ", rendered here too: the disable writes the installation's own trees alone"
	case len(listed) > 0:
		return whose + " lists " + strings.Join(listed, ", ") + ": the disable writes the installation's own trees alone"
	}
	return ""
}

// lists says whether a kustomization lists entry under list.
func lists(content, list, item string) bool {
	_, m, err := mapping([]byte(content))
	if err != nil {
		return false
	}
	seq := entry(m, list)
	return seq != nil && slices.ContainsFunc(seq.Content, func(e *yaml.Node) bool {
		return strings.TrimSuffix(e.Value, "/") == strings.TrimSuffix(item, "/")
	})
}

// remainingFile is a file a remaining capability renders.
type remainingFile struct {
	capability string
	file       render.File
}

func capabilitiesOf(files []remainingFile) string {
	var names []string
	for _, f := range files {
		if !slices.Contains(names, f.capability) {
			names = append(names, f.capability)
		}
	}
	return strings.Join(names, ", ")
}

func includeKey(repo, kustomization string, inc render.Include) string {
	list := ListResources
	if inc.Component {
		list = ListComponents
	}
	return repo + ":" + kustomization + " " + list + " " + inc.Resource
}

func sortedRefs(refs []fileRef) []fileRef {
	out := slices.Clone(refs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out
}

// pairings are the values f holds that another installation holds too.
func pairings(f render.File) []string {
	var out []string
	for _, g := range f.Generated {
		if g.Peer == nil {
			continue
		}
		if p := g.Peer.Installation + ":" + g.Peer.Path; !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// edit takes the definition's part out of a file with several owners: its
// rendered leaves and entries (rendered, when the definition renders the
// file) that no remaining capability renders too (others), and the include
// entries it lists there (unlist). Every other byte stays as it is. A
// kustomization left listing nothing, or a file left with no key, is
// deleted; the record never is.
func edit(ref fileRef, current string, f render.File, isRendered bool, unlist []render.Include, others []remainingFile) Removal {
	r := Removal{Repository: ref.repository, Path: ref.path, Current: current}
	out := []byte(current)
	var err error
	if isRendered {
		keeps := make([][]byte, 0, len(others))
		for _, o := range others {
			keeps = append(keeps, o.file.Content)
		}
		if out, err = strip(f.Content, out, keeps...); err != nil {
			return r.failed(err)
		}
	}
	for _, inc := range unlist {
		list := ListResources
		if inc.Component {
			list = ListComponents
		}
		if out, err = UnlistEntry(out, list, inc.Resource); err != nil {
			return r.failed(err)
		}
	}
	empty, err := emptied(ref.path, out)
	if err != nil {
		return r.failed(err)
	}
	switch {
	case empty && !isRecord(ref.path):
		r.Change, r.Current = ChangeDelete, ""
	case string(out) == current:
		r.Change = ChangeUnchanged
	default:
		r.Change, r.Content = ChangeUpdate, string(out)
	}
	return r
}

func (r Removal) failed(err error) Removal {
	r.Change, r.Error, r.Current = ChangeUnknown, err.Error(), ""
	return r
}

func isRecord(p string) bool {
	return strings.HasPrefix(p, "installations/") && path.Base(p) == render.RecordFile
}

// emptied says whether a file holds nothing once the definition's part is
// out: a kustomization listing no resources and no components, any other
// file a mapping without keys.
func emptied(p string, content []byte) (bool, error) {
	_, m, err := mapping(content)
	if err != nil {
		return false, err
	}
	if path.Base(p) != kustomizationFile {
		return len(m.Content) == 0, nil
	}
	for _, list := range keptLists {
		if seq := entry(m, list); seq != nil && seq.Kind == yaml.SequenceNode && len(seq.Content) > 0 {
			return false, nil
		}
	}
	return true, nil
}

// strip answers current without rendered's part, edited on the YAML nodes
// so every comment and the order of what stays are kept: a scalar the
// render sets, or an empty mapping or list, goes with its key, a mapping loses the render's keys and goes
// once it held nothing else, a list loses the render's entries — a scalar
// by value, a mapping by its id or name, else by value — and goes once
// empty. What one of keeps (the renders of the capabilities that stay)
// carries at the same place stays. In a kustomization the render's
// resources and components go and nothing else: apiVersion and kind stay
// the file's.
func strip(rendered, current []byte, keeps ...[]byte) ([]byte, error) {
	doc, cur, err := mapping(current)
	if err != nil {
		return nil, err
	}
	ren, err := strippable(rendered)
	if err != nil {
		return nil, err
	}
	kept := make([]*yaml.Node, 0, len(keeps))
	for _, k := range keeps {
		n, err := strippable(k)
		if err != nil {
			return nil, err
		}
		kept = append(kept, n)
	}
	changed := stripMapping(cur, ren, kept)
	if header := headerOf(rendered); header != "" {
		for _, n := range append([]*yaml.Node{doc}, cur.Content...) {
			if strings.TrimSpace(n.HeadComment) == header {
				n.HeadComment, changed = "", true
			}
		}
	}
	if !changed {
		return current, nil
	}
	return encode(doc)
}

// headerOf is the comment a rendered file opens with (the
// definition's "Rendered by …" header), trimmed; "" for a file without one.
// The definition's part of a shared file is its header too.
func headerOf(rendered []byte) string {
	doc, m, err := mapping(rendered)
	if err != nil {
		return ""
	}
	if h := strings.TrimSpace(doc.HeadComment); h != "" {
		return h
	}
	if len(m.Content) > 0 {
		return strings.TrimSpace(m.Content[0].HeadComment)
	}
	return ""
}

// strippable is the part of a rendered file strip takes out: the mapping,
// or a kustomization's resources and components lists alone.
func strippable(rendered []byte) (*yaml.Node, error) {
	_, ren, err := mapping(rendered)
	if err != nil {
		return nil, err
	}
	if kind := entry(ren, "kind"); kind == nil || kind.Value != kustomizationKind && kind.Value != "Component" {
		return ren, nil
	}
	lists := &yaml.Node{Kind: yaml.MappingNode}
	for _, list := range keptLists {
		if seq := entry(ren, list); seq != nil {
			lists.Content = append(lists.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: list}, seq)
		}
	}
	return lists, nil
}

// stripMapping removes ren's part from the mapping cur, but what one of
// keeps carries, and says whether anything changed.
func stripMapping(cur, ren *yaml.Node, keeps []*yaml.Node) bool {
	changed := false
	for i := 0; i+1 < len(ren.Content); i += 2 {
		key, value := ren.Content[i].Value, ren.Content[i+1]
		at := keyIndex(cur, key)
		if at < 0 {
			continue
		}
		var under []*yaml.Node
		for _, k := range keeps {
			if k != nil && k.Kind == yaml.MappingNode {
				if n := entry(k, key); n != nil {
					under = append(under, n)
				}
			}
		}
		existing := cur.Content[at+1]
		var drop bool
		switch {
		case existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode && len(value.Content) > 0:
			if !stripMapping(existing, value, under) {
				continue
			}
			drop = len(existing.Content) == 0
		case existing.Kind == yaml.SequenceNode && value.Kind == yaml.SequenceNode && len(value.Content) > 0:
			if !stripSequence(existing, value, under) {
				continue
			}
			drop = len(existing.Content) == 0
		default:
			if len(under) > 0 {
				continue
			}
			drop = true
		}
		changed = true
		if drop {
			cur.Content = slices.Delete(cur.Content, at, at+2)
		}
	}
	return changed
}

// stripSequence removes from cur every entry ren carries that none of keeps
// carries, and says whether anything changed.
func stripSequence(cur, ren *yaml.Node, keeps []*yaml.Node) bool {
	carried := func(item *yaml.Node, in []*yaml.Node) bool {
		return slices.ContainsFunc(in, func(r *yaml.Node) bool { return sameEntry(item, r) })
	}
	var kept []*yaml.Node
	for _, k := range keeps {
		if k.Kind == yaml.SequenceNode {
			kept = append(kept, k.Content...)
		}
	}
	n := len(cur.Content)
	cur.Content = slices.DeleteFunc(cur.Content, func(item *yaml.Node) bool {
		return carried(item, ren.Content) && !carried(item, kept)
	})
	return len(cur.Content) != n
}

// sameEntry says whether a list entry on record is the render's: a mapping
// with an id (a Dex client) or a name by that key, anything else by value.
func sameEntry(cur, ren *yaml.Node) bool {
	if cur.Kind == yaml.MappingNode && ren.Kind == yaml.MappingNode {
		for _, key := range []string{"id", "name"} {
			c, r := entry(cur, key), entry(ren, key)
			if c != nil && r != nil {
				return c.Value == r.Value
			}
		}
	}
	return equal(cur, ren)
}

func keyIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// UnlistEntry answers current without entry under its top-level sequence
// list (resources or components): the mirror of ListEntry, edited on the
// YAML nodes; a list left empty goes with its key. An entry not listed
// leaves the file as it is.
func UnlistEntry(current []byte, list, item string) ([]byte, error) {
	doc, m, err := mapping(current)
	if err != nil {
		return nil, err
	}
	seq := entry(m, list)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return current, nil
	}
	n := len(seq.Content)
	seq.Content = slices.DeleteFunc(seq.Content, func(e *yaml.Node) bool {
		return e.Kind == yaml.ScalarNode && strings.TrimSuffix(e.Value, "/") == strings.TrimSuffix(item, "/")
	})
	if len(seq.Content) == n {
		return current, nil
	}
	if len(seq.Content) == 0 {
		at := keyIndex(m, list)
		m.Content = slices.Delete(m.Content, at, at+2)
	}
	return encode(doc)
}

// asPlan is the disable as a plan the pull requests are grouped from: every
// file it touches, by repository.
func (d Disablement) asPlan() Installation {
	p := Installation{Name: d.Name}
	for _, r := range d.Files {
		if r.Change == ChangeDelete || r.Change == ChangeUpdate {
			p.Files = append(p.Files, File{Repository: r.Repository, Path: r.Path, Change: r.Change})
		}
	}
	return p
}

// Unknown are the files the disable could not read as the caller.
func (d Disablement) Unknown() []Removal {
	var out []Removal
	for _, r := range d.Files {
		if r.Change == ChangeUnknown {
			out = append(out, r)
		}
	}
	return out
}

// Changes counts the files the disable deletes and updates.
func (d Disablement) Changes() (deleted, updated int) {
	for _, r := range d.Files {
		switch r.Change {
		case ChangeDelete:
			deleted++
		case ChangeUpdate:
			updated++
		}
	}
	return deleted, updated
}

// Pairings are the other installations' sides of the values the disable
// removes, each once, sorted.
func (d Disablement) Pairings() []string {
	var out []string
	for _, r := range d.Files {
		for _, p := range r.Pairings {
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Removes says whether the disable deletes repository:path.
func (d Disablement) Removes(repository, p string) bool {
	return slices.ContainsFunc(d.Files, func(r Removal) bool {
		return r.Repository == repository && r.Path == p && r.Change == ChangeDelete
	})
}

// checklistOf are the objects the deleted files declare that no remaining
// capability renders: what the fleet's non-pruning Kustomization leaves on
// the cluster once they are gone.
func checklistOf(removed []render.File, remaining []Remaining) []Object {
	stays := map[Object]bool{}
	for _, r := range remaining {
		for _, o := range Objects(&render.Result{Files: r.Result.Files}) {
			stays[o] = true
		}
	}
	var out []Object
	for _, f := range removed {
		for _, o := range manifests(f.Content) {
			if o.Kind != "" && o.Name != "" && o.Kind != kustomizationKind && o.Kind != "Component" && !stays[o] && !slices.Contains(out, o) {
				out = append(out, o)
			}
		}
	}
	return out
}

// basesOf are the remote bases the deleted kustomizations list: resources
// by URL, each once, in order.
func basesOf(removed []render.File) []string {
	var out []string
	for _, f := range removed {
		_, m, err := mapping(f.Content)
		if err != nil {
			continue
		}
		if kind := entry(m, "kind"); kind == nil || kind.Value != kustomizationKind {
			continue
		}
		if seq := entry(m, ListResources); seq != nil {
			for _, e := range seq.Content {
				if strings.Contains(e.Value, "://") && !slices.Contains(out, e.Value) {
					out = append(out, e.Value)
				}
			}
		}
	}
	return out
}

// ObjectsIn are the Kubernetes objects of a manifest file: every YAML
// document with a kind and a metadata.name, kustomize's own documents aside.
func ObjectsIn(content []byte) []Object {
	var out []Object
	for _, o := range manifests(content) {
		if o.Kind != "" && o.Name != "" && o.Kind != kustomizationKind {
			out = append(out, o)
		}
	}
	return out
}

// Checklist orders the objects a person deletes after the disable merged:
// the HelmReleases first (deleting one uninstalls its chart and what the
// chart made), their sources and every other object, the Namespaces — each
// takes what is left inside it —, then the objects outside them. An object
// inside a Namespace of the list goes with it and is left out.
func Checklist(objects []Object) []Object {
	namespaces := map[string]bool{}
	for _, o := range objects {
		if o.Kind == "Namespace" {
			namespaces[o.Name] = true
		}
	}
	rank := func(o Object) int {
		switch o.Kind {
		case helmReleaseKind:
			return 0
		case "Namespace":
			return 2
		case "Secret", "ConfigMap":
			return 3
		}
		return 1
	}
	var out []Object
	for _, o := range objects {
		if o.Kind != "Namespace" && namespaces[o.Namespace] || slices.Contains(out, o) {
			continue
		}
		out = append(out, o)
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// String is the object as a checklist line names it: Kind namespace/name.
func (o Object) String() string {
	if o.Namespace == "" {
		return o.Kind + " " + o.Name
	}
	return fmt.Sprintf("%s %s/%s", o.Kind, o.Namespace, o.Name)
}

// RemoteBase splits a kustomize remote base the way the fleet writes it,
// https://github.com/<owner>/<repo>//<dir>?ref=<ref>, into its repository,
// directory and ref; ok is false for any other shape.
func RemoteBase(url string) (repository, dir, ref string, ok bool) {
	rest, found := strings.CutPrefix(url, "https://github.com/")
	if !found {
		return "", "", "", false
	}
	rest, ref, _ = strings.Cut(rest, "?ref=")
	repository, dir, found = strings.Cut(rest, "//")
	if !found || strings.Count(repository, "/") != 1 {
		return "", "", "", false
	}
	return repository, strings.Trim(dir, "/"), ref, true
}

// KustomizationResources are the resources a kustomization.yaml lists, in
// order; nil for a file that is none.
func KustomizationResources(content []byte) []string {
	_, m, err := mapping(bytes.TrimSpace(content))
	if err != nil {
		return nil
	}
	seq := entry(m, ListResources)
	if seq == nil {
		return nil
	}
	var out []string
	for _, e := range seq.Content {
		out = append(out, e.Value)
	}
	return out
}
