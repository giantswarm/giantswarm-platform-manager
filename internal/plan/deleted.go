package plan

import (
	"context"
	"errors"
	"path"
	"slices"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A file the definition no longer renders: a reconcile keeps every file on
// record but the ones a removal of kind deleted-file names. Such a file on
// record under the installation's extras/ goes whole — unless the render
// carries it after all, or another capability on record renders it, or it
// is the definition's marker — and the entry that lists it in the
// kustomization.yaml of its directory goes with it, unlisted the way a
// disable unlists what it deletes (gone.unlistRemoved). The objects the file
// declares join the plan's checklist candidates (offRecord): the
// Kustomization over extras/ does not prune.

// deletedRef is a file of the definition's removals of kind deleted-file in
// the installation's tree, with the kustomization of its directory.
type deletedRef struct {
	ref, kustomization fileRef
	why                string
}

// deletedRefs are the files opts' removals of kind deleted-file name, in the
// installation's management-clusters repository under
// management-clusters/<installation>/extras/.
func deletedRefs(opts Options) []deletedRef {
	if len(opts.DeletedFiles) == 0 {
		return nil
	}
	inst := opts.Installation
	repo := ResolveRepository("giantswarm/"+inst.Customer+"-management-clusters", inst, opts.Hub)
	out := make([]deletedRef, 0, len(opts.DeletedFiles))
	for _, d := range opts.DeletedFiles {
		p := path.Join("management-clusters", inst.Name, "extras", d.Path)
		out = append(out, deletedRef{ref: fileRef{repo, p}, kustomization: fileRef{repo, path.Join(path.Dir(p), kustomizationFile)}, why: d.Reason})
	}
	return out
}

// deleteFiles deletes the files opts' removals of kind deleted-file name
// that are on record and that nothing renders, and unlists each from the
// kustomization of its directory as the plan writes it (written) or, where
// the plan writes none, as it is on record. Each deleted file is a File of
// change delete naming why and the entries unlisted; its objects go to off.
// A file that could not be read, or whose staying cannot be told, is a File
// of change unknown: nothing is deleted blind.
func (p *Installation) deleteFiles(ctx context.Context, opts Options, res *render.Result, off *offRecord, written map[string]string) {
	refs := deletedRefs(opts)
	if len(refs) == 0 {
		return
	}
	resolve := func(r render.Repository) string { return ResolveRepository(string(r), opts.Installation, opts.Hub) }
	rendered := map[string]bool{}
	for repo, files := range res.Files {
		for p := range files {
			rendered[fileRef{resolve(repo), p}.key()] = true
		}
	}
	marker := ""
	if opts.Definition.EnabledMarker != nil {
		marker = opts.Definition.EnabledMarker(opts.Installation.Name)
	}
	// stays names, once read, the files the other capabilities on record render.
	var stays map[string]bool
	var staysErr error
	staying := func(ref fileRef) (bool, error) {
		if stays == nil && staysErr == nil {
			stays = map[string]bool{}
			if opts.Remaining != nil {
				var remaining []Remaining
				if remaining, staysErr = opts.Remaining(ctx); staysErr == nil {
					for _, r := range remaining {
						for repo, files := range r.Result.Files {
							for p := range files {
								stays[fileRef{resolve(repo), p}.key()] = true
							}
						}
					}
				}
			}
		}
		return stays[ref.key()], staysErr
	}
	g := gone{files: map[string]*Removal{}}
	var deleted []*Removal
	var kustomizations []fileRef
	for _, d := range refs {
		if rendered[d.ref.key()] || d.ref.path == marker || owner(d.ref.path) != opts.Installation.Name {
			continue
		}
		current, err := opts.Read(ctx, d.ref.repository, d.ref.path)
		if errors.Is(err, gh.ErrNotFound) {
			continue
		}
		if err == nil {
			var stay bool
			if stay, err = staying(d.ref); err == nil && stay {
				continue
			}
		}
		if err != nil {
			p.Files = append(p.Files, File{Repository: d.ref.repository, Path: d.ref.path, Change: ChangeUnknown, Error: err.Error()})
			p.Diff[ChangeUnknown]++
			continue
		}
		r := &Removal{Repository: d.ref.repository, Path: d.ref.path, Change: ChangeDelete, Why: d.why, Current: current}
		g.files[d.ref.key()] = r
		deleted = append(deleted, r)
		if !slices.Contains(kustomizations, d.kustomization) {
			kustomizations = append(kustomizations, d.kustomization)
		}
	}
	for _, k := range kustomizations {
		p.unlist(ctx, opts, k, g, written)
	}
	for _, r := range deleted {
		f := File{Repository: r.Repository, Path: r.Path, Change: ChangeDelete, Why: r.Why, ListedIn: r.ListedIn}
		if opts.Content {
			f.Current = r.Current
		}
		p.Files = append(p.Files, f)
		p.Diff[ChangeDelete]++
		off.deleted(r.Repository, r.Path, r.Current)
	}
}

// unlist takes the entries naming a file of g out of the kustomization at
// k: the plan's file where it writes one — its change update once it
// changed, the entries it kept for other owners without the ones unlisted —
// else a file of the plan of its own, read on record. An absent
// kustomization lists nothing; one that cannot be read or edited is a file
// of change unknown.
func (p *Installation) unlist(ctx context.Context, opts Options, k fileRef, g gone, written map[string]string) {
	i := slices.IndexFunc(p.Files, func(f File) bool { return f.Repository == k.repository && f.Path == k.path })
	content, ok := written[k.key()]
	if i >= 0 && (p.Files[i].Change == ChangeUnknown || !ok) {
		return
	}
	var current string
	if i < 0 {
		var err error
		current, err = opts.Read(ctx, k.repository, k.path)
		switch {
		case errors.Is(err, gh.ErrNotFound):
			return
		case err != nil:
			p.Files = append(p.Files, File{Repository: k.repository, Path: k.path, Change: ChangeUnknown, Error: err.Error()})
			p.Diff[ChangeUnknown]++
			return
		}
		content = current
	}
	out, err := g.unlistRemoved(k, []byte(content))
	if err != nil {
		if i >= 0 {
			p.Diff[p.Files[i].Change]--
			p.Files[i].Change, p.Files[i].Error, p.Files[i].Content = ChangeUnknown, err.Error(), ""
		} else {
			p.Files = append(p.Files, File{Repository: k.repository, Path: k.path, Change: ChangeUnknown, Error: err.Error()})
		}
		p.Diff[ChangeUnknown]++
		return
	}
	if string(out) == content {
		return
	}
	written[k.key()] = string(out)
	if i < 0 {
		f := File{Repository: k.repository, Path: k.path, Change: ChangeUpdate}
		if opts.Content {
			f.Content, f.Current = string(out), current
		}
		p.Files = append(p.Files, f)
		p.Diff[ChangeUpdate]++
		return
	}
	f := &p.Files[i]
	if opts.Content {
		f.Content = string(out)
	}
	f.Kept = slices.DeleteFunc(f.Kept, func(kept Kept) bool {
		return (kept.List == ListResources || kept.List == ListComponents) && !lists(string(out), kept.List, kept.Entry)
	})
	if f.Change == ChangeUnchanged {
		p.Diff[ChangeUnchanged]--
		p.Diff[ChangeUpdate]++
		f.Change = ChangeUpdate
	}
}
