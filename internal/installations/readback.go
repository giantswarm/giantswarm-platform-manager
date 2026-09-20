package installations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// InputsInstallation is the key of the input document the installation's
// facts sit under: the schema's installation.* entries.
const InputsInstallation = "installation"

// Reader reads a file of a repository as the person; gh.ErrNotFound when
// the file is not there.
type Reader func(ctx context.Context, repository, path string) (string, error)

// The kinds of a read-back: what x-readback takes from the key it names.
const (
	// ReadBackValue is the key's value, the default kind.
	ReadBackValue = "value"
	// ReadBackPresent is true when the key exists, false when it does not.
	ReadBackPresent = "present"
	// ReadBackHost is the host of the URL the key holds.
	ReadBackHost = "host"
	// ReadBackFile is whether the file itself is on record; it names no key.
	ReadBackFile = "file"
)

// schemaNode is the part of a schema node the read-back reads: the
// properties below it, its default, and its x-readback — {file, key, kind,
// prefix} naming the fileset key of the file the value is read from (an
// entry of the schema's x-files), the path in it, the kind, and the prefix
// the value carries in the file, stripped from the value read back (a value
// without it yields nothing). A step of the path into a list is its index,
// a step into YAML text (a ConfigMap's values, a kustomization's patch)
// decodes it.
type schemaNode struct {
	Properties map[string]schemaNode `json:"properties"`
	Default    json.RawMessage       `json:"default"`
	ReadBack   *struct {
		File   string `json:"file"`
		Key    string `json:"key"`
		Kind   string `json:"kind"`
		Prefix string `json:"prefix"`
	} `json:"x-readback"`
}

// fileSpec is one entry of the schema's x-files: the repository file the
// definition renders a fileset key to, <name> standing for the installation,
// and the path in the file the keys are read under — the document a
// ConfigMap carries as text; the file itself when empty.
type fileSpec struct {
	Repository MarkerRepository `json:"repository"`
	Path       string           `json:"path"`
	Document   string           `json:"document"`
}

// inputSchema is the schema as the read-back reads it: the input tree and
// the files its read-backs name.
type inputSchema struct {
	schemaNode
	Files map[string]fileSpec `json:"x-files"`
}

func (c Capability) inputSchema() (*inputSchema, error) {
	raw, err := c.Schema()
	if err != nil {
		return nil, err
	}
	var s inputSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s: schema: %w", c.Name, err)
	}
	return &s, nil
}

// Defaults is the input document the schema's defaults make: every leaf
// below the top level that declares one, at its path; installation.* (the
// facts) takes no part.
func (c Capability) Defaults() (map[string]any, error) {
	s, err := c.inputSchema()
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	var walk func(n schemaNode, path []string) error
	walk = func(n schemaNode, path []string) error {
		for k, child := range n.Properties {
			p := append(append([]string{}, path...), k)
			if len(p) == 1 && k == InputsInstallation {
				continue
			}
			if len(child.Properties) > 0 {
				if err := walk(child, p); err != nil {
					return err
				}
				continue
			}
			if len(child.Default) == 0 {
				continue
			}
			var v any
			if err := json.Unmarshal(child.Default, &v); err != nil {
				return fmt.Errorf("%s: schema: %s: default: %w", c.Name, strings.Join(p, "."), err)
			}
			set(out, p, v)
		}
		return nil
	}
	return out, walk(s.schemaNode, nil)
}

// ReadBack reads every input the schema marks x-readback from the files on
// record of inst, as the person read reads as, and answers what it found by
// dotted input key: the leaf's value, whether the key is present, the host
// of the URL it holds, or whether the file is on record. A file that is not
// on record, or a key not in it, yields nothing — the default stands. A file
// the person cannot read, or a read-back naming a file the schema's x-files
// does not, is the error.
func (c Capability) ReadBack(ctx context.Context, read Reader, inst Installation) (map[string]any, error) {
	s, err := c.inputSchema()
	if err != nil {
		return nil, err
	}
	return readBack(ctx, read, inst, s)
}

