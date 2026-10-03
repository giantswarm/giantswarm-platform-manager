package plan

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// MissingFacts are the facts of the record the render needs (render.Input
// Facts) that the installation's record leaves empty: File is the record,
// repository:path, Missing the facts in the definition's order, Error why
// the record could not be read — every fact counts as missing then.
type MissingFacts struct {
	File    string        `json:"file"`
	Missing []render.Fact `json:"missing"`
	Error   string        `json:"error,omitempty"`
}

// recordRef is the installation's record in its configs repository, the file
// the facts are read from.
func recordRef(opts Options) fileRef {
	configs := ResolveRepository("giantswarm/"+opts.Installation.Customer+"-configs", opts.Installation, opts.Hub)
	return fileRef{configs, render.RecordPath(opts.Installation.Name)}
}

// missingFacts reads the record at ref and answers the facts it leaves empty,
// nil where it sets every one. The shared default carries such a key empty,
// so an empty value is missing alike.
func missingFacts(ctx context.Context, read Reader, ref fileRef, facts []render.Fact) *MissingFacts {
	if len(facts) == 0 {
		return nil
	}
	m := &MissingFacts{File: ref.repository + ":" + ref.path}
	data, err := read(ctx, ref.repository, ref.path)
	var record map[string]any
	if err == nil {
		err = yaml.Unmarshal([]byte(data), &record)
	}
	if err != nil {
		m.Missing, m.Error = facts, err.Error()
		return m
	}
	for _, f := range facts {
		if !setIn(record, strings.Split(f.Key, ".")) {
			m.Missing = append(m.Missing, f)
		}
	}
	if len(m.Missing) == 0 {
		return nil
	}
	return m
}

// setIn says whether doc holds a non-empty scalar at path.
func setIn(doc map[string]any, path []string) bool {
	v, ok := doc[path[0]]
	if !ok || v == nil {
		return false
	}
	if len(path) > 1 {
		next, isMap := v.(map[string]any)
		return isMap && setIn(next, path[1:])
	}
	switch v.(type) {
	case map[string]any, []any:
		return false
	}
	return fmt.Sprint(v) != ""
}

// FactsRefusal says why a commit of p is refused for the record's facts: the
// render needs a fact the installation's record leaves empty — the
// installation's configuration renders values from it that the definition
// never writes, and the chart refuses the install without them. It names the
// record, each missing key and where its value comes from. Empty where the
// record sets every fact the render needs. The comparison runs either way;
// only the commit is held.
func (p Installation) FactsRefusal() string {
	m := p.MissingFacts
	if m == nil || len(m.Missing) == 0 {
		return ""
	}
	keys := make([]string, len(m.Missing))
	sources := make([]string, len(m.Missing))
	var renders []string
	for i, f := range m.Missing {
		keys[i] = f.Key
		sources[i] = f.Key + " is " + f.Source
		if !slices.Contains(renders, f.Renders) {
			renders = append(renders, f.Renders)
		}
	}
	if m.Error != "" {
		return fmt.Sprintf("the record %s, which has to set %s, could not be read: %s", m.File, strings.Join(keys, " and "), m.Error)
	}
	it := "it"
	if len(keys) > 1 {
		it = "them"
	}
	return fmt.Sprintf("the record %s leaves %s empty; the installation's configuration renders %s from %s, and the chart refuses the install without %s. Set %s there first: %s",
		m.File, strings.Join(keys, " and "), strings.Join(renders, " and "), it, it, it, strings.Join(sources, "; "))
}
