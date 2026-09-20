package installations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
)

// schemaNode is the part of a schema node the read-back reads: the
// properties below it, its default, and its x-readback — {file, key, kind}
// naming the fileset key of the file the value is read from (an entry of
// the schema's x-files), the YAML path in it and the kind.
type schemaNode struct {
	Properties map[string]schemaNode `json:"properties"`
	Default    json.RawMessage       `json:"default"`
	ReadBack   *struct {
		File string `json:"file"`
		Key  string `json:"key"`
		Kind string `json:"kind"`
	} `json:"x-readback"`
}

// fileSpec is one entry of the schema's x-files: the repository file the
// definition renders a fileset key to, <name> standing for the installation.
type fileSpec struct {
	Repository MarkerRepository `json:"repository"`
	Path       string           `json:"path"`
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
// dotted input key: the leaf's value, whether the key is present, or the
// host of the URL it holds. A file that is not on record, or a key not in
// it, yields nothing — the default stands. A file the person cannot read, or
// a read-back naming a file the schema's x-files does not, is the error.
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
			if doc == nil {
				continue
			}
			v, found := lookup(doc, strings.Split(rb.Key, "."))
			kind := rb.Kind
			if kind == "" {
				kind = ReadBackValue
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
				return fmt.Errorf("schema: %s: x-readback kind %q is not %s, %s or %s", input, kind, ReadBackValue, ReadBackPresent, ReadBackHost)
			}
		}
		return nil
	}
	return out, walk(s.schemaNode, nil)
}

// readBackDoc is the decoded file of fileset key file for inst, read once
// per read-back; nil when the file is not on record.
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
	if doc == nil {
		doc = map[string]any{}
	}
	docs[file] = doc
	return doc, nil
}

// lookup walks a decoded YAML document along path.
func lookup(doc map[string]any, path []string) (any, bool) {
	var cur any = doc
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
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
