package dexsplit

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Ref is one value in a SOPS file: the file and the dotted key path, a
// numeric segment indexing a list.
type Ref struct {
	File, Path string
}

func (r Ref) String() string { return r.File + "#" + r.Path }

// Vault moves values between SOPS files without handing one to its caller.
// Reveal is the exception, for the leaves the caller names as configuration
// (ids, names, redirect URIs, peers): the plaintext patch carries them.
type Vault interface {
	// Name is the vault's flag value.
	Name() string
	// Reveal decrypts the scalar leaves at paths of a SOPS file.
	Reveal(ctx context.Context, file string, paths []string) (map[string]string, error)
	// CopySecret writes a new SOPS file dst: the Secret name in namespace
	// with the value of src under stringData.key, encrypted under the
	// creation rules of the .sops.yaml nearest above dst.
	CopySecret(ctx context.Context, src Ref, dst, name, namespace, key string) error
	// Unset removes the key paths from a SOPS file, the rest kept as it is.
	Unset(ctx context.Context, file string, paths []string) error
	// Equal says whether two values are equal.
	Equal(ctx context.Context, a, b Ref) (bool, error)
}

// ErrUnsupported is an operation a vault does not offer.
var ErrUnsupported = errors.New("not offered by this vault")

// Runner runs a command in dir with stdin and answers its stdout; a non-zero
// exit is an *exec.ExitError carrying the command's stderr.
type Runner func(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, error)

// Exec is the Runner of the real commands.
func Exec(ctx context.Context, dir string, stdin []byte, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...) //nolint:gosec // sops or beekeeper, arguments built here
	c.Dir = dir
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ee.Stderr = errOut.Bytes()
		}
		return nil, err
	}
	return out.Bytes(), nil
}

// The vaults by flag value.
const (
	VaultSOPS      = "sops"
	VaultBeekeeper = "beekeeper"
)

// NewVault is the vault named: sops for a person, whose sops decrypts with
// the person's age identity; beekeeper for an agent, whose values move
// inside beekeeper's process.
func NewVault(name string, run Runner) (Vault, error) {
	switch name {
	case VaultSOPS:
		return sopsVault{run: run}, nil
	case VaultBeekeeper:
		return beekeeperVault{run: run}, nil
	}
	return nil, fmt.Errorf("--vault %q: %s or %s", name, VaultSOPS, VaultBeekeeper)
}

// sopsVault runs the person's sops. Decrypted documents stay in this
// process's memory; a value is handed to sops encrypt on stdin.
type sopsVault struct{ run Runner }

func (sopsVault) Name() string { return VaultSOPS }

// The types sops is told: it types a file by extension, and *.yaml.patch is
// none it knows.
var sopsTypes = []string{"--input-type", "yaml", "--output-type", "yaml"}

func (v sopsVault) decrypt(ctx context.Context, file string) (*yaml.Node, error) {
	out, err := v.run(ctx, filepath.Dir(file), nil, "sops", append(append([]string{"decrypt"}, sopsTypes...), filepath.Base(file))...)
	if err != nil {
		return nil, commandError("sops decrypt "+file, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(out, &doc); err != nil || len(doc.Content) == 0 {
		return nil, fmt.Errorf("sops decrypt %s: no YAML document", file)
	}
	return doc.Content[0], nil
}

func (v sopsVault) value(ctx context.Context, r Ref) (string, error) {
	doc, err := v.decrypt(ctx, r.File)
	if err != nil {
		return "", err
	}
	n := lookup(doc, r.Path)
	if n == nil || n.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s: no value", r)
	}
	return n.Value, nil
}

func (v sopsVault) Reveal(ctx context.Context, file string, paths []string) (map[string]string, error) {
	doc, err := v.decrypt(ctx, file)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		n := lookup(doc, p)
		if n == nil || n.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s#%s: no scalar", file, p)
		}
		out[p] = n.Value
	}
	return out, nil
}

func (v sopsVault) CopySecret(ctx context.Context, src Ref, dst, name, namespace, key string) error {
	value, err := v.value(ctx, src)
	if err != nil {
		return err
	}
	manifest, err := secretManifest(name, namespace, key, value)
	if err != nil {
		return err
	}
	// sops finds the creation rule by the override's path relative to the
	// directory it runs in, the repository the .sops.yaml is at the root of.
	root, rel, err := sopsRoot(dst)
	if err != nil {
		return err
	}
	args := append(append([]string{"encrypt"}, sopsTypes...), "--filename-override", rel, "--output", rel)
	if _, err := v.run(ctx, root, manifest, "sops", args...); err != nil {
		return commandError("sops encrypt "+dst, err)
	}
	return nil
}

