package template

import (
	"bytes"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// testdata are the render library's golden shapes, by definition.
var testdata = map[string]string{
	"agent-platform":  "../../../render/agentplatform/testdata",
	"customer-portal": "../../../render/customerportal/testdata",
}

// TestGoldenByteForByte is the acceptance criterion: `platformctl template`
// reproduces every golden fileset of the render library, file for file and
// byte for byte, from the same inputs document.
func TestGoldenByteForByte(t *testing.T) {
	for definition, dir := range testdata {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		root := os.DirFS(dir)
		shapes := 0
		for _, e := range entries {
			shape := e.Name()
			if _, err := fs.Stat(root, shape+"/input.yaml"); err != nil {
				continue // not an installation shape (the consumption fixtures)
			}
			shapes++
			t.Run(definition+"/"+shape, func(t *testing.T) {
				inputs, err := fs.ReadFile(root, shape+"/input.yaml")
				if err != nil {
					t.Fatal(err)
				}
				got, err := Render(definition, inputs)
				if err != nil {
					t.Fatal(err)
				}
				out := t.TempDir()
				if err := Write(out, got); err != nil {
					t.Fatal(err)
				}
				want := readTree(t, dir+"/"+shape+"/golden")
				written := readTree(t, out)
				for name, content := range want {
					if !bytes.Equal(written[name], content) {
						t.Errorf("%s differs from the golden", name)
					}
				}
				for name := range written {
					if _, ok := want[name]; !ok {
						t.Errorf("%s written but not in the golden", name)
					}
				}
			})
		}
		if shapes == 0 {
			t.Fatal("no golden shape found under " + dir)
		}
	}
}

func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	root := os.DirFS(dir)
	err := fs.WalkDir(root, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(root, path)
		if err != nil {
			return err
		}
		out[path] = content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderRefusals(t *testing.T) {
	if _, err := Render("agent-platform", []byte("secrets: {}\n")); err == nil || !strings.Contains(err.Error(), "`input`") {
		t.Fatalf("no input mapping: got %v", err)
	}
	if _, err := Render("agent-platform", []byte("input: {colourScheme: dark}\n")); err == nil || !strings.Contains(err.Error(), "colourScheme") {
		t.Fatalf("unknown key: got %v", err)
	}
	if _, err := Render("cluster", []byte("input: {}\n")); err == nil || !strings.Contains(err.Error(), "agent-platform") {
		t.Fatalf("unknown shape: got %v", err)
	}
	if _, err := Render("agent-platform", []byte("input: [1")); err == nil {
		t.Fatal("malformed YAML: want an error")
	}
}

func TestShapes(t *testing.T) {
	if got := Shapes(); len(got) != 2 || got[0] != "agent-platform" || got[1] != "customer-portal" {
		t.Fatalf("got %v", got)
	}
}
