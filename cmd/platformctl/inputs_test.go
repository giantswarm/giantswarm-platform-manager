package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

const (
	templateCmd     = "template"
	installationCmd = "installation"
	actionCmd       = "action"
	kagentEnabled   = "kagent.enabled=true"
	agentPlatform   = installations.AgentPlatform
	publicInput     = "../../render/agentplatform/testdata/public-customer/input.yaml"
)

func TestNest(t *testing.T) {
	got, err := nest([]string{
		kagentEnabled,
		"portal.enabled=false",
		"toolAccess.agentManager=none",
		"federation.hubs=[\"hazel\"]",
		"installation.name=rowan",
		"installation.chartLine=\"4\"",
		"kagent.replicas=3",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `{"federation":{"hubs":["hazel"]},"installation":{"chartLine":"4","name":"rowan"},"kagent":{"enabled":true,"replicas":3},"portal":{"enabled":false},"toolAccess":{"agentManager":"none"}}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestNestRefusals(t *testing.T) {
	for _, c := range []struct {
		pairs []string
		want  string
	}{
		{[]string{"kagent=true", kagentEnabled}, "kagent is already a value"},
		{[]string{kagentEnabled, "kagent=true"}, "given twice, or already a mapping"},
		{[]string{"kagent..enabled=true"}, "empty key segment"},
		{[]string{"kagent.=true"}, "empty key segment"},
	} {
		if _, err := nest(c.pairs); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: got %v, want %q", c.pairs, err, c.want)
		}
	}
	var k kvFlag
	if err := k.Set("novalue"); err == nil {
		t.Error("a pair without = is refused")
	}
	if err := k.Set("=x"); err == nil {
		t.Error("an empty key is refused")
	}
	if err := k.Set("a=b=c"); err != nil || k.String() != "a=b=c" {
		t.Errorf("the first = splits: %v %q", err, k.String())
	}
}

func TestUsage(t *testing.T) {
	installation := func(sub string, rest ...string) []string { return append([]string{installationCmd, sub}, rest...) }
	tmpl := func(rest ...string) []string { return append([]string{templateCmd}, rest...) }
	for _, c := range []struct {
		args []string
		exit int
		want string
	}{
		{nil, exitUsage, "platformctl template <shape>"},
		{[]string{"nonsense"}, exitUsage, `"nonsense" is not a command`},
		{[]string{installationCmd}, exitUsage, "needs one of its subcommands"},
		{installation("enable", "hazel", agentPlatform), exitUsage, "one of --dry-run and --commit"},
		{installation("enable", "hazel", agentPlatform, "--dry-run", "--commit"), exitUsage, "one of --dry-run and --commit"},
		{installation("enable", "hazel"), exitUsage, "installation enable <installation> <capability> --dry-run|--commit"},
		{installation("reconcile", "--all", "hazel", agentPlatform, "--dry-run"), exitUsage, "<installation>|--all <capability>"},
		{installation("enable", "--all", "hazel", agentPlatform, "--commit"), exitUsage, "flag provided but not defined: -all"},
		{installation("enable", "hazel", agentPlatform, "--dry-run", "--secret", "kagent.modelKey=env:KEY"), exitUsage, "--secret goes with --commit"},
		{installation("enable", "hazel", agentPlatform, "--commit", "--secret", "kagent.modelKey=PLACEHOLDER-TYPED-VALUE"), exitUsage, "--secret kagent.modelKey: " + secretSyntax},
		{installation("enable", "hazel", agentPlatform, "--commit", "--secret", "kagent.modelKey=env:UNSET_FOR_THIS_TEST"), exitError, "UNSET_FOR_THIS_TEST is not set"},
		{installation("verify", "hazel"), exitUsage, "installation verify <installation> <capability>"},
		{installation("list", "--output", "yaml"), exitUsage, "--output is text or json"},
		{[]string{actionCmd, "get"}, exitUsage, "action get <name>"},
		{[]string{actionCmd, "list", "extra"}, exitUsage, "action list ["},
		{[]string{actionCmd, "approve"}, exitUsage, "action approve <name>"},
		{[]string{actionCmd, "deny", "enable-hazel-abc123"}, exitUsage, "action deny <name> --reason <text>"},
		{[]string{actionCmd, "merge", "one", "two"}, exitUsage, "action merge <name>"},
		{tmpl(agentPlatform), exitUsage, "--inputs <file>"},
		{tmpl("cluster", "--inputs", publicInput), exitError, "not a capability definition"},
		{tmpl(agentPlatform, "--inputs", "/nonexistent"), exitError, "no such file"},
		{[]string{"help"}, exitOK, "Exit codes"},
		{[]string{"version"}, exitOK, ""},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(c.args, &stdout, &stderr); got != c.exit {
			t.Errorf("%v: exit %d, want %d\n%s%s", c.args, got, c.exit, stdout.String(), stderr.String())
		}
		if out := stdout.String() + stderr.String(); !strings.Contains(out, c.want) {
			t.Errorf("%v: output lacks %q:\n%s", c.args, c.want, out)
		}
	}
}

// TestTemplateCommand runs the CLI end to end on a golden input: the printed
// tree names every golden file, and --out writes them.
func TestTemplateCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{templateCmd, agentPlatform, "--inputs", publicInput}, &stdout, &stderr); got != exitOK {
		t.Fatalf("exit %d: %s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "--- includes.txt\n") {
		t.Fatalf("output lacks includes.txt:\n%s", stdout.String())
	}
	dir := t.TempDir()
	stdout.Reset()
	if got := run([]string{templateCmd, agentPlatform, "--inputs", publicInput, "--out", dir}, &stdout, &stderr); got != exitOK {
		t.Fatalf("exit %d: %s", got, stderr.String())
	}
	if !strings.Contains(stdout.String(), "files written to "+dir) {
		t.Fatalf("got %s", stdout.String())
	}
}
