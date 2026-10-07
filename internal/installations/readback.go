package installations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
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
	// ReadBackComment is the comment written above the key: its lines, the
	// comment markers stripped; a key without one yields nothing.
	ReadBackComment = "comment"
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
// read from (an entry of the schema's x-files) — one, or several the record
// may carry the value in, the first on record that holds the key answering —
// the path in it — one, or several the record may carry the value under, the
// first on record answering — the kind, and the prefix the value carries in
// the file, stripped from the value read back (a value without it yields
// nothing; of a host kind it is the host's leading label). A step of the
// path into a list is its index or a [key=value] selector, the entry whose
// key — a dotted path into the entry ([target.kind=OCIRepository]) — has the
// value; a step into a mapping whose key carries dots is the key
// in brackets ([app-config.agent-platform.yaml]); a step into YAML text (a
// ConfigMap's values, a kustomization's patch) decodes it. Skip is a regular
// expression a value read back is matched against: a match yields nothing —
// the value the definition renders itself where the person chose nothing —
// of a comment kind, the comment the definition writes itself.
type readBackSpec struct {
	Files  names  `json:"file"`
	Keys   names  `json:"key"`
	Kind   string `json:"kind"`
	Prefix string `json:"prefix"`
	Skip   string `json:"skip"`
}

// names is x-readback's file or key: one, or a list.
type names []string

func (n *names) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*n = names{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return fmt.Errorf("x-readback %s: a name or a list of names", raw)
	}
	*n = many
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
// of the URL it holds, the installation whose host it is, the comment above
// the key, or whether the file is on record. registry is every installation on record, the ones an
// installation kind resolves a host to; inst is resolved whether or not it
// is among them. A file that is not on record, or a key not in it, yields
// nothing — the default stands; a read-back naming several files reads the
// first on record that holds the key, and the key is present when one of
// them holds it. A file the person cannot read, or a read-back naming a
// file the schema's x-files does not, is the error.
func (c Capability) ReadBack(ctx context.Context, read Reader, inst Installation, registry []Installation) (map[string]any, error) {
	s, err := c.inputSchema()
	if err != nil {
		return nil, err
	}
	return readBack(ctx, read, inst, registry, s)
}

