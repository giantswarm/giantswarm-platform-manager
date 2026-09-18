package actions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The chart's CRD declares every field the Action types write. A field the
// schema lacks is pruned by the API server on create, silently: the record
// read back is without it, and whatever is built from the record later is
// without it too (the actor's email once — the review was posted without its
// actor). The e2e stack stores Actions in a fake client that prunes nothing,
// so this is the test that holds the CRD to the types.
func TestCRDDeclaresEveryFieldOfTheTypes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "helm", "giantswarm-platform-manager", "templates", "crd-actions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// The template's Helm directives (the guard, the labels include) are not
	// part of the schema.
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.Contains(l, "{{") {
			lines = append(lines, l)
		}
	}
	var crd map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &crd); err != nil {
		t.Fatal(err)
	}
	versions, _ := crd["spec"].(map[string]any)["versions"].([]any)
	if len(versions) != 1 {
		t.Fatalf("the CRD serves %d version(s), the test knows one", len(versions))
	}
	root, _ := versions[0].(map[string]any)["schema"].(map[string]any)["openAPIV3Schema"].(map[string]any)
	props, _ := root["properties"].(map[string]any)
	for name, typ := range map[string]reflect.Type{"spec": reflect.TypeOf(Spec{}), "status": reflect.TypeOf(Status{})} {
		node, ok := props[name].(map[string]any)
		if !ok {
			t.Fatalf("the CRD has no %s schema", name)
		}
		declares(t, name, typ, node)
	}
}

// declares fails the test for every JSON field of typ that node, an OpenAPI
// schema, does not declare; path names the place in the message.
func declares(t *testing.T, path string, typ reflect.Type, node map[string]any) {
	t.Helper()
	if preserve, _ := node["x-kubernetes-preserve-unknown-fields"].(bool); preserve {
		return
	}
	switch typ.Kind() {
	case reflect.Pointer:
		declares(t, path, typ.Elem(), node)
	case reflect.Slice:
		items, ok := node["items"].(map[string]any)
		if !ok {
			t.Errorf("%s: an array in the types, no items in the CRD", path)
			return
		}
		declares(t, path+"[]", typ.Elem(), items)
	case reflect.Map:
		values, ok := node["additionalProperties"].(map[string]any)
		if !ok {
			t.Errorf("%s: a map in the types, no additionalProperties in the CRD", path)
			return
		}
		declares(t, path+"[*]", typ.Elem(), values)
	case reflect.Struct:
		if typ == reflect.TypeOf(time.Time{}) {
			return
		}
		props, _ := node["properties"].(map[string]any)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "" || name == "-" {
				continue
			}
			child, ok := props[name].(map[string]any)
			if !ok {
				t.Errorf("%s.%s: written by actions.%s, not declared in the CRD — the API server prunes it", path, name, typ.Name())
				continue
			}
			declares(t, path+"."+name, f.Type, child)
		}
	}
}
