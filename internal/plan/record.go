package plan

import (
	"gopkg.in/yaml.v3"
)

// The installation's record — installations/<name>/config.yaml.patch in its
// configs repository, the file konfigure overlays on the shared default for
// every app of the installation — is the installation's own file. The
// agent-platform definition writes one key into it, agentPlatform.kagentApiV2,
// the meta chart line a fresh enable selects (render/agentplatform), and never
// the file whole: the plan edits the rendered keys into the record and keeps
// everything else, comments included. The record is never created here: an
// installation without one is not on record at all.

// keepRecord answers current with rendered's keys edited in — a mapping
// merged key by key, a scalar replacing the value of its key, a key the record
// lacks appended with the rendered comment — and keeps every other key as it
// is. Nothing to edit leaves current byte for byte; nothing is named as kept,
// since the whole file is the record's own.
func keepRecord(rendered, current []byte) ([]byte, []Kept, error) {
	doc, cur, err := mapping(current)
	if err != nil {
		return nil, nil, err
	}
	_, ren, err := mapping(rendered)
	if err != nil {
		return nil, nil, err
	}
	if !mergeMapping(cur, ren) {
		return current, nil, nil
	}
	out, err := encode(doc)
	return out, nil, err
}

// mergeMapping edits ren's entries into cur and says whether anything
// changed: a key cur lacks is appended with ren's key and value nodes (their
// comments with them), two mappings merge, any other value is replaced where
// it differs.
func mergeMapping(cur, ren *yaml.Node) bool {
	changed := false
	for i := 0; i+1 < len(ren.Content); i += 2 {
		key, value := ren.Content[i], ren.Content[i+1]
		existing := entry(cur, key.Value)
		switch {
		case existing == nil:
			cur.Content = append(cur.Content, key, value)
			changed = true
		case existing.Kind == yaml.MappingNode && value.Kind == yaml.MappingNode:
			if mergeMapping(existing, value) {
				changed = true
			}
		case !equal(existing, value):
			*existing = *value
			changed = true
		}
	}
	return changed
}
