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
	"strings"

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
// presents its secret agree on it. A key pair is one Name declared twice, each
// declaration naming the Half its placeholder receives. A declaration whose
// consumer decodes the leaf names the Encoding its placeholder receives: one
// value lands as it is where it is read and base64 where it is decoded.
type Generated struct {
	Name        string
	Placeholder string
	Kind        GeneratedKind
	// Length is the size of a Base64 or Alphanumeric value; a key pair has none.
	Length int
	// Half is the half of a KeyPairES256 the placeholder receives; a Base64 or
	// Alphanumeric value has none.
	Half Half
	// Encoding is how the placeholder receives the value; empty is the value
	// as generated.
	Encoding Encoding
}

// GeneratedKind is the shape of a generated value.
type GeneratedKind string

const (
	// Base64 is Length random bytes, standard base64.
	Base64 GeneratedKind = "base64"
	// Alphanumeric is Length characters of [A-Za-z0-9].
	Alphanumeric GeneratedKind = "alphanumeric"
	// KeyPairES256 is an ECDSA P-256 key pair, drawn once per Name: the
	// private half a PKCS #8 PEM, the public half a SubjectPublicKeyInfo PEM,
	// each written as one YAML scalar. The private half lands only in a
	// secret file; the commit step refuses a pair declared without it.
	KeyPairES256 GeneratedKind = "keypair-es256"
)

// Half is the half of a key pair a placeholder receives.
type Half string

const (
	// Private is the private key; only a secret file may receive it.
	Private Half = "private"
	// Public is the public key; a plain file may receive it too.
	Public Half = "public"
)

// KeyPair declares one half of the key pair name: the placeholder is the
// name's, qualified by the half, so the two halves of one pair never share a
// placeholder.
func KeyPair(name string, half Half) Generated {
	return Generated{Name: name, Placeholder: Placeholder(name + "." + string(half)), Kind: KeyPairES256, Half: half}
}

// Encoding is how a placeholder receives its value: as it is (the zero
// value), or encoded for a consumer that decodes the leaf.
type Encoding string

// EncodedBase64 is the value's standard base64: for a Secret key under data:,
// or a chart that copies the value under data: as it is. A Base64 or
// Alphanumeric value takes it; a key pair's halves are quoted YAML scalars
// and take no encoding.
const EncodedBase64 Encoding = "base64"

// Encoded is the declaration of g's value at a placeholder that receives it
// encoded: the name's, qualified by the encoding, so the raw and the encoded
// declaration of one value never share a placeholder. Kind and Length stay
// the value's, declared alike wherever the name appears.
func (g Generated) Encoded(encoding Encoding) Generated {
	g.Placeholder = Placeholder(g.Name + "." + string(encoding))
	g.Encoding = encoding
	return g
}

// Input is a definition's parsed input document, as the plan and the verify
// read it beyond the render: the markers and fields of the secret values a
// person supplies at commit, the required person inputs no layer of the
// document holds (rendered as Missing markers, by field), the id of a
// built-in Dex client the dex-app chart's key names (empty where the
// definition has none), and the actions the customer takes that no pull
// request delivers.
type Input interface {
	SuppliedMarkers() map[string]string
	SuppliedSecretFields() []string
	MissingInputs() []string
	BuiltInDexClientID(key string) string
	CustomerActions() []CustomerAction
	// Selected are the inputs the definition chose beyond the document, in
	// the document's shape, for the plan to answer as the effective inputs:
	// the chart line a fresh enable selects. Nil where the document stands.
	Selected() map[string]any
}

// RecordFile is the installation's record in its configs repository,
// installations/<name>/config.yaml.patch: the facts every definition reads
// its installation.* inputs from, and the one file a definition edits a key
// into rather than writes (the agent-platform enable's chart line).
const RecordFile = "config.yaml.patch"

// RecordPath is the installation's record.
func RecordPath(installation string) string {
	return "installations/" + installation + "/" + RecordFile
}

// Mode is what a render is for. The zero value is a commit: what is
// rendered is written, so a required person input the document lacks is
// refused, named by field. A comparison (verify_capability, a dry run)
// renders such an input as its Missing marker instead and the Input names
// it: a comparison never refuses over a choice not on record, only a commit
// does.
type Mode int

const (
	// ModeCommit renders what is written: a missing required person input
	// is refused.
	ModeCommit Mode = iota
	// ModeCompare renders for a comparison: a missing required person input
	// is its Missing marker, and every leaf that carries one is compared as
	// not checked.
	ModeCompare
)

// CustomerAction is something the rollout needs from the customer that no
// pull request of the manager delivers: what to do, and why it is theirs.
type CustomerAction struct {
	Action string
	Why    string
}