func readBack(ctx context.Context, read Reader, inst Installation, registry []Installation, s *inputSchema) (map[string]any, error) {
	files := newReadBackFiles(ctx, read, inst)
	out := map[string]any{}
	return out, s.leaves(func(input string, leaf schemaNode) error {
		rb := leaf.ReadBack
		if rb == nil {
			return nil
		}
		kind := rb.Kind
		if kind == "" {
			kind = ReadBackValue
		}
		var v any
		var onRecord, found bool
		for _, file := range rb.Files {
			spec, ok := s.Files[file]
			if !ok {
				return fmt.Errorf("schema: %s: x-readback names the file %q, which x-files does not declare", input, file)
			}
			text, err := files.text(file, spec)
			if err != nil {
				return err
			}
			if text == nil {
				continue
			}
			onRecord = true
			if kind == ReadBackFile {
				break
			}
			if kind == ReadBackComment {
				v, found, err = files.comment(file, spec, rb.Keys)
			} else {
				v, found, err = files.value(file, spec, rb.Keys)
			}
			if err != nil {
				return err
			}
			if found {
				break
			}
		}
		if kind == ReadBackFile {
			out[input] = onRecord
			return nil
		}
		if !onRecord {
			return nil
		}
		if found && (kind == ReadBackHost || kind == ReadBackInstallation) {
			raw, _ := v.(string)
			v, found = hostOf(raw), hostOf(raw) != ""
		}
		if raw, isText := v.(string); found && rb.Prefix != "" {
			if found = isText && strings.HasPrefix(raw, rb.Prefix); found {
				v = strings.TrimPrefix(raw, rb.Prefix)
			}
		}
		if found && rb.Skip != "" {
			skip, err := regexp.Compile(rb.Skip)
			if err != nil {
				return fmt.Errorf("schema: %s: x-readback skip: %w", input, err)
			}
			found = !skip.MatchString(fmt.Sprint(v))
		}
		switch kind {
		case ReadBackValue, ReadBackHost, ReadBackComment:
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
			return fmt.Errorf("schema: %s: x-readback kind %q is not %s, %s, %s, %s, %s or %s", input, kind, ReadBackValue, ReadBackPresent, ReadBackHost, ReadBackInstallation, ReadBackComment, ReadBackFile)
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

// readBackFiles are the files on record one read-back reads for an
// installation, each read once and decoded once in each form a kind needs:
// as the document its x-files entry names, for a value; as the node tree,
// the comments in place, for a comment.
type readBackFiles struct {
	ctx   context.Context
	read  Reader
	inst  Installation
	texts map[string]*string
	docs  map[string]map[string]any
	nodes map[string]*yaml.Node
}

func newReadBackFiles(ctx context.Context, read Reader, inst Installation) *readBackFiles {
	return &readBackFiles{ctx: ctx, read: read, inst: inst, texts: map[string]*string{}, docs: map[string]map[string]any{}, nodes: map[string]*yaml.Node{}}
}

// location is the repository and path of the file spec declares for the
// installation.
func (f *readBackFiles) location(spec fileSpec) (repo, path string) {
	repo = f.inst.Repositories.Configs
	if spec.Repository == ManagementClustersRepository {
		repo = f.inst.Repositories.ManagementClusters
	}
	return repo, strings.ReplaceAll(spec.Path, "<name>", f.inst.Name)
}

// text is the content of the file of fileset key file, read once; nil when
// the file is not on record.
func (f *readBackFiles) text(file string, spec fileSpec) (*string, error) {
	if text, ok := f.texts[file]; ok {
		return text, nil
	}
	repo, path := f.location(spec)
	content, err := f.read(f.ctx, repo, path)
	if errors.Is(err, gh.ErrNotFound) {
		f.texts[file] = nil
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading back %s in %s: %w", path, repo, err)
	}
	f.texts[file] = &content
	return &content, nil
}

// value is the value at the first of the dotted paths the file holds, below
// the document spec names in it; nothing where the file is not on record or
// no path is in it.
func (f *readBackFiles) value(file string, spec fileSpec, paths []string) (any, bool, error) {
	doc, err := f.doc(file, spec)
	if err != nil || doc == nil {
		return nil, false, err
	}
	for _, p := range paths {
		if v, ok := lookup(doc, splitKey(p)); ok {
			return v, true, nil
		}
	}
	return nil, false, nil
}

// doc is the decoded file at the document spec names in it, decoded once;
// nil when the file is not on record, empty when the document is not in it.
func (f *readBackFiles) doc(file string, spec fileSpec) (map[string]any, error) {
	if doc, ok := f.docs[file]; ok {
		return doc, nil
	}
	text, err := f.text(file, spec)
	if err != nil || text == nil {
		return nil, err
	}
	doc := map[string]any{}
	if err := yaml.Unmarshal([]byte(*text), &doc); err != nil {
		repo, path := f.location(spec)
		return nil, fmt.Errorf("reading back %s in %s: %w", path, repo, err)
	}
	if spec.Document != "" {
		v, _ := lookup(doc, splitKey(spec.Document))
		doc, _ = decoded(v).(map[string]any)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	f.docs[file] = doc
	return doc, nil
}

// comment is the comment above the first of the dotted paths the file holds
// a comment at, below the document spec names in it, its lines' comment
// markers stripped; nothing where the file is not on record, no path is in
// it or the key carries none.
func (f *readBackFiles) comment(file string, spec fileSpec, paths []string) (any, bool, error) {
	node, err := f.node(file, spec)
	if err != nil || node == nil {
		return nil, false, err
	}
	var document []string
	if spec.Document != "" {
		document = splitKey(spec.Document)
	}
	for _, p := range paths {
		if c, ok := commentAt(node, append(slices.Clone(document), splitKey(p)...)); ok {
			return c, true, nil
		}
	}
	return nil, false, nil
}

// node is the file as its YAML node tree, comments in place, decoded once;
// nil when the file is not on record.
func (f *readBackFiles) node(file string, spec fileSpec) (*yaml.Node, error) {
	if node, ok := f.nodes[file]; ok {
		return node, nil
	}
	text, err := f.text(file, spec)
	if err != nil || text == nil {
		return nil, err
	}
	node := &yaml.Node{}
	if err := yaml.Unmarshal([]byte(*text), node); err != nil {
		repo, path := f.location(spec)
		return nil, fmt.Errorf("reading back %s in %s: %w", path, repo, err)
	}
	f.nodes[file] = node
	return node, nil
}

// commentAt is the comment above the node at path in n, walked as lookup
// walks a document — a mapping by key, a list by index or by a [key=value]
// selector, YAML text by what it decodes to — as the person wrote it: the
// marker and the space after it stripped from every line, the empty lines
// at either end dropped. A mapping key's is the comment above the key, a
// list entry's the comment above the entry. Nothing where the path is not
// in n or the node carries no comment.
func commentAt(n *yaml.Node, path []string) (string, bool) {
	cur, comment := n, ""
	for _, k := range path {
		var ok bool
		if cur, comment, ok = stepNode(cur, k); !ok {
			return "", false
		}
	}
	lines := strings.Split(comment, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(line), "#"), " ")
	}
	for len(lines) > 0 && lines[0] == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n"), len(lines) > 0
}

// stepNode is one step of commentAt into cur: the node the step reaches and
// the comment above it.
func stepNode(cur *yaml.Node, k string) (*yaml.Node, string, bool) {
	switch cur.Kind {
	case yaml.DocumentNode:
		if len(cur.Content) == 0 {
			return nil, "", false
		}
		return stepNode(cur.Content[0], k)
	case yaml.MappingNode:
		if literal, ok := bracketed(k); ok {
			k = literal
		}
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == k {
				return cur.Content[i+1], cur.Content[i].HeadComment, true
			}
		}
	case yaml.SequenceNode:
		if key, value, isSelector := selector(k); isSelector {
			for _, entry := range cur.Content {
				if v, ok := nodeAt(entry, strings.Split(key, ".")); ok && v.Kind == yaml.ScalarNode && v.Value == value {
					return entry, entry.HeadComment, true
				}
			}
			return nil, "", false
		}
		if i, err := strconv.Atoi(k); err == nil && i >= 0 && i < len(cur.Content) {
			return cur.Content[i], cur.Content[i].HeadComment, true
		}
	case yaml.ScalarNode:
		// YAML text: a step into what it decodes to, where that is a mapping
		// or a list; text that holds a scalar is the end of the walk.
		text := &yaml.Node{}
		if err := yaml.Unmarshal([]byte(cur.Value), text); err != nil || len(text.Content) == 0 || text.Content[0].Kind == yaml.ScalarNode {
			return nil, "", false
		}
		return stepNode(text.Content[0], k)
	}
	return nil, "", false
}

// nodeAt is the node at path in n.
func nodeAt(n *yaml.Node, path []string) (*yaml.Node, bool) {
	for _, k := range path {
		var ok bool
		if n, _, ok = stepNode(n, k); !ok {
			return nil, false
		}
	}
	return n, true
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

// lookup walks a decoded YAML document along path: a mapping by key (a key
// with dots in it bracketed), a list by index or by a [key=value] selector,
// YAML text by what it decodes to.
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
		if literal, ok := bracketed(k); ok {
			k = literal
		}
		v, ok := c[k]
		return v, ok
	case []any:
		if key, value, isSelector := selector(k); isSelector {
			for _, entry := range c {
				m, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				if v, ok := lookup(m, strings.Split(key, ".")); ok && v != nil && fmt.Sprint(v) == value {
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

// selector reads a [key=value] step: the key, a dotted path into the entry,
// and the value an entry of a list must carry there.
func selector(k string) (key, value string, ok bool) {
	inner, isBracketed := bracketed(k)
	if !isBracketed {
		return "", "", false
	}
	key, value, ok = strings.Cut(inner, "=")
	return key, value, ok && key != ""
}

// bracketed reads a [literal] step: the mapping key it stands for, with
// the dots splitKey would otherwise split it on.
func bracketed(k string) (string, bool) {
	if len(k) < 2 || !strings.HasPrefix(k, "[") || !strings.HasSuffix(k, "]") {
		return "", false
	}
	return k[1 : len(k)-1], true
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
