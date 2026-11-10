package agentplatform

import (
	"encoding/json"
	"fmt"
	"strings"
)

// renamedInputs are the inputs read under a former name: an action records
// every input it rendered from, so its record keeps the name it was written
// with, and verify_installation and watch_action parse that record again.
var renamedInputs = []struct{ from, to string }{
	{"agentManager.skills.appSecretName", "agentManager.skills.github.app.secretName"},
}

// renameInputs moves each input given under its former name to its current
// one. The same input under both names with different values is refused: no
// name wins silently.
func renameInputs(encoded []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil || doc == nil {
		return encoded, nil //nolint:nilerr // not an object: the schema reports it
	}
	moved := false
	for _, r := range renamedInputs {
		old, ok := take(doc, r.from)
		if !ok {
			continue
		}
		moved = true
		current, set := lookupPath(doc, r.to)
		if set && !isZero(current) && !isZero(old) && fmt.Sprint(current) != fmt.Sprint(old) {
			return nil, refuse(fmt.Sprintf("%s is the former name of %s, and the two are given different values (%v, %v); give %s only", r.from, r.to, old, current, r.to))
		}
		if !set || isZero(current) {
			if err := put(doc, r.to, old); err != nil {
				return nil, fmt.Errorf("%w: %s: %w", ErrInput, r.from, err)
			}
		}
	}
	if !moved {
		return encoded, nil
	}
	return json.Marshal(doc)
}

// take removes the value at the dotted path and returns it.
func take(doc map[string]any, path string) (any, bool) {
	parent, key, ok := parentOf(doc, path, false)
	if !ok {
		return nil, false
	}
	v, ok := parent[key]
	delete(parent, key)
	return v, ok
}

// lookupPath returns the value at the dotted path.
func lookupPath(doc map[string]any, path string) (any, bool) {
	parent, key, ok := parentOf(doc, path, false)
	if !ok {
		return nil, false
	}
	v, ok := parent[key]
	return v, ok
}

// put sets the value at the dotted path, creating the objects on the way.
func put(doc map[string]any, path string, v any) error {
	parent, key, ok := parentOf(doc, path, true)
	if !ok {
		return fmt.Errorf("%s crosses a value that is not an object", path)
	}
	parent[key] = v
	return nil
}

func parentOf(doc map[string]any, path string, create bool) (map[string]any, string, bool) {
	keys := strings.Split(path, ".")
	m := doc
	for _, k := range keys[:len(keys)-1] {
		next, ok := m[k]
		if !ok {
			if !create {
				return nil, "", false
			}
			next = map[string]any{}
			m[k] = next
		}
		if m, ok = next.(map[string]any); !ok {
			return nil, "", false
		}
	}
	return m, keys[len(keys)-1], true
}

func isZero(v any) bool {
	return v == nil || v == "" || v == false
}