// Include is an entry a kustomization.yaml the definition does not own must
// list for the rendered files to take effect: the installation's
// extras/kustomization.yaml naming ./agent-platform/, for example. The engine
// adds it on enable and removes it on disable; the file itself is never
// rendered, because other owners write to it.
type Include struct {
	Repository Repository
	Path       string
	Resource   string
	// Component says the entry is a kustomize Component and belongs under the
	// kustomization's components list, not its resources: a directory that
	// patches objects the listing kustomization composes.
	Component bool
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
	// ResourcePresent is an object that exists; a Secret carries Expect.Keys,
	// an object with a status.state reports none that Expect.NotState names.
	ResourcePresent ProbeKind = "ResourcePresent"
	// Condition is an object whose condition Expect.Condition has Expect.ConditionStatus.
	Condition ProbeKind = "Condition"
	// HTTP is a GET of URL, redirects not followed, answered as Expect says.
	HTTP ProbeKind = "HTTP"
	// LogAbsent is a workload whose log matches Expect.Absent nowhere.
	LogAbsent ProbeKind = "LogAbsent"
	// Drift is a live object whose values equal what the rendered files imply.
	Drift ProbeKind = "Drift"
	// APIServed is an API the installation's apiserver serves, found by
	// discovery: Resource is the resource and its group
	// (podcertificaterequests.certificates.k8s.io), Expect.Version the version.
	APIServed ProbeKind = "APIServed"
)

// Expectation is what a probe expects; the fields its kind does not read stay zero.
type Expectation struct {
	Status           int      `yaml:"status,omitempty"`           // HTTP: the status code
	Statuses         []int    `yaml:"statuses,omitempty"`         // HTTP: any of these status codes, where the answer has several right shapes
	LocationContains string   `yaml:"locationContains,omitempty"` // HTTP: a substring of the Location header (302 probes)
	BodyContains     string   `yaml:"bodyContains,omitempty"`     // HTTP: a substring of the body (200 probes)
	Condition        string   `yaml:"condition,omitempty"`        // Condition: the type, e.g. Accepted, Ready
	ConditionStatus  string   `yaml:"conditionStatus,omitempty"`  // Condition: True or False
	Absent           string   `yaml:"absent,omitempty"`           // LogAbsent: a pattern that must not appear in the workload's log
	Keys             []string `yaml:"keys,omitempty"`             // ResourcePresent on a Secret: the keys it carries
	NotState         string   `yaml:"notState,omitempty"`         // ResourcePresent: a status.state the object must not report (an MCPServer's Failed)
	Version          string   `yaml:"version,omitempty"`          // APIServed: the API version the resource is served at
	Note             string   `yaml:"note,omitempty"`             // one sentence a person reads next to the mark (e.g. what a False means)
	// Compare lists, for a Drift probe, the places of the live object that are
	// compared to the render; empty compares the object's whole user values
	// (a HelmRelease: the values its valuesFrom ConfigMaps carry) to the
	// rendered values file.
	Compare []Comparison `yaml:"compare,omitempty"`
}

// Comparison is one place of a live object held against one place of the
// render. Live is a dotted path in the object; a path that crosses a YAML
// document stored in a string field names the field, then ":" and the path
// inside it (data.config.yaml:aggregator.oauth.server.trustedAudiences). A
// list element is [n], an argument list [args] (the element that starts with
// Prefix is compared, without the prefix). Rendered is the dotted path in the
// rendered values file the same place has.
type Comparison struct {
	Live     string `yaml:"live"`
	Rendered string `yaml:"rendered"`
	Prefix   string `yaml:"prefix,omitempty"`
}

// Action is something outside the platform team's hands, rendered as a note
// with a state. Feature is the features.yaml feature it holds up.
type Action struct {
	ID      string      `yaml:"id"`
	Feature string      `yaml:"feature"`
	State   ActionState `yaml:"state"`
	Note    string      `yaml:"note"` // what to do, in one sentence
	// Dimension is the live dimension of features.yaml the action holds up:
	// while the action is the customer's, that dimension reads waiting for
	// the customer instead of drifted.
	Dimension string `yaml:"dimension,omitempty"`
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

// IncludeComponent records a kustomize Component a shared kustomization must
// list under components.
func (r *Result) IncludeComponent(repo Repository, path, component string) {
	r.Includes = append(r.Includes, Include{Repository: repo, Path: path, Resource: component, Component: true})
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

// Supplied is the marker a definition writes where the value a person
// supplies at commit for field goes; the commit step replaces it.
func Supplied(field string) string { return "SUPPLIED(" + field + ")" }

// Missing is the marker a comparison renders where the value of a required
// person input no layer of the document holds would go: a choice not on
// record. A leaf that carries it is not checked; a commit never writes it.
func Missing(field string) string { return "MISSING(" + field + ")" }

// IsMarker reports whether value is a marker that stands for a value — a
// generated placeholder, a supplied or a missing one — rather than the value
// itself: what a definition leaves as it is where it encodes a leaf.
func IsMarker(value string) bool {
	for _, marker := range []string{Placeholder(""), Supplied(""), Missing("")} {
		if strings.HasPrefix(value, strings.TrimSuffix(marker, ")")) && strings.HasSuffix(value, ")") {
			return true
		}
	}
	return false
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