func (v sopsVault) Unset(ctx context.Context, file string, paths []string) error {
	for _, p := range paths {
		args := append(append([]string{"unset"}, sopsTypes...), filepath.Base(file), sopsIndex(p))
		if _, err := v.run(ctx, filepath.Dir(file), nil, "sops", args...); err != nil {
			return commandError("sops unset "+file+" "+p, err)
		}
	}
	return nil
}

func (v sopsVault) Equal(ctx context.Context, a, b Ref) (bool, error) {
	va, err := v.value(ctx, a)
	if err != nil {
		return false, err
	}
	vb, err := v.value(ctx, b)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(va), []byte(vb)) == 1, nil
}

// sopsIndex is a dotted path in sops' index syntax: ["a"][0]["b"].
func sopsIndex(path string) string {
	var b strings.Builder
	for _, k := range strings.Split(path, ".") {
		if _, err := strconv.Atoi(k); err == nil {
			b.WriteString("[" + k + "]")
			continue
		}
		q, _ := json.Marshal(k)
		b.WriteString("[" + string(q) + "]")
	}
	return b.String()
}

// sopsRoot is the directory of the .sops.yaml nearest above file, and file
// relative to it.
func sopsRoot(file string) (root, rel string, err error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return "", "", err
	}
	for dir := filepath.Dir(abs); ; dir = filepath.Dir(dir) {
		if exists(filepath.Join(dir, ".sops.yaml")) {
			rel, err := filepath.Rel(dir, abs)
			return dir, rel, err
		}
		if dir == filepath.Dir(dir) {
			return "", "", fmt.Errorf("%s: no .sops.yaml above it", file)
		}
	}
}

// secretManifest is the Secret with value under stringData.key, as the
// definitions render a Dex client's Secret.
func secretManifest(name, namespace, key, value string) ([]byte, error) {
	m := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"type":       "Opaque",
		"stringData": map[string]any{key: value},
	}
	return yaml.Marshal(m)
}

// beekeeperVault asks beekeeper, which holds the identities and runs sops in
// its own process; it answers key names, lengths and equality, never a
// secret. Reveal answers configuration alone: beekeeper's classifier refuses
// the call when a leaf looks secret.
type beekeeperVault struct{ run Runner }

func (beekeeperVault) Name() string { return VaultBeekeeper }

func (v beekeeperVault) Reveal(ctx context.Context, file string, paths []string) (map[string]string, error) {
	out := make(map[string]string, len(paths))
	if len(paths) == 0 {
		return out, nil
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	answer, err := v.run(ctx, filepath.Dir(abs), nil, "beekeeper", append([]string{"--json", "secret", "reveal", abs}, paths...)...)
	if err != nil {
		return nil, commandError("beekeeper secret reveal "+file, err)
	}
	var fields []struct{ Path, Value string }
	if err := json.Unmarshal(answer, &fields); err != nil {
		return nil, fmt.Errorf("beekeeper secret reveal %s: no JSON answer", file)
	}
	for _, f := range fields {
		out[f.Path] = f.Value
	}
	for _, p := range paths {
		if _, ok := out[p]; !ok {
			return nil, fmt.Errorf("%s#%s: no scalar", file, p)
		}
	}
	return out, nil
}

// CopySecret runs in dst's directory, so the source is made absolute first.
func (v beekeeperVault) CopySecret(ctx context.Context, src Ref, dst, name, namespace, key string) error {
	abs, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	from := src
	if from.File, err = filepath.Abs(src.File); err != nil {
		return err
	}
	_, err = v.run(ctx, filepath.Dir(abs), nil, "beekeeper", "secret", "copy", from.String()+"="+key, abs, "--name", name, "--namespace", namespace)
	return commandError("beekeeper secret copy "+src.String(), err)
}

// Unset hands every path to one call: beekeeper removes a path under
// another with its parent and a list's later items first, and answers a path
// already gone as absent.
func (v beekeeperVault) Unset(ctx context.Context, file string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	args := append(append([]string{"secret", "unset", abs}, paths...), "--write")
	_, err = v.run(ctx, filepath.Dir(abs), nil, "beekeeper", args...)
	return commandError("beekeeper secret unset "+file, err)
}

func (v beekeeperVault) Equal(ctx context.Context, a, b Ref) (bool, error) {
	_, err := v.run(ctx, "", nil, "beekeeper", "secret", "compare", a.String(), b.String())
	var ee *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &ee) && ee.ExitCode() == 1:
		return false, nil
	}
	return false, commandError("beekeeper secret compare "+a.String()+" "+b.String(), err)
}

// commandError words a failed command with the last line of its stderr:
// sops and beekeeper name the file and the key there, never a value.
func commandError(what string, err error) error {
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		lines := strings.Split(strings.TrimSpace(string(ee.Stderr)), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return fmt.Errorf("%s: %s", what, last)
		}
	}
	return fmt.Errorf("%s: %w", what, err)
}
