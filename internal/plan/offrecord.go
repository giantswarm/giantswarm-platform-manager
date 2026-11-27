package plan

import (
	"slices"
)

// offRecord collects, as Build walks the plan's files, what the plan takes
// off the record: the record's side of every file the plan changes, and of
// every file it deletes. A plan deletes a file only where the definition's
// removal of kind deleted-file names it (deleteFiles), and a kustomization
// keeps every other entry on record (keep), so a document the plan removes
// from a file it writes and a file it deletes are the ways an object leaves
// the record. The objects those documents declare that no file of the plan
// declares again are the plan's checklist candidates
// (Installation.Checklist), each on the installation whose tree its file is
// in; whether the fleet's Kustomization prunes them, and what another
// capability on record keeps, the caller reads (Unpruned, Checklist).
type offRecord struct {
	// installation is the plan's: an object of a file in its own tree, or in
	// a fleet repository's, is on it.
	installation string
	// removed are the record's side of the files the plan changes or deletes.
	removed []recorded
	// stays are the files as the plan writes them.
	stays []recorded
}

// recorded is a file's content at repository:path.
type recorded struct {
	repository, path, content string
}

// file records one file of the plan: content as the plan writes it, current
// as it is on record (onRecord false where the record lacks it or it could
// not be read).
func (o *offRecord) file(repository, path, content, current string, onRecord bool) {
	o.stays = append(o.stays, recorded{repository, path, content})
	if onRecord && current != content {
		o.removed = append(o.removed, recorded{repository, path, current})
	}
}

// deleted records a file the plan deletes, current as it is on record.
func (o *offRecord) deleted(repository, path, current string) {
	o.removed = append(o.removed, recorded{repository, path, current})
}

// objects are the objects f declares, each on the installation whose tree f
// is in (Object.Installation, empty for the plan's own) and taken off the
// record by f's repository.
func (o *offRecord) objects(f recorded) []Object {
	on := owner(f.path)
	if on == o.installation {
		on = ""
	}
	var out []Object
	for _, ob := range manifests([]byte(f.content)) {
		ob.Installation, ob.Repository = on, f.repository
		out = append(out, ob)
	}
	return out
}

// checklist are the objects the files on record declare that no file of the
// plan declares on the same installation, kustomize's own documents aside,
// each once.
func (o *offRecord) checklist() []Object {
	stays := map[Object]bool{}
	for _, f := range o.stays {
		for _, ob := range o.objects(f) {
			stays[ob.on()] = true
		}
	}
	var out []Object
	for _, f := range o.removed {
		for _, ob := range o.objects(f) {
			if ob.listed() && !stays[ob.on()] && !slices.ContainsFunc(out, func(have Object) bool { return have.on() == ob.on() }) {
				out = append(out, ob)
			}
		}
	}
	return out
}
