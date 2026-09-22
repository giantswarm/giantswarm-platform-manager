package definitions_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"text/template"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// TestEveryProbeTemplateExecutes executes every probe's URL template of
// every definition over a ProbeData with every field set: a template over a
// field the verify does not fill fails here, not as a template error on the
// first verify of an installation.
func TestEveryProbeTemplateExecutes(t *testing.T) {
	var full verify.ProbeData
	v := reflect.ValueOf(&full).Elem()
	for i := 0; i < v.NumField(); i++ {
		v.Field(i).SetString(v.Type().Field(i).Name)
	}
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range caps {
		probes, err := definitions.Probes(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range probes {
			tmpl, err := template.New(p.ID).Parse(p.URL)
			if err != nil {
				t.Errorf("%s: probe %s: %v", c, p.ID, err)
				continue
			}
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, full); err != nil {
				t.Errorf("%s: probe %s: %v", c, p.ID, err)
			}
		}
	}
}

// TestEveryDefinitionParses holds every capability's data files to their
// shape: features.yaml, probes.yaml, removals.yaml and migrations.yaml of
// every definition load, and no file but migrations.yaml is empty. A
// removals.yaml no code path reads at run time is caught here, not on the
// first dry run that classifies with it.
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
			migrations, err := definitions.Migrations(c)
			if err != nil {
				t.Fatalf("migrations.yaml: %v", err)
			}
			seen = map[string]bool{}
			for _, m := range migrations {
				if seen[m.Key] {
					t.Errorf("migrations.yaml: key %q listed twice", m.Key)
				}
				seen[m.Key] = true
			}
		})
	}
}

// TestEveryReadBackNamesADeclaredFile holds every schema's x-readback to
// its shape: the file it names is one the schema's x-files declares, the
// kind is one the reader knows, a key (a path, or a list of paths) is named
// unless the kind is the file's presence, and an installation kind names
// the service label it strips as its prefix.
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
					prefix, _ := rb["prefix"].(string)
					var keys []string
					switch key := rb["key"].(type) {
					case string:
						keys = []string{key}
					case []any:
						for _, k := range key {
							s, _ := k.(string)
							keys = append(keys, s)
						}
					}
					named := len(keys) > 0 && !slices.Contains(keys, "")
					switch kind {
					case "", "value", "present", "host":
						if !named {
							t.Errorf("%s: x-readback names no key", path)
						}
					case "installation":
						if !named || prefix == "" {
							t.Errorf("%s: x-readback of an installation names no key or no service label as its prefix", path)
						}
					case "file":
						if len(keys) != 0 {
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

// TestInputSummary reads what the schema says an input is, the way a
// refusal quotes it: the first clause of the description, lowered, when
// short; nothing for a long one or a field the schema does not know.
func TestInputSummary(t *testing.T) {
	const customerPortal = "customer-portal"
	for _, tc := range []struct{ capability, field, want string }{
		{customerPortal, "plugins.grafana.domain", "the Grafana instance the plugin links to"},
		{customerPortal, "portal.domain", "the portal's hostname"},
		{customerPortal, "chart.line", "the semver range the portal's OCIRepository follows"},
		{customerPortal, "portal.supportUrl", "where the home page's support link goes"},
		{customerPortal, "federation.tokenBroker", ""},
		{customerPortal, "portal.nothing", ""},
		{"nothing", "portal.domain", ""},
	} {
		if got := definitions.InputSummary(tc.capability, tc.field); got != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.capability, tc.field, got, tc.want)
		}
	}
}
