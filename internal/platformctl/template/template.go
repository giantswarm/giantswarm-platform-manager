// Package template renders a capability's fileset locally: the inputs file in,
// the render library's tree out. No token, no network, no state — the render
// library is the only thing it calls.
package template

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Document is the inputs file: the definition's input document under `input`
// and the secret values a person supplies under `secrets` — the document the
// golden filesets are rendered from (render/<definition>/testdata/<shape>/input.yaml).
type Document struct {
	Input   map[string]any    `yaml:"input"`
	Secrets map[string]string `yaml:"secrets"`
}

// Shapes are the capability definitions the registry renders, sorted.
func Shapes() []string {
	names := installations.CapabilityNames()
	sort.Strings(names)
	return names
}

// Render decodes the inputs document and renders it through the shape's
// definition, answering the fileset as a tree (render.Result.Tree). A
// definition's refusal comes back as its error.
func Render(shape string, inputs []byte) (map[string][]byte, error) {
	def, ok := installations.FindCapability(shape)
	if !ok {
		return nil, fmt.Errorf("shape %q is not a capability definition; the definitions are: %s", shape, strings.Join(Shapes(), ", "))
	}
	var doc Document
	if err := yaml.Unmarshal(inputs, &doc); err != nil {
		return nil, fmt.Errorf("decode the inputs document: %w", err)
	}
	if doc.Input == nil {
		return nil, fmt.Errorf("the inputs document has no `input` mapping; it carries `input` (the definition's inputs) and `secrets` (the values you supply)")
	}
	result, err := def.Render(doc.Input, doc.Secrets, render.ModeCommit)
	if err != nil {
		return nil, err
	}
	return result.Tree(), nil
}

// Write lays the tree out under dir, creating directories as needed. Files
// already there with the same paths are replaced; others stay.
func Write(dir string, tree map[string][]byte) error {
	for name, content := range tree {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Paths are the tree's entries, sorted.
func Paths(tree map[string][]byte) []string {
	paths := make([]string, 0, len(tree))
	for name := range tree {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	return paths
}
