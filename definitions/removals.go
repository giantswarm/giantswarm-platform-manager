package definitions

import (
	"fmt"
	"io/fs"
	"sort"

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
