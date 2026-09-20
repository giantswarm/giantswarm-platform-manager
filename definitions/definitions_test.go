package definitions_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// TestEveryDefinitionParses holds every capability's data files to their
// shape: features.yaml, probes.yaml and removals.yaml of every definition
// load, and no file is empty. A removals.yaml no code path reads at run time
// is caught here, not on the first dry run that classifies with it.
func TestEveryDefinitionParses(t *testing.T) {
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) == 0 {
		t.Fatal("no capability under definitions/")
	}
	for _, c := range caps {
		t.Run(c, func(t *testing.T) {
			feats, err := definitions.Features(c)
			if err != nil {
				t.Fatalf("features.yaml: %v", err)
			}
			if len(feats) == 0 {
				t.Error("features.yaml: no feature")
			}
			probes, err := definitions.Probes(c)
			if err != nil {
				t.Fatalf("probes.yaml: %v", err)
			}
			if len(probes) == 0 {
				t.Error("probes.yaml: no probe")
			}
			removals, err := definitions.Removals(c)
			if err != nil {
				t.Fatalf("removals.yaml: %v", err)
			}
			if len(removals) == 0 {
				t.Error("removals.yaml: no removal")
			}
			seen := map[string]bool{}
			for _, r := range removals {
				if seen[r.Key] {
					t.Errorf("removals.yaml: key %q listed twice", r.Key)
				}
				seen[r.Key] = true
			}
		})
	}
}

// TestEveryReadBackNamesADeclaredFile holds every schema's x-readback to
// its shape: the file it names is one the schema's x-files declares, the
// kind is one the reader knows, and a key is named unless the kind is the
// file's presence.
func TestEveryReadBackNamesADeclaredFile(t *testing.T) {
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range caps {
		t.Run(c, func(t *testing.T) {
			raw, err := definitions.FS.ReadFile(c + "/schema.json")
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			files, _ := schema["x-files"].(map[string]any)
			var walk func(node map[string]any, path string)
			walk = func(node map[string]any, path string) {
				if rb, ok := node["x-readback"].(map[string]any); ok {
					file, _ := rb["file"].(string)
					if _, declared := files[file]; !declared {
						t.Errorf("%s: x-readback names the file %q, which x-files does not declare", path, file)
					}
					kind, _ := rb["kind"].(string)
					key, _ := rb["key"].(string)
					switch kind {
					case "", "value", "present", "host":
						if key == "" {
							t.Errorf("%s: x-readback names no key", path)
						}
					case "file":
						if key != "" {
							t.Errorf("%s: x-readback of the file's presence names a key", path)
						}
					default:
						t.Errorf("%s: x-readback kind %q", path, kind)
					}
				}
				props, _ := node["properties"].(map[string]any)
				for name, child := range props {
					if m, ok := child.(map[string]any); ok {
						walk(m, strings.TrimPrefix(path+"."+name, "."))
					}
				}
			}
			walk(schema, "")
		})
	}
}
