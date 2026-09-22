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

// SourcePerson is the x-source of an input the person chooses.
const SourcePerson = "person"

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
	// ReadBackInstallation is the installation whose base domain the host
	// of the URL the key holds is, once the service label the prefix names
	// is stripped: muster.<baseDomain> is the installation whose muster it
	// is. Resolved among the registry's installations and the installation
	// read for; nothing when it is no installation's.
	ReadBackInstallation = "installation"
	// ReadBackFile is whether the file itself is on record; it names no key.
	ReadBackFile = "file"
)

// schemaNode is the part of a schema node the read-back reads: the
// properties below it, its default, its source and its x-readback.
type schemaNode struct {
	Properties map[string]schemaNode `json:"properties"`
	Default    json.RawMessage       `json:"default"`
	Source     string                `json:"x-source"`
	ReadBack   *readBackSpec         `json:"x-readback"`
}

// readBackSpec is an x-readback: the fileset key of the file the value is
// read from (an entry of the schema's x-files), the path in it — one, or
// several the record may carry the value under, the first on record
// answering — the kind, and the prefix the value carries in the file,
// stripped from the value read back (a value without it yields nothing; of
// a host kind it is the host's leading label). A step of the path into a
// list is its index or a [key=value] selector, the entry whose key has the
// value; a step into YAML text (a ConfigMap's values, a kustomization's
// patch) decodes it.
type readBackSpec struct {
	File   string `json:"file"`
	Keys   keys   `json:"key"`
	Kind   string `json:"kind"`
	Prefix string `json:"prefix"`
}

// keys is x-readback's key: a path, or a list of paths.
type keys []string

func (k *keys) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*k = keys{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return fmt.Errorf("x-readback key %s: a path or a list of paths", raw)
	}
	*k = many
	return nil
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

