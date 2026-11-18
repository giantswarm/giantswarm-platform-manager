package render

import (
	"bytes"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// schemas holds one compile of each definition's schema by its path in
// definitions.FS.
var schemas sync.Map // path -> func() (*jsonschema.Schema, error)

// DefinitionSchema returns the JSON schema at path in definitions.FS (such
// as "agent-platform/schema.json"), compiled on the first call and shared by
// every later one. The file is embedded, so it never changes, and Validate
// only reads a compiled schema, so one compile serves concurrent renders. A
// definition's Parse validates every render's input against it, and a
// verify renders the input once for each perturbed leaf: compiling the
// schema again on each render was most of a render's time.
func DefinitionSchema(path string) (*jsonschema.Schema, error) {
	compile, _ := schemas.LoadOrStore(path, sync.OnceValues(func() (*jsonschema.Schema, error) {
		raw, err := definitions.FS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		compiler := jsonschema.NewCompiler()
		if err := compiler.AddResource("schema.json", doc); err != nil {
			return nil, err
		}
		return compiler.Compile("schema.json")
	}))
	return compile.(func() (*jsonschema.Schema, error))()
}
