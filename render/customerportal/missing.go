package customerportal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The required person inputs: what the schema's required lists name with
// x-source person, and the choices the definition needs under a condition
// the lists cannot express. Parse fills every one the document lacks and
// names it: a string with its Missing marker, which a comparison renders
// and compares every leaf that carries it as not checked; a boolean with
// false, so the section it switches on is not rendered (a boolean has no
// marker form, and one is absent only while the file it reads back from is
// not on record). A commit refuses every one by field. Nothing is decided
// per field: a new required person input of the schema, or a new row of
// conditionalChoices, follows.

// conditionalChoices are the person inputs required when another input is
// true: field when the boolean at when is set. None today: every plugin's
// switch stands alone, its values derived or supplied at commit.
var conditionalChoices []struct{ when, field string }

// sourcePerson is the x-source of an input the person types.
const sourcePerson = "person"

// schemaNode is the schema as the input layer reads it: the properties, the
// required ones, and each leaf's source and type.
type schemaNode struct {
	Properties map[string]schemaNode `json:"properties"`
	Required   []string              `json:"required"`
	Source     string                `json:"x-source"`
	Type       string                `json:"type"`
}

// fillMissing fills every required person input that doc lacks — a string
// with its Missing marker, a boolean with false, a required object with an
// empty one, to walk on — and answers the fields filled, sorted. A required
// input of another source or type is left to the validator's refusal.
func fillMissing(doc map[string]any, schema []byte) ([]string, error) {
	var s schemaNode
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("customer-portal: schema: %w", err)
	}
	var out []string
	var walk func(n schemaNode, doc map[string]any, path []string)
	walk = func(n schemaNode, doc map[string]any, path []string) {
		for _, k := range n.Required {
			child, known := n.Properties[k]
			if _, held := doc[k]; held || !known {
				continue
			}
			field := strings.Join(append(append([]string{}, path...), k), ".")
			switch {
			case len(child.Properties) > 0:
				doc[k] = map[string]any{}
			case child.Source == sourcePerson && child.Type == "string":
				doc[k] = render.Missing(field)
				out = append(out, field)
			case child.Source == sourcePerson && child.Type == "boolean":
				doc[k] = false
				out = append(out, field)
			}
		}
		for k, child := range n.Properties {
			if sub, ok := doc[k].(map[string]any); ok && len(child.Properties) > 0 {
				walk(child, sub, append(append([]string{}, path...), k))
			}
		}
	}
	walk(s, doc, nil)
	for _, c := range conditionalChoices {
		if on, _ := lookup(doc, c.when).(bool); !on {
			continue
		}
		if _, held := lookupIn(doc, c.field); held {
			continue
		}
		parent, _ := lookupIn(doc, c.field[:strings.LastIndex(c.field, ".")])
		if m, ok := parent.(map[string]any); ok {
			m[c.field[strings.LastIndex(c.field, ".")+1:]] = render.Missing(c.field)
			out = append(out, c.field)
		}
	}
	sort.Strings(out)
	return out, nil
}

// lookup is the value at the dotted path in doc; nil when absent.
func lookup(doc map[string]any, path string) any {
	v, _ := lookupIn(doc, path)
	return v
}

// lookupIn is the value at the dotted path in doc and whether it is held.
func lookupIn(doc map[string]any, path string) (any, bool) {
	var v any = doc
	for _, k := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

// explained reports whether every leaf of a validation error is at a field
// filled with a Missing marker: a marker fails the constraints of the value
// it stands for (a pattern, a format) by design, and that is no refusal.
func explained(err *jsonschema.ValidationError, filled map[string]bool) bool {
	if len(err.Causes) == 0 {
		return filled[strings.Join(err.InstanceLocation, ".")]
	}
	for _, c := range err.Causes {
		if !explained(c, filled) {
			return false
		}
	}
	return true
}

// notOnRecord is a commit's refusal of the required person inputs the
// document lacks, one sentence: what each is and its key, and that the
// person supplies them under Apply changes — "the portal's hostname
// (portal.domain) and the organisation's name as the portal shows it
// (portal.organization) are not on record; supply them under Apply changes".
func notOnRecord(missing []string) string {
	names := make([]string, 0, len(missing))
	for _, field := range missing {
		names = append(names, describe(field))
	}
	if len(missing) == 1 {
		return names[0] + " is not on record; supply it under Apply changes"
	}
	return render.List(names) + " are not on record; supply them under Apply changes"
}
