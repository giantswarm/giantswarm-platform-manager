package plan

import (
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// offRecord collects, as Build walks the plan's files, what the plan takes
// off the record: the record's side of every file the plan changes. A plan
// deletes no file and unlists no kustomization entry — a kustomization
// keeps every entry on record (keep) — so a document it removes from a file
// it writes is the one way an object leaves the record. The objects those
// documents declare that no file of the plan declares again are the plan's
// checklist candidates (Installation.Checklist); whether the fleet's
// Kustomization prunes them, and what another capability on record keeps,
// the caller reads (Unpruned, Checklist).
type offRecord struct {
	// removed are the record's side of the files the plan changes.
	removed []render.File
	// stays are the files as the plan writes them, by repository and path.
	stays render.Fileset
}

// file records one file of the plan: content as the plan writes it, current
// as it is on record (onRecord false where the record lacks it or it could
// not be read).
func (o *offRecord) file(repository, path, content, current string, onRecord bool) {
	if o.stays == nil {
		o.stays = render.Fileset{}
	}
	repo := render.Repository(repository)
	if o.stays[repo] == nil {
		o.stays[repo] = map[string]render.File{}
	}
	o.stays[repo][path] = render.File{Content: []byte(content)}
	if onRecord && current != content {
		o.removed = append(o.removed, render.File{Content: []byte(current)})
	}
}

// checklist are the objects the files on record declare that no file of the
// plan declares, kustomize's own documents aside, each once.
func (o *offRecord) checklist() []Object {
	return checklistOf(o.removed, []Remaining{{Result: &render.Result{Files: o.stays}}})
}