func readBack(ctx context.Context, read Reader, inst Installation, s *inputSchema) (map[string]any, error) {
	docs := map[string]map[string]any{}
	out := map[string]any{}
	var walk func(n schemaNode, path []string) error
	walk = func(n schemaNode, path []string) error {
		keys := make([]string, 0, len(n.Properties))
		for k := range n.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			child := n.Properties[k]
			p := append(append([]string{}, path...), k)
			if len(child.Properties) > 0 {
				if err := walk(child, p); err != nil {
					return err
				}
				continue
			}
			rb := child.ReadBack
			if rb == nil {
				continue
			}
			input := strings.Join(p, ".")
			spec, ok := s.Files[rb.File]
			if !ok {
				return fmt.Errorf("schema: %s: x-readback names the file %q, which x-files does not declare", input, rb.File)
			}
			doc, err := readBackDoc(ctx, read, inst, rb.File, spec, docs)
			if err != nil {
				return err
			}
			kind := rb.Kind
			if kind == "" {
				kind = ReadBackValue
			}
			if kind == ReadBackFile {
				out[input] = doc != nil
				continue
			}
			if doc == nil {
				continue
			}
			v, found := lookup(doc, strings.Split(rb.Key, "."))
			if raw, isText := v.(string); found && rb.Prefix != "" {
				if found = isText && strings.HasPrefix(raw, rb.Prefix); found {
					v = strings.TrimPrefix(raw, rb.Prefix)
				}
			}
			switch kind {
			case ReadBackValue:
				if found {
					out[input] = v
				}
			case ReadBackPresent:
				out[input] = found
			case ReadBackHost:
				if raw, _ := v.(string); found && hostOf(raw) != "" {
					out[input] = hostOf(raw)
				}
			default:
				return fmt.Errorf("schema: %s: x-readback kind %q is not %s, %s, %s or %s", input, kind, ReadBackValue, ReadBackPresent, ReadBackHost, ReadBackFile)
			}
		}
		return nil
	}
	return out, walk(s.schemaNode, nil)
}

// readBackDoc is the decoded file of fileset key file for inst, read once
// per read-back, at the document spec names in it; nil when the file is not
// on record, empty when the document is not in it.
func readBackDoc(ctx context.Context, read Reader, inst Installation, file string, spec fileSpec, docs map[string]map[string]any) (map[string]any, error) {
	if doc, ok := docs[file]; ok {
		return doc, nil
	}
	repo := inst.Repositories.Configs
	if spec.Repository == ManagementClustersRepository {
		repo = inst.Repositories.ManagementClusters
	}
	path := strings.ReplaceAll(spec.Path, "<name>", inst.Name)
	content, err := read(ctx, repo, path)
	if errors.Is(err, gh.ErrNotFound) {
		docs[file] = nil
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading back %s in %s: %w", path, repo, err)
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return nil, fmt.Errorf("reading back %s in %s: %w", path, repo, err)
	}
	if spec.Document != "" {
		v, _ := lookup(doc, strings.Split(spec.Document, "."))
		doc, _ = decoded(v).(map[string]any)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	docs[file] = doc
	return doc, nil
}

// lookup walks a decoded YAML document along path: a mapping by key, a
// list by index, YAML text by what it decodes to.
func lookup(doc map[string]any, path []string) (any, bool) {
	var cur any = doc
	for _, k := range path {
		var ok bool
		if cur, ok = step(cur, k); !ok {
			return nil, false
		}
	}
	return cur, true
}

// step is one step of a lookup into cur.
func step(cur any, k string) (any, bool) {
	switch c := cur.(type) {
	case map[string]any:
		v, ok := c[k]
		return v, ok
	case []any:
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= len(c) {
			return nil, false
		}
		return c[i], true
	case string:
		return step(decoded(c), k)
	}
	return nil, false
}

// decoded is v, YAML text decoded to what it holds; text that holds no
// mapping or list is nil.
func decoded(v any) any {
	text, isText := v.(string)
	if !isText {
		return v
	}
	var out any
	if err := yaml.Unmarshal([]byte(text), &out); err != nil {
		return nil
	}
	switch out.(type) {
	case map[string]any, []any:
		return out
	}
	return nil
}

// set puts v at path in doc, creating the mappings on the way.
func set(doc map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, ok := doc[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			doc[k] = next
		}
		doc = next
	}
	doc[path[len(path)-1]] = v
}
