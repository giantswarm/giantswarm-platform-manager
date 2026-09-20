package installations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

const readBackSchema = `{
  "type": "object",
  "x-files": {
    "values": {"repository": "management-clusters", "path": "management-clusters/<name>/values.yaml"},
    "patch": {"repository": "configs", "path": "installations/<name>/patch.yaml"}
  },
  "properties": {
    "installation": {"type": "object", "properties": {"name": {"type": "string"}}},
    "serving": {"type": "object", "properties": {
      "enabled": {"type": "boolean", "default": false, "x-readback": {"file": "values", "key": "components.serving.enabled"}}
    }},
    "tunnel": {"type": "object", "properties": {
      "enabled": {"type": "boolean", "default": false, "x-readback": {"file": "values", "key": "tunnel", "kind": "present"}}
    }},
    "portal": {"type": "object", "properties": {
      "domain": {"type": "string", "x-readback": {"file": "values", "key": "app.baseUrl", "kind": "host"}},
      "organization": {"type": "string", "default": "Giant Swarm"}
    }},
    "missing": {"type": "object", "properties": {
      "key": {"type": "string", "x-readback": {"file": "patch", "key": "nothing.here"}}
    }}
  }
}`

func readBackFixture(t *testing.T) *inputSchema {
	t.Helper()
	var s inputSchema
	if err := json.Unmarshal([]byte(readBackSchema), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

var readBackInstallation = Installation{Name: "rowan", Repositories: Repositories{Configs: "acme/configs", ManagementClusters: "acme/mcs"}}

// files answers the files on record by repository:path; a path not there is
// gh.ErrNotFound, as the reader reports it.
func files(m map[string]string) Reader {
	return func(_ context.Context, repository, path string) (string, error) {
		if content, ok := m[repository+":"+path]; ok {
			return content, nil
		}
		return "", gh.ErrNotFound
	}
}

// The three kinds read the file the definition renders the fileset key to:
// value takes the leaf, present says whether the key exists, host the host
// of a URL; a key or file not on record yields nothing.
func TestReadBackKinds(t *testing.T) {
	read := files(map[string]string{
		"acme/mcs:management-clusters/rowan/values.yaml": "components:\n  serving:\n    enabled: true\ntunnel:\n  port: 8443\napp:\n  baseUrl: https://portal.rowan.acme.test/\n",
	})
	got, err := readBack(context.Background(), read, readBackInstallation, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"serving.enabled": true, "tunnel.enabled": true, "portal.domain": "portal.rowan.acme.test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// Without the file on record nothing is read back but a present kind, which
// says the key is absent; the defaults stand.
func TestReadBackWithoutTheFile(t *testing.T) {
	got, err := readBack(context.Background(), files(nil), readBackInstallation, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("read back %v from no file", got)
	}
	read := files(map[string]string{"acme/mcs:management-clusters/rowan/values.yaml": "components: {}\napp:\n  baseUrl: not a url\n"})
	got, err = readBack(context.Background(), read, readBackInstallation, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"tunnel.enabled": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// A file the person cannot read is the error, not a default.
func TestReadBackForbidden(t *testing.T) {
	read := func(context.Context, string, string) (string, error) { return "", gh.ErrForbidden }
	if _, err := readBack(context.Background(), read, readBackInstallation, readBackFixture(t)); !errors.Is(err, gh.ErrForbidden) {
		t.Errorf("err %v", err)
	}
}

// Defaults is the document the schema's defaults make, installation aside.
func TestDefaults(t *testing.T) {
	got, err := (Capability{Name: AgentPlatform}).Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"modelServing": map[string]any{"enabled": false}}; !reflect.DeepEqual(got, want) {
		t.Errorf("defaults %v, want %v", got, want)
	}
}

// The agent-platform definition reads the serving choice back from the
// configmap patch on record.
func TestAgentPlatformReadsBackModelServing(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	read := files(map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): "components:\n  modelServing:\n    enabled: true\n"})
	got, err := def.ReadBack(context.Background(), read, readBackInstallation)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"modelServing.enabled": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}
