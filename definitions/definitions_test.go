package definitions_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"text/template"

	"gopkg.in/yaml.v3"

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
// every definition load, no file but migrations.yaml is empty, and every
// removal's kind is one its file's header documents — the engine acts on
// some (kept, hub), so a kind spelled otherwise would silently be none. A
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
			dimensionNodesCarryOnlyTheirFields(t, c)
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
			kinds := documentedKinds(t, c)
			seen := map[string]bool{}
			for _, r := range removals {
				if seen[r.Key] {
					t.Errorf("removals.yaml: key %q listed twice", r.Key)
				}
				seen[r.Key] = true
				if !kinds[r.Kind] {
					t.Errorf("removals.yaml: key %q has kind %q, which the header's kinds do not document", r.Key, r.Kind)
				}
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

// documentedKinds are the kinds a capability's removals.yaml documents in
// its header: each line under "# kinds:" that names one in its first column.
func documentedKinds(t *testing.T, capability string) map[string]bool {
	t.Helper()
	raw, err := definitions.FS.ReadFile(capability + "/removals.yaml")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	var in bool
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case !strings.HasPrefix(line, "#"):
			in = false
		case line == "# kinds:":
			in = true
		case in && strings.HasPrefix(line, "#   ") && !strings.HasPrefix(line, "#    "):
			kinds[strings.Fields(line[1:])[0]] = true
		}
	}
	if len(kinds) == 0 {
		t.Fatal("removals.yaml: the header documents no kind")
	}
	return kinds
}

// TestEveryReadBackNamesADeclaredFile holds every schema's x-readback to
// its shape: every file it names (one, or a list) is one the schema's
// x-files declares, the kind is one the reader knows, a key (a path, or a
// list of paths) is named unless the kind is the file's presence, and an
// installation kind names the service label it strips as its prefix.
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
					names := func(field string) []string {
						switch v := rb[field].(type) {
						case string:
							return []string{v}
						case []any:
							var out []string
							for _, n := range v {
								s, _ := n.(string)
								out = append(out, s)
							}
							return out
						}
						return nil
					}
					if len(names("file")) == 0 {
						t.Errorf("%s: x-readback names no file", path)
					}
					for _, file := range names("file") {
						if _, declared := files[file]; !declared {
							t.Errorf("%s: x-readback names the file %q, which x-files does not declare", path, file)
						}
					}
					kind, _ := rb["kind"].(string)
					prefix, _ := rb["prefix"].(string)
					keys := names("key")
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
	const customerPortal, agentPlatform = "customer-portal", "agent-platform"
	for _, tc := range []struct{ capability, field, want string }{
		{customerPortal, "plugins.grafana.domain", "the installation's own Grafana"},
		{customerPortal, "portal.domain", "the portal's hostname"},
		{customerPortal, "portal.organization", "the organisation's name as the portal shows it"},
		{customerPortal, "plugins.github.appId", "the GitHub App's id"},
		{customerPortal, "chart.line", "the semver range the portal's OCIRepository follows"},
		{customerPortal, "portal.supportUrl", "where the home page's support link goes"},
		{customerPortal, "federation.tokenBroker", ""},
		{agentPlatform, "installation.federation.brokerClientId", "dex client id of the hub's token-exchange broker client"},
		{agentPlatform, "installation.chartLine", ""},
		{agentPlatform, "installation.podCertificateRequest", ""},
		{customerPortal, "portal.nothing", ""},
		{"nothing", "portal.domain", ""},
	} {
		if got := definitions.InputSummary(tc.capability, tc.field); got != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.capability, tc.field, got, tc.want)
		}
	}
}

// dimensionNodesCarryOnlyTheirFields holds every dimension mapping of a
// capability's features.yaml to the fields Dimension has. In a YAML flow
// mapping an unquoted scalar ends at the first comma, so a key with an
// unquoted comma is read truncated and its tail becomes fields yaml.v3
// ignores: the card would show half the key, and the verify would route by
// half of it.
func dimensionNodesCarryOnlyTheirFields(t *testing.T, capability string) {
	t.Helper()
	raw, err := definitions.FS.ReadFile(capability + "/features.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	fields := map[string]bool{"id": true, "kind": true, "key": true, "catchAll": true}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode && n.Style&yaml.FlowStyle != 0 {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if !fields[n.Content[i].Value] {
					t.Errorf("features.yaml line %d: %q is no field of a dimension: a key with an unquoted comma is read truncated", n.Content[i].Line, n.Content[i].Value)
				}
			}
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&doc)
}
