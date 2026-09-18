package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// The sources a --secret value comes from. The value itself is never a flag
// argument: the shell's history and the process list would keep it.
const (
	secretFromFile  = "@"
	secretFromEnv   = "env:"
	secretFromStdin = "-"
)

// secretFlag collects repeated --secret field=source flags. Set accepts every
// value and checks nothing: the flag package echoes a refused value in its
// error, and a person who passed the secret itself by mistake must not see it
// printed. sources refuses it, naming only the field.
type secretFlag []string

func (s *secretFlag) String() string { return strings.Join(*s, ",") }

func (s *secretFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// secretSource is one --secret flag, parsed: the field as the plan's
// suppliedSecrets name it and where its value is read from.
type secretSource struct {
	field string
	// kind is secretFromFile, secretFromEnv or secretFromStdin; ref the path
	// or the variable's name.
	kind, ref string
}

const secretSyntax = "--secret takes <field>=@<file>, <field>=env:<NAME> or <field>=- (stdin), never the value itself"

// sources parses the --secret flags; every refusal is a usage error and names
// no value.
func sources(pairs []string) ([]secretSource, error) {
	out := make([]secretSource, 0, len(pairs))
	seen := map[string]bool{}
	stdin := false
	for _, p := range pairs {
		field, src, ok := strings.Cut(p, "=")
		if !ok || field == "" {
			return nil, fmt.Errorf("%s", secretSyntax)
		}
		if seen[field] {
			return nil, fmt.Errorf("--secret %s: given twice", field)
		}
		seen[field] = true
		s := secretSource{field: field}
		switch {
		case src == secretFromStdin:
			if stdin {
				return nil, fmt.Errorf("--secret %s: stdin already supplies another field", field)
			}
			stdin = true
			s.kind = secretFromStdin
		case strings.HasPrefix(src, secretFromFile) && len(src) > len(secretFromFile):
			s.kind, s.ref = secretFromFile, src[len(secretFromFile):]
		case strings.HasPrefix(src, secretFromEnv) && len(src) > len(secretFromEnv):
			s.kind, s.ref = secretFromEnv, src[len(secretFromEnv):]
		default:
			return nil, fmt.Errorf("--secret %s: %s", field, secretSyntax)
		}
		out = append(out, s)
	}
	return out, nil
}

// readSecrets reads every source into the tool's `secrets` object, field to
// value, with one trailing line break dropped (a file written by an editor
// or `echo` ends in one; the value does not). This is the only place a value
// is held before the call, and nothing here prints one.
func readSecrets(srcs []secretSource, stdin io.Reader, getenv func(string) (string, bool)) (map[string]any, error) {
	out := make(map[string]any, len(srcs))
	for _, s := range srcs {
		var value string
		switch s.kind {
		case secretFromStdin:
			b, err := io.ReadAll(stdin)
			if err != nil {
				return nil, fmt.Errorf("--secret %s: reading stdin: %w", s.field, err)
			}
			value = string(b)
		case secretFromFile:
			b, err := os.ReadFile(s.ref)
			if err != nil {
				return nil, fmt.Errorf("--secret %s: %w", s.field, err)
			}
			value = string(b)
		case secretFromEnv:
			v, ok := getenv(s.ref)
			if !ok {
				return nil, fmt.Errorf("--secret %s: %s is not set in the environment", s.field, s.ref)
			}
			value = v
		}
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
		if value == "" {
			return nil, fmt.Errorf("--secret %s: the source is empty", s.field)
		}
		out[s.field] = value
	}
	return out, nil
}