// leaves visits every leaf of the schema below the top level, by dotted
// path, in key order; installation.* (the facts) takes no part.
func (s *inputSchema) leaves(visit func(path string, leaf schemaNode) error) error {
	var walk func(n schemaNode, path []string) error
	walk = func(n schemaNode, path []string) error {
		names := make([]string, 0, len(n.Properties))
		for k := range n.Properties {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			child := n.Properties[k]
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
			if err := visit(strings.Join(p, "."), child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(s.schemaNode, nil)
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
	return out, s.leaves(func(path string, leaf schemaNode) error {
		if len(leaf.Default) == 0 {
			return nil
		}
		var v any
		if err := json.Unmarshal(leaf.Default, &v); err != nil {
			return fmt.Errorf("%s: schema: %s: default: %w", c.Name, path, err)
		}
		set(out, splitKey(path), v)
		return nil
	})
}

// PersonInputs are the inputs the person chooses: the schema's leaves
// marked x-source person, by dotted key, sorted.
func (c Capability) PersonInputs() ([]string, error) {
	s, err := c.inputSchema()
	if err != nil {
		return nil, err
	}
	var out []string
	return out, s.leaves(func(path string, leaf schemaNode) error {
		if leaf.Source == SourcePerson {
			out = append(out, path)
		}
		return nil
	})
}

// Unset names the person inputs that hold no value in values — the choices
// not on record: no default, nothing read back, nothing typed — by dotted
// key, sorted.
func (c Capability) Unset(values map[string]any) ([]string, error) {
	fields, err := c.PersonInputs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range fields {
		if v, ok := lookup(values, splitKey(f)); !ok || v == nil {
			out = append(out, f)
		}
	}
	return out, nil
}

// ReadBack reads every input the schema marks x-readback from the files on
// record of inst, as the person read reads as, and answers what it found by
// dotted input key: the leaf's value, whether the key is present, the host
// of the URL it holds, the installation whose host it is, or whether the
// file is on record. registry is every installation on record, the ones an
// installation kind resolves a host to; inst is resolved whether or not it
// is among them. A file that is not on record, or a key not in it, yields
// nothing — the default stands. A file the person cannot read, or a
// read-back naming a file the schema's x-files does not, is the error.
func (c Capability) ReadBack(ctx context.Context, read Reader, inst Installation, registry []Installation) (map[string]any, error) {
	s, err := c.inputSchema()
	if err != nil {
		return nil, err
	}
	return readBack(ctx, read, inst, registry, s)
}

func readBack(ctx context.Context, read Reader, inst Installation, registry []Installation, s *inputSchema) (map[string]any, error) {
	docs := map[string]map[string]any{}
	out := map[string]any{}
	return out, s.leaves(func(input string, leaf schemaNode) error {
		rb := leaf.ReadBack
		if rb == nil {
			return nil
		}
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
			return nil
		}
		if doc == nil {
			return nil
		}
		v, found := lookupFirst(doc, rb.Keys)
		if found && (kind == ReadBackHost || kind == ReadBackInstallation) {
			raw, _ := v.(string)
			v, found = hostOf(raw), hostOf(raw) != ""
		}
		if raw, isText := v.(string); found && rb.Prefix != "" {
			if found = isText && strings.HasPrefix(raw, rb.Prefix); found {
				v = strings.TrimPrefix(raw, rb.Prefix)
			}
		}
		switch kind {
		case ReadBackValue, ReadBackHost:
			if found {
				out[input] = v
			}
		case ReadBackPresent:
			out[input] = found
		case ReadBackInstallation:
			if domain, _ := v.(string); found {
				if name, ok := installationOf(domain, inst, registry); ok {
					out[input] = name
				}
			}
		default:
			return fmt.Errorf("schema: %s: x-readback kind %q is not %s, %s, %s, %s or %s", input, kind, ReadBackValue, ReadBackPresent, ReadBackHost, ReadBackInstallation, ReadBackFile)
		}
		return nil
	})
}

// installationOf is the installation whose base domain domain is: inst
// when it is inst's, else the first by name among the registry's; none
// when it is no installation's.
func installationOf(domain string, inst Installation, registry []Installation) (string, bool) {
	if domain == "" {
		return "", false
	}
	if inst.BaseDomain == domain {
		return inst.Name, true
	}
	name := ""
	for _, r := range registry {
		if r.BaseDomain == domain && (name == "" || r.Name < name) {
			name = r.Name
		}
	}
	return name, name != ""
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
		v, _ := lookup(doc, splitKey(spec.Document))
		doc, _ = decoded(v).(map[string]any)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	docs[file] = doc
	return doc, nil
}

// lookupFirst is the value at the first of the dotted paths that doc
// holds.
func lookupFirst(doc map[string]any, paths []string) (any, bool) {
	for _, p := range paths {
		if v, ok := lookup(doc, splitKey(p)); ok {
			return v, true
		}
	}
	return nil, false
}

// splitKey splits a dotted path into its steps, a [key=value] selector
// kept whole whatever it holds.
func splitKey(key string) []string {
	var steps []string
	depth, start := 0, 0
	for i, r := range key {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth--
		case r == '.' && depth == 0:
			steps = append(steps, key[start:i])
			start = i + 1
		}
	}
	return append(steps, key[start:])
}

// lookup walks a decoded YAML document along path: a mapping by key, a
// list by index or by a [key=value] selector, YAML text by what it decodes
// to.
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
		if key, value, isSelector := selector(k); isSelector {
			for _, entry := range c {
				if m, ok := entry.(map[string]any); ok && m[key] != nil && fmt.Sprint(m[key]) == value {
					return entry, true
				}
			}
			return nil, false
		}
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

// selector reads a [key=value] step: the key and the value an entry of a
// list must carry.
func selector(k string) (key, value string, ok bool) {
	if !strings.HasPrefix(k, "[") || !strings.HasSuffix(k, "]") {
		return "", "", false
	}
	key, value, ok = strings.Cut(k[1:len(k)-1], "=")
	return key, value, ok && key != ""
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
