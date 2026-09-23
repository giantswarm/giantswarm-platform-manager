package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster/mustertest"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/update"
)

// Words the command-line tests share.
const (
	completeCmd   = "__complete"
	completionCmd = "completion"
	helpCmd       = "help"
	helpFlag      = "--help"
	versionCmd    = "version"
	enableCmd     = "enable"
	reconcileCmd  = "reconcile"
	verifyCmd     = "verify"
	listCmd       = "list"
	getCmd        = "get"
	denyCmd       = "deny"
	dryRunFlag    = "--dry-run"
	inputsFlag    = "--inputs"
	hazel         = "hazel"
	lab           = "lab"
	nonsense      = "nonsense"
	zsh           = "zsh"
)

// TestExitCodes holds every exit code the command line answers: 0 done, 1 a
// failure, 2 usage (among them the flags cobra cannot parse, worded as the
// flag package worded them), 3 sign-in required, 125 a newer release for
// self-update --check (TestSelfUpdateCheckExits125WhenANewerReleaseIsOut).
func TestExitCodes(t *testing.T) {
	for _, c := range []struct {
		args []string
		exit int
		want string
	}{
		{[]string{versionCmd}, exitOK, ""},
		{[]string{helpCmd}, exitOK, "Exit codes: 0 done; 1 the tool refused or the call failed; 2 usage; 3 sign in required;\n125 self-update --check"},
		{[]string{"-h"}, exitOK, "platformctl self-update [--check]"},
		{[]string{helpFlag}, exitOK, "platformctl completion bash|zsh|fish|powershell"},
		{[]string{installationCmd, enableCmd, helpFlag}, exitOK, dryRunFlag},
		{[]string{completionCmd, zsh}, exitOK, "#compdef platformctl"},
		{[]string{templateCmd, agentPlatform, inputsFlag, "/nonexistent"}, exitError, "platformctl: open /nonexistent"},
		{nil, exitUsage, "platformctl template <shape>"},
		{[]string{nonsense}, exitUsage, `platformctl: "nonsense" is not a command`},
		{[]string{installationCmd, nonsense}, exitUsage, "platformctl installation: needs one of its subcommands"},
		{[]string{actionCmd}, exitUsage, "platformctl action: needs one of its subcommands"},
		{[]string{actionCmd, getCmd, "a", "--bogus"}, exitUsage, "platformctl: flag provided but not defined: -bogus\n\nUsage:\n  platformctl action get <name>"},
		{[]string{actionCmd, getCmd, "a", "-x"}, exitUsage, "flag provided but not defined: -x"},
		{[]string{"--bogus"}, exitUsage, "flag provided but not defined: -bogus\n\nplatformctl — the laptop"},
		{[]string{installationCmd, listCmd, "--timeout"}, exitUsage, "flag needs an argument: -timeout"},
		{[]string{installationCmd, listCmd, "--timeout", "soon"}, exitUsage, `invalid value "soon" for flag -timeout`},
		{[]string{installationCmd, enableCmd, hazel, agentPlatform, "--dry-run=maybe"}, exitUsage, `invalid value "maybe" for flag -dry-run`},
		{[]string{installationCmd, enableCmd, hazel, agentPlatform, "--input", "novalue", dryRunFlag}, exitUsage, `invalid value "novalue" for flag -input: --input takes key=value`},
	} {
		var stdout, stderr bytes.Buffer
		if got := run(c.args, &stdout, &stderr); got != c.exit {
			t.Errorf("%v: exit %d, want %d\n%s%s", c.args, got, c.exit, stdout.String(), stderr.String())
		}
		if out := stdout.String() + stderr.String(); !strings.Contains(out, c.want) {
			t.Errorf("%v: output lacks %q:\n%s", c.args, c.want, out)
		}
	}
	if code, out, errs := bridge(t, "sign-in", installationCmd, verifyCmd, lab, agentPlatform); code != exitSignIn {
		t.Errorf("sign-in: exit %d\n%s%s", code, out, errs)
	}
}

// Help goes to stdout, a usage error to stderr, as before.
func TestHelpIsStdoutAndUsageStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	run([]string{helpCmd}, &stdout, &stderr)
	if stdout.String() != usage || stderr.Len() != 0 {
		t.Errorf("help: stdout %d bytes, stderr %q", stdout.Len(), stderr.String())
	}
	stdout.Reset()
	run(nil, &stdout, &stderr)
	if stdout.Len() != 0 || stderr.String() != usage {
		t.Errorf("no command: stdout %q, stderr %d bytes", stdout.String(), stderr.Len())
	}
}

