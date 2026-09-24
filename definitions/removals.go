package definitions

import (
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Removal is one key of an installation's fileset that no input of the
// capability's schema renders: the renderer drops it for the reason given.
// Key paths use the file prefixes and the normalisation of schema.json's
// x-renders; Kind names the removal's class as the capability's removals.yaml
// documents it.
type Removal struct {
	Key    string `yaml:"key"`
	Kind   string `yaml:"kind"`
	Reason string `yaml:"reason"`
}

// RemovalKept is the kind of a removal the plan does not remove: a key of a
// file the definition writes whole whose replacement is not on the fleet yet
// — a shared default not landed, an object under extras not created. The plan
// carries the key from the record into the render, the pull request never
// removes it, the comparison finds it as defined, and the reason says what
// has to land before the key goes.
const RemovalKept = "kept"

// RemovalHub is the kind of a removal that is a section of the hub's Dev
// Portal, which the definition does not render: the comparison plans its
// removal like any other's, and a commit whose plan removes one the record
// carries with a value is held for the installation, naming the sections —
// the definition would turn the hub's portal into a customer's.
const RemovalHub = "hub"

// RemovalOtherDefinition is the kind of a removal that is a key the
// capability hands to the other definition, which renders it in a file of
// its own: the customer-portal definition's agent-platform section, which
// the agent-platform definition's Component carries.
const RemovalOtherDefinition = "other-definition"

// KeptKeys are the key paths of a capability's removals of kind kept under
// the file prefix (KindConfigMap, …), the prefix cut off, in file order.
func KeptKeys(capability, prefix string) ([]string, error) {
	return keysOfKind(capability, prefix, RemovalKept)
}

// HandedKeys are the key paths of a capability's removals of kind
// other-definition under the file prefix (backstage:app-config, …), the
// prefix cut off, in file order: the keys the capability hands to the other
// definition.
func HandedKeys(capability, prefix string) ([]string, error) {
	return keysOfKind(capability, prefix, RemovalOtherDefinition)
}

// keysOfKind are the key paths of a capability's removals of kind under the
// file prefix, the prefix cut off, in file order.
func keysOfKind(capability, prefix, kind string) ([]string, error) {
	rs, err := Removals(capability)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range rs {
		if path, ok := strings.CutPrefix(r.Key, prefix+":"); ok && r.Kind == kind {
			out = append(out, path)
		}
	}
	return out, nil
}

// Removals reads a capability's removals in file order. A removal without a
// key, a kind or a reason is an error naming the entry.
func Removals(capability string) ([]Removal, error) {
	raw, err := FS.ReadFile(capability + "/removals.yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Removals []Removal `yaml:"removals"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s/removals.yaml: %w", capability, err)
	}
	for i, r := range doc.Removals {
		if r.Key == "" || r.Kind == "" || r.Reason == "" {
			return nil, fmt.Errorf("%s/removals.yaml: removal %d (%q): key, kind and reason are required", capability, i, r.Key)
		}
	}
	return doc.Removals, nil
}

// Migration is one key the definition renders that an installation enabled
// before the migration lacks: its absence from the record is the migration's
// planned addition, not drift. Key paths use the file prefixes and the
// normalisation of removals.yaml; the reason names the migration.
type Migration struct {
	Key    string `yaml:"key"`
	Reason string `yaml:"reason"`
}

// Migrations reads a capability's migrations in file order — the order the
// comparison tries them, a more specific key before one that covers it. A
// migration without a key or a reason is an error naming the entry.
func Migrations(capability string) ([]Migration, error) {
	raw, err := FS.ReadFile(capability + "/migrations.yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Migrations []Migration `yaml:"migrations"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s/migrations.yaml: %w", capability, err)
	}
	for i, m := range doc.Migrations {
		if m.Key == "" || m.Reason == "" {
			return nil, fmt.Errorf("%s/migrations.yaml: migration %d (%q): key and reason are required", capability, i, m.Key)
		}
	}
	return doc.Migrations, nil
}

// Capabilities lists the capabilities the definitions carry: one directory each.
func Capabilities() ([]string, error) {
	entries, err := fs.ReadDir(FS, ".")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
