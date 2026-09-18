package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// kvFlag collects repeated --input key=value flags.
type kvFlag []string

func (k *kvFlag) String() string { return strings.Join(*k, ",") }

func (k *kvFlag) Set(v string) error {
	if key, _, ok := strings.Cut(v, "="); !ok || key == "" {
		return fmt.Errorf("--input takes key=value, got %q", v)
	}
	*k = append(*k, v)
	return nil
}

// nest turns key=value pairs into the tool's `inputs` object: a dotted key is
// a path into nested mappings, a value that parses as JSON is that value
// (true, 3, ["hazel"], {"a": 1}, "quoted"), anything else is a string. The
// definition's schema, not this function, says what the keys and types are.
func nest(pairs []string) (map[string]any, error) {
	out := map[string]any{}
	for _, p := range pairs {
		key, val, _ := strings.Cut(p, "=")
		path := strings.Split(key, ".")
		m := out
		for i, seg := range path[:len(path)-1] {
			if seg == "" {
				return nil, fmt.Errorf("--input %s: empty key segment", key)
			}
			next, ok := m[seg]
			if !ok {
				n := map[string]any{}
				m[seg] = n
				m = n
				continue
			}
			n, ok := next.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("--input %s: %s is already a value, not a mapping", key, strings.Join(path[:i+1], "."))
			}
			m = n
		}
		last := path[len(path)-1]
		if last == "" {
			return nil, fmt.Errorf("--input %s: empty key segment", key)
		}
		if _, exists := m[last]; exists {
			return nil, fmt.Errorf("--input %s: given twice, or already a mapping", key)
		}
		m[last] = value(val)
	}
	return out, nil
}

func value(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err == nil {
		return v
	}
	return s
}