// The flag package read -dry-run as --dry-run; so does platformctl on cobra,
// through the bridge end to end.
func TestSingleDashFlagsStillWork(t *testing.T) {
	code, single, errs := bridge(t, "connected", installationCmd, enableCmd, lab, agentPlatform, "-dry-run", "-output=json")
	if code != exitOK {
		t.Fatalf("exit %d, stderr %q", code, errs)
	}
	_, double, _ := bridge(t, "connected", installationCmd, enableCmd, lab, agentPlatform, dryRunFlag, "--output", "json")
	if single != double || !strings.Contains(single, `"caller": "`+mustertest.Caller+`"`) {
		t.Errorf("-dry-run -output=json:\n%s\n--dry-run --output json:\n%s", single, double)
	}
}

// goFlags turns a flag package word into cobra's and leaves a flag's value,
// everything after "--" and cobra's own command lines alone.
func TestGoFlags(t *testing.T) {
	root := newRoot(update.New())
	for _, c := range []struct{ in, want []string }{
		{[]string{actionCmd, denyCmd, "x", "-reason", "-why"}, []string{actionCmd, denyCmd, "x", "--reason", "-why"}},
		{[]string{actionCmd, denyCmd, "x", "-reason=-why", "-output", "json"}, []string{actionCmd, denyCmd, "x", "--reason=-why", "--output", "json"}},
		{[]string{installationCmd, reconcileCmd, "-all", agentPlatform, "-commit", "-input", "a=-b"}, []string{installationCmd, reconcileCmd, "--all", agentPlatform, "--commit", "--input", "a=-b"}},
		{[]string{installationCmd, enableCmd, "-h", "-", "--", "-dry-run"}, []string{installationCmd, enableCmd, "-h", "-", "--", "-dry-run"}},
		{[]string{completeCmd, installationCmd, enableCmd, "-dr"}, []string{completeCmd, installationCmd, enableCmd, "-dr"}},
	} {
		if got := goFlags(root, c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("goFlags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// complete asks the completion the shell scripts ask: the words, one per
// line, then :<directive>.
func complete(t *testing.T, args ...string) []string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(append([]string{completeCmd}, args...), &stdout, &stderr); code != exitOK {
		t.Fatalf("%v: exit %d\n%s", args, code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var words []string
	for _, l := range lines {
		if strings.HasPrefix(l, ":") {
			continue
		}
		word, _, _ := strings.Cut(l, "\t")
		words = append(words, word)
	}
	return words
}

func TestCompletion(t *testing.T) {
	var caps []string
	for _, c := range installations.Capabilities() {
		caps = append(caps, c.Name)
	}
	for _, c := range []struct {
		args, want []string
	}{
		{[]string{""}, []string{actionCmd, completionCmd, helpCmd, installationCmd, selfUpdateCmd, templateCmd, versionCmd}},
		{[]string{installationCmd, ""}, []string{enableCmd, listCmd, reconcileCmd, verifyCmd}},
		{[]string{actionCmd, ""}, []string{"approve", denyCmd, getCmd, listCmd, "merge", "watch"}},
		{[]string{templateCmd, ""}, caps},
		{[]string{installationCmd, enableCmd, ""}, nil},
		{[]string{installationCmd, enableCmd, hazel, ""}, caps},
		{[]string{installationCmd, enableCmd, hazel, "agent-"}, []string{agentPlatform}},
		{[]string{installationCmd, enableCmd, hazel, agentPlatform, ""}, nil},
		{[]string{installationCmd, reconcileCmd, ""}, nil},
		{[]string{installationCmd, reconcileCmd, "--all", ""}, caps},
		{[]string{installationCmd, reconcileCmd, hazel, lab, ""}, caps},
		{[]string{installationCmd, verifyCmd, hazel, ""}, caps},
		{[]string{actionCmd, listCmd, "--capability", ""}, caps},
		{[]string{installationCmd, listCmd, "--output", ""}, []string{outputText, outputJSON}},
		{[]string{installationCmd, enableCmd, hazel, agentPlatform, "--d"}, []string{dryRunFlag}},
		{[]string{selfUpdateCmd, "--"}, []string{checkFlag, helpFlag}},
		{[]string{completionCmd, ""}, []string{"bash", "fish", "powershell", zsh}},
	} {
		if got := complete(t, c.args...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: completes %q, want %q", c.args, got, c.want)
		}
	}
}

// The zsh script completes through the binary's own __complete, which knows
// the capabilities without a network.
func TestCompletionScripts(t *testing.T) {
	for _, shell := range []string{"bash", zsh, "fish", "powershell"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{completionCmd, shell}, &stdout, &stderr); code != exitOK || !strings.Contains(stdout.String(), completeCmd) {
			t.Errorf("%s: exit %d, %d bytes\n%s", shell, code, stdout.Len(), stderr.String())
		}
	}
}
