package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	modelKey  = "kagent.modelKey"
	slackBot  = "klausGateway.slack.bot-token"
	slackSign = "klausGateway.slack.signing-secret"
	// placeholder is a value the tests move around; the assertion is that it
	// reaches the secrets object and nothing else.
	placeholder = "PLACEHOLDER-VALUE"
)

func TestSecretsComeFromFileEnvAndStdin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "model-key")
	if err := os.WriteFile(file, []byte(placeholder+"-file\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(name string) (string, bool) {
		if name == "SLACK_BOT_TOKEN" {
			return placeholder + "-env\n", true
		}
		return "", false
	}
	srcs, err := sources([]string{modelKey + "=@" + file, slackBot + "=env:SLACK_BOT_TOKEN", slackSign + "=-"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := readSecrets(srcs, strings.NewReader(placeholder+"-stdin\n"), env)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{modelKey: placeholder + "-file", slackBot: placeholder + "-env", slackSign: placeholder + "-stdin"}
	if len(got) != len(want) {
		t.Fatalf("got %d fields, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q (one trailing line break dropped, the field flat as suppliedSecrets names it)", k, got[k], v)
		}
	}
}

// TestSecretSourcesRefuseTheValueItself: a value on the command line is
// refused, and the refusal names the field, never what was typed.
func TestSecretSourcesRefuseTheValueItself(t *testing.T) {
	for _, c := range []struct {
		pairs []string
		want  string
	}{
		{[]string{modelKey + "=" + placeholder}, "--secret " + modelKey + ": " + secretSyntax},
		{[]string{modelKey + "=@"}, "--secret " + modelKey + ": "},
		{[]string{modelKey + "=env:"}, "--secret " + modelKey + ": "},
		{[]string{placeholder}, secretSyntax},
		{[]string{"=@file"}, secretSyntax},
		{[]string{modelKey + "=-", slackBot + "=-"}, "stdin already supplies another field"},
		{[]string{modelKey + "=-", modelKey + "=env:X"}, "given twice"},
	} {
		_, err := sources(c.pairs)
		if err == nil {
			t.Errorf("%v: accepted", c.pairs)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: error %q lacks %q", c.pairs, err, c.want)
		}
		if strings.Contains(err.Error(), placeholder) {
			t.Errorf("%v: the error echoes the value: %q", c.pairs, err)
		}
	}
}

func TestReadSecretsRefusesEmptyAndMissingSources(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unset := func(string) (string, bool) { return "", false }
	for _, c := range []struct {
		pair, want string
	}{
		{modelKey + "=@" + empty, "the source is empty"},
		{modelKey + "=@" + empty + "-missing", "no such file"},
		{modelKey + "=env:UNSET_FOR_THIS_TEST", "UNSET_FOR_THIS_TEST is not set in the environment"},
		{modelKey + "=-", "the source is empty"},
	} {
		srcs, err := sources([]string{c.pair})
		if err != nil {
			t.Fatal(err)
		}
		_, err = readSecrets(srcs, strings.NewReader(""), unset)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v lacks %q", c.pair, err, c.want)
		}
	}
}

// TestCommitNeverEchoesATypedSecret runs the CLI end to end on the mistake
// the sources guard exists for: the value itself on the command line. The
// refusal reaches stderr, the value does not.
func TestCommitNeverEchoesATypedSecret(t *testing.T) {
	var stdout, stderr strings.Builder
	args := []string{installationCmd, "enable", "hazel", agentPlatform, "--commit", "--secret", modelKey + "=" + placeholder}
	if got := run(args, &stdout, &stderr); got != exitUsage {
		t.Fatalf("exit %d, want %d\n%s%s", got, exitUsage, stdout.String(), stderr.String())
	}
	out := stdout.String() + stderr.String()
	if strings.Contains(out, placeholder) {
		t.Errorf("the output echoes the value:\n%s", out)
	}
	if !strings.Contains(out, "--secret "+modelKey+": "+secretSyntax) {
		t.Errorf("the refusal does not name the field and the sources:\n%s", out)
	}
}
