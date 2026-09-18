// Package render is the core of the capability render library: the types every
// capability definition produces and the helpers they share.
//
// The library has no I/O. A definition takes an installation's inputs and
// returns a Result: the files of the installation's GitOps repositories,
// repository by repository-relative path, the entries a shared kustomization
// must list, and, as data, the probes of the running installation and the
// actions left to people outside the platform team. It runs no git, no sops
// and no network, executes no probe, and it generates no secret: a Secret
// manifest is emitted in plaintext with a placeholder per generated value,
// described in File.Generated for the commit step (gitops-commit's sopsenc)
// to fill in and encrypt.
package render

import (
	"bytes"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// Repository names a GitHub repository as "owner/name".
type Repository string

// Fileset maps a repository to its files by repository-relative path.
type Fileset map[Repository]map[string]File

// File is one rendered file. Content is complete plaintext; Generated lists the
// placeholders in it that stand for values the commit step generates.
type File struct {
	Content   []byte
	Generated []Generated
}

// Generated describes one placeholder in a File. Two files that carry the same
// Name receive the same value: that is how a Dex client and the workload that
// presents its secret agree on it.
type Generated struct {
	Name        string
	Placeholder string
	Kind        GeneratedKind
	Length      int
}

// GeneratedKind is the shape of a generated value.
type GeneratedKind string

const (
	// Base64 is Length random bytes, standard base64.
	Base64 GeneratedKind = "base64"
	// Alphanumeric is Length characters of [A-Za-z0-9].
	Alphanumeric GeneratedKind = "alphanumeric"
)

// Include is an entry a kustomization.yaml the definition does not own must
// list for the rendered files to take effect: the installation's
// extras/kustomization.yaml naming ./agent-platform/, for example. The engine
// adds it on enable and removes it on disable; the file itself is never
// rendered, because other owners write to it.
type Include struct {
	Repository Repository
	Path       string
	Resource   string
}

// Result is what a definition renders for one installation.
type Result struct {
	Files    Fileset
	Includes []Include
	Probes   []Probe  // what the verify slice checks on the running installation, in order
	Actions  []Action // what a person outside the platform team still has to do
}

// Probe is one check of the running installation, as data: the definition
// describes it, the verify slice executes it. ID is the live dimension of the
// definition's features.yaml it observes; Feature its feature.
type Probe struct {
	ID        string      `yaml:"id"`
	Feature   string      `yaml:"feature"`
	Kind      ProbeKind   `yaml:"kind"`
	Namespace string      `yaml:"namespace,omitempty"` // resource probes: the object's namespace
	Resource  string      `yaml:"resource,omitempty"`  // resource probes: kind or kind.group, e.g. "HelmRelease", "Cluster.postgresql.cnpg.io"
	Name      string      `yaml:"name,omitempty"`      // resource probes: the object's name (or a pod label selector, "app=…")
	URL       string      `yaml:"url,omitempty"`       // HTTP probes: the address
	Expect    Expectation `yaml:"expect,omitempty"`    // what the probe expects; empty where existence or readiness is the check
}

// ProbeKind is what a probe does.
type ProbeKind string

const (
	// HelmReleaseReady is a HelmRelease whose Ready condition is True.
	HelmReleaseReady ProbeKind = "HelmReleaseReady"
	// PodsRunning is every pod the selector in Name matches being Running.
	PodsRunning ProbeKind = "PodsRunning"
	// ResourcePresent is an object that exists; a Secret carries Expect.Keys.
	ResourcePresent ProbeKind = "ResourcePresent"
	// Condition is an object whose condition Expect.Condition has Expect.ConditionStatus.
	Condition ProbeKind = "Condition"
	// HTTP is a GET of URL, redirects not followed, answered as Expect says.
	HTTP ProbeKind = "HTTP"
	// LogAbsent is a workload whose log matches Expect.Absent nowhere.
	LogAbsent ProbeKind = "LogAbsent"
	// Drift is a live object whose values equal what the rendered files imply.
	Drift ProbeKind = "Drift"
)

// Expectation is what a probe expects; the fields its kind does not read stay zero.
type Expectation struct {
	Status           int      `yaml:"status,omitempty"`           // HTTP: the status code
	LocationContains string   `yaml:"locationContains,omitempty"` // HTTP: a substring of the Location header (302 probes)
	BodyContains     string   `yaml:"bodyContains,omitempty"`     // HTTP: a substring of the body (200 probes)
	Condition        string   `yaml:"condition,omitempty"`        // Condition: the type, e.g. Accepted, Ready
	ConditionStatus  string   `yaml:"conditionStatus,omitempty"`  // Condition: True or False
	Absent           string   `yaml:"absent,omitempty"`           // LogAbsent: a pattern that must not appear in the workload's log
	Keys             []string `yaml:"keys,omitempty"`             // ResourcePresent on a Secret: the keys it carries
	Note             string   `yaml:"note,omitempty"`             // one sentence a person reads next to the mark (e.g. what a False means)
}

// Action is something outside the platform team's hands, rendered as a note
// with a state. Feature is the features.yaml feature it holds up.
type Action struct {
	ID      string      `yaml:"id"`
	Feature string      `yaml:"feature"`
	State   ActionState `yaml:"state"`
	Note    string      `yaml:"note"` // what to do, in one sentence
}

// ActionState is where an action stands.
type ActionState string

// WaitingForCustomer is an action whose next step is the customer's.
const WaitingForCustomer ActionState = "WaitingForCustomer"

// Add puts a file into the result. A path rendered twice is a bug in the
// definition and panics.
func (r *Result) Add(repo Repository, path string, f File) {
	if r.Files == nil {
		r.Files = Fileset{}
	}
	if r.Files[repo] == nil {
		r.Files[repo] = map[string]File{}
	}
	if _, dup := r.Files[repo][path]; dup {
		panic(fmt.Sprintf("render: %s: %s rendered twice", repo, path))
	}
	r.Files[repo][path] = f
}

// Include records an entry a shared kustomization must carry.
func (r *Result) Include(repo Repository, path, resource string) {
	r.Includes = append(r.Includes, Include{Repository: repo, Path: path, Resource: resource})
}

// Len is the number of files across every repository.
func (r *Result) Len() int {
	n := 0
	for _, files := range r.Files {
		n += len(files)
	}
	return n
}

// Paths lists every file as "owner/name:path", sorted.
func (f Fileset) Paths() []string {
	var out []string
	for repo, files := range f {
		for path := range files {
			out = append(out, string(repo)+":"+path)
		}
	}
	sort.Strings(out)
	return out
}

// Placeholder is the text that stands for the generated value name in a
// rendered file. It is deliberately unlike a credential so secret scanners on
// the pull request stay quiet, and unique enough for the commit step to
// replace it verbatim.
func Placeholder(name string) string {
	return "GENERATED(" + name + ")"
}

// YAML marshals v with two-space indentation, the shape the fleet's
// hand-written files use. Struct field order is the key order.
func YAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MustYAML is YAML for values a definition builds itself, where an encoding
// error is a programming error.
func MustYAML(v any) []byte {
	b, err := YAML(v)
	if err != nil {
		panic(err)
	}
	return b
}

// SecretKey is one stringData entry of a Secret manifest: either a Generated
// value the commit step creates, or a Value the caller supplied.
type SecretKey struct {
	Key       string
	Generated *Generated
	Value     string
}

// GeneratedKey is a SecretKey whose value the commit step generates.
func GeneratedKey(key, name string, kind GeneratedKind, length int) SecretKey {
	return SecretKey{Key: key, Generated: &Generated{Name: name, Placeholder: Placeholder(name), Kind: kind, Length: length}}
}

// ValueKey is a SecretKey with a value the caller supplied.
func ValueKey(key, value string) SecretKey {
	return SecretKey{Key: key, Value: value}
}

// Secret renders an Opaque Secret manifest in plaintext. Keys keep their
// order; generated keys carry their placeholder and are listed in
// File.Generated.
func Secret(name, namespace string, labels map[string]string, keys ...SecretKey) File {
	type metadata struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels,omitempty"`
	}
	type secret struct {
		APIVersion string   `yaml:"apiVersion"`
		Kind       string   `yaml:"kind"`
		Metadata   metadata `yaml:"metadata"`
		Type       string   `yaml:"type"`
		StringData Map      `yaml:"stringData"`
	}
	f := File{}
	data := Map{}
	for _, k := range keys {
		value := k.Value
		if k.Generated != nil {
			value = k.Generated.Placeholder
			f.Generated = append(f.Generated, *k.Generated)
		}
		data = append(data, Entry{Key: k.Key, Value: value})
	}
	f.Content = MustYAML(secret{
		APIVersion: "v1", Kind: "Secret",
		Metadata: metadata{Name: name, Namespace: namespace, Labels: labels},
		Type:     "Opaque", StringData: data,
	})
	return f
}

// Map is an ordered YAML mapping; a definition builds the fleet's files with
// keys in the order a person would write them.
type Map []Entry

// Entry is one key of a Map.
type Entry struct {
	Key   string
	Value any
}

// MarshalYAML renders the Map as a mapping node in order.
func (m Map) MarshalYAML() (any, error) {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, e := range m {
		key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.Key}
		value := &yaml.Node{}
		if err := value.Encode(e.Value); err != nil {
			return nil, err
		}
		node.Content = append(node.Content, key, value)
	}
	return node, nil
}

// LineComment appends "# comment" to the first line of doc whose key is key
// (the line's first token, indentation ignored), for markers such as
// "# gitleaks:allow" on a value a secret scanner would otherwise flag.
func LineComment(doc []byte, key, comment string) []byte {
	lines := bytes.Split(doc, []byte("\n"))
	for i, line := range lines {
		if bytes.HasPrefix(bytes.TrimLeft(line, " "), []byte(key+":")) {
			lines[i] = append(line, []byte(" # "+comment)...)
			break
		}
	}
	return bytes.Join(lines, []byte("\n"))
}
