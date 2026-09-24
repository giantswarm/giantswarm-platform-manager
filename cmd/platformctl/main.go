// Command platformctl is the laptop and CI surface of
// giantswarm-platform-manager, with no logic of its own. `template` renders a
// capability's fileset locally by importing the render library — no token, no
// network. `installation` and `action` are thin clients of the manager's tools
// through muster: they call a tool and format its answer for a terminal, or
// print it as JSON. `self-update` installs the latest release once its
// signature verifies, `completion` prints a shell's completion script.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/format"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/update"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

const usage = `platformctl — the laptop and CI surface of giantswarm-platform-manager

  platformctl template <shape> --inputs <file> [--out <dir>]
      Render a capability's fileset locally from an inputs document (` + "`input`" + ` and ` + "`secrets`" + `);
      no token, no network. Without --out the files are printed.
  platformctl installation list [<installation>...] [--customer <name>]
  platformctl installation enable <installation> <capability> --dry-run|--commit [--input k=v]... [--secret f=src]... [--rotate <name>]... [--content]
  platformctl installation reconcile <installation>...|--all <capability> --dry-run|--commit [--input k=v]... [--secret f=src]... [--rotate <name>]... [--content]
  platformctl installation verify <installation> <capability>
  platformctl action get <name>
  platformctl action list [--installation <name>] [--capability <name>]
  platformctl action approve <name>
  platformctl action deny <name> --reason <text>
  platformctl action merge <name>
  platformctl action watch <name>
  platformctl version
  platformctl self-update [--check]
  platformctl completion bash|zsh|fish|powershell

The installation and action commands call the manager's tools through muster's own
bridge (muster agent --mcp-server), which signs you in to muster when needed. Their flags:
  --output text|json    text for a terminal (default); json prints the manager's answer as it is
  --muster <path>       the muster CLI (default: muster on PATH)
  --endpoint <url>      the muster aggregator (default: muster's configuration)
  --config-path <dir>   muster's configuration directory (default: muster's)
  --timeout <duration>  per call, in the muster CLI bridge and platformctl alike (default 5m)

--input kagent.enabled=true nests dotted keys into the tool's inputs; a value that parses as
JSON is that value (true, 3, ["hazel"]), anything else is a string.

--dry-run renders the change and writes nothing; --commit is the manager's mode commit: the pull
requests opened as you, the Team review asked — for one installation the action, for a reconcile over
a set (two or more installations named, or --all for every installation of the registry) the wave
over the set, one action rolled out a stage per merge. --secret <field>=@<file>, <field>=env:<NAME>
or <field>=- (stdin, one field) supplies a secret the plan's suppliedSecrets name; the value is sent
once, never printed, and never taken from the command line. --rotate <name> (repeatable) rotates a
generated value on request, named as the dry run's generated secrets list it
(<installation>-muster-valkey-password): the dry run shows it "rotates on request" with the files that
hold it, the commit writes a new value into them and draws its component's credentials revision, so
every workload that reads it restarts. Over a set a name applies where an installation's plan lists
it; a name no plan lists is refused.
verify prints the features of the definition with their marks and dimensions. approve, deny and
merge are the review's tools called as you; the manager's answer says what follows. deny on a
merged action that failed or was reverted is its actor's withdrawal: the action moves to
withdrawn with the reason, the review's thread told. watch reads the rollout of a merged action
as you: whether its pull requests are still on the default branch (reverted, naming the revert,
when they are not), the Flux objects, then the probes, and carries the action to enabled,
waiting for the customer or failed — call it again while it is rolling out.

self-update installs the latest release over this binary once its cosign bundle verifies as a
CircleCI build of giantswarm/giantswarm-platform-manager; --check only reports both versions. The
other commands print a one-line hint on stderr while a newer release is out;
` + update.OptOutEnv + `=1 silences it. completion prints the shell's completion script:
subcommands, flags and capability names (` + "`platformctl completion zsh > \"${fpath[1]}/_platformctl\"`" + `).

Exit codes: 0 done; 1 the tool refused or the call failed; 2 usage; 3 sign in required;
125 self-update --check found a newer release.
`

const (
	exitOK = iota
	exitError
	exitUsage
	exitSignIn
)

// exitOutdated is `self-update --check`'s answer when a newer release exists,
// devctl's convention for `version check`.
const exitOutdated = 125

const (
	outputText = "text"
	outputJSON = "json"
)

// say writes to one of the CLI's streams; a failed write to stdout or stderr
// has nowhere left to be reported.
func say(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is one command line against GitHub's releases for self-update and the
// newer-release hint.
func run(args []string, stdout, stderr io.Writer) int {
	return execute(update.New(), args, stdout, stderr)
}

// execute runs one command line on the command tree and answers its exit
// code. A command prints its own errors and answers its code (exitCode); a
// flag cobra cannot parse is a usage error printed here.
func execute(u *update.Updater, args []string, stdout, stderr io.Writer) int {
	root := newRoot(u)
	root.SetOut(stdout)
	root.SetErr(stderr)
	// Never nil: cobra reads os.Args for a nil slice.
	root.SetArgs(append([]string{}, goFlags(root, args)...))
	err := root.Execute()
	var code exitCode
	var flagErr *flagError
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &code):
		return int(code)
	case errors.As(err, &flagErr):
		say(stderr, "platformctl: %s\n\n%s", flagErr.msg, usageOf(flagErr.cmd))
		return exitUsage
	}
	return fail(stderr, err)
}

// newRoot is the command tree, with u behind self-update and the hint.
func newRoot(u *update.Updater) *cobra.Command {
	root := &cobra.Command{
		Use:           "platformctl",
		Short:         "The laptop and CI surface of giantswarm-platform-manager",
		Args:          cobra.ArbitraryArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		// No command, or a first word that is none: the usage, exit 2.
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				say(c.ErrOrStderr(), "%s", usage)
			} else {
				say(c.ErrOrStderr(), "platformctl: %q is not a command\n\n%s", args[0], usage)
			}
			return exitCode(exitUsage)
		},
		// The one-line hint that a newer release exists, on stderr ahead of
		// the command's own output (update.OptOutEnv silences it).
		PersistentPreRun: func(c *cobra.Command, _ []string) {
			if remindsOfNewerRelease(c) {
				u.Remind(c.Context(), c.ErrOrStderr())
			}
		},
	}
	root.AddCommand(
		newTemplateCmd(),
		group("installation", "The capabilities of the installations: list, enable, reconcile, verify",
			newInstallationListCmd(),
			newCapabilityCmd(tools.ToolEnableCapability, false),
			newCapabilityCmd(tools.ToolReconcileCapability, true),
			newVerifyCmd(),
		),
		group("action", "The actions: get, list, approve, deny, merge, watch",
			newActionGetCmd(),
			newActionListCmd(),
			newActionApproveCmd(),
			newActionDenyCmd(),
			newActionMergeCmd(),
			newActionWatchCmd(),
		),
		newVersionCmd(),
		newSelfUpdateCmd(u),
	)
	// platformctl's help is the usage above; a subcommand's is cobra's, with
	// its flags.
	commandHelp := root.HelpFunc()
	root.SetHelpFunc(func(c *cobra.Command, args []string) {
		if !c.HasParent() {
			say(c.OutOrStdout(), "%s", usage)
			return
		}
		commandHelp(c, args)
	})
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &flagError{cmd: c, msg: goFlagMessage(err)}
	})
	return root
}

// quietCommands never print the newer-release hint: the commands about
// versions and cobra's plumbing (help, completion; __complete is hidden).
var quietCommands = map[string]bool{
	"version":     true,
	"self-update": true,
	"help":        true,
	"completion":  true,
}

// remindsOfNewerRelease says whether c is a command that does work, the ones
// the hint is for: not platformctl itself (the usage), and neither c nor a
// command above it hidden or in quietCommands (`completion zsh` is under
// `completion`).
func remindsOfNewerRelease(c *cobra.Command) bool {
	if !c.HasParent() {
		return false
	}
	for ; c != nil; c = c.Parent() {
		if c.Hidden || quietCommands[c.Name()] {
			return false
		}
	}
	return true
}

// group is installation and action: a word that needs one of its
// subcommands.
func group(name, short string, leaves ...*cobra.Command) *cobra.Command {
	g := &cobra.Command{
		Use:   name,
		Short: short,
		Args:  cobra.ArbitraryArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			say(c.ErrOrStderr(), "platformctl %s: needs one of its subcommands\n\n%s", name, usage)
			return exitCode(exitUsage)
		},
	}
	g.AddCommand(leaves...)
	return g
}

// leaf is one subcommand. run checks the positional arguments itself,
// prints its own errors and answers the exit code; its flags are the
// command's, registered by the caller. Positional arguments complete to
// nothing unless the caller says otherwise.
func leaf(use, short string, run func(pos []string, stdout, stderr io.Writer) int) *cobra.Command {
	return &cobra.Command{
		Use:               use,
		Short:             short,
		Args:              cobra.ArbitraryArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(c *cobra.Command, pos []string) error {
			if n := run(pos, c.OutOrStdout(), c.ErrOrStderr()); n != exitOK {
				return exitCode(n)
			}
			return nil
		},
	}
}

// exitCode ends a command that has printed why with its exit code.
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit code %d", int(e)) }

// flagError is a flag cobra could not parse, worded as the flag package
// worded it before platformctl was built on cobra.
type flagError struct {
	cmd *cobra.Command
	msg string
}

func (e *flagError) Error() string { return e.msg }

// goFlagMessage words a flag error the way the flag package did ("flag
// provided but not defined: -all"); scripts and people have seen these.
func goFlagMessage(err error) string {
	var notDefined *pflag.NotExistError
	var noValue *pflag.ValueRequiredError
	var invalid *pflag.InvalidValueError
	switch {
	case errors.As(err, &notDefined):
		return "flag provided but not defined: -" + notDefined.GetSpecifiedName()
	case errors.As(err, &noValue):
		return "flag needs an argument: -" + noValue.GetSpecifiedName()
	case errors.As(err, &invalid):
		return fmt.Sprintf("invalid value %q for flag -%s: %v", invalid.GetValue(), invalid.GetFlag().Name, invalid.Unwrap())
	}
	return err.Error()
}

// usageOf is what a usage error prints after the reason: platformctl's
// usage, or a subcommand's with its flags.
func usageOf(c *cobra.Command) string {
	if !c.HasParent() {
		return usage
	}
	return c.UsageString()
}

// goFlags rewrites the single-dash long flags the flag package accepted
// (-dry-run, -output=json) to the double dash cobra reads, so a command line
// written before platformctl was built on cobra keeps working. The value of a
// flag that takes one (--reason -x) and every word after "--" stay as they
// are; so does a command line cobra itself answers (help, completion).
func goFlags(root *cobra.Command, args []string) []string {
	cmd, _, err := root.Find(args)
	if err != nil || cmd == root {
		return args
	}
	out := make([]string, 0, len(args))
	value := false
	for i, a := range args {
		switch {
		case value:
			value = false
		case a == "--":
			return append(out, args[i:]...)
		case len(a) > 2 && a[0] == '-':
			if a[1] != '-' {
				a = "-" + a
			}
			name, _, inline := strings.Cut(a[2:], "=")
			f := cmd.Flags().Lookup(name)
			value = f != nil && f.NoOptDefVal == "" && !inline
		}
		out = append(out, a)
	}
	return out
}

func usageError(stderr io.Writer, msg string) int {
	say(stderr, "platformctl: %s\n\n%s", msg, usage)
	return exitUsage
}

func fail(stderr io.Writer, err error) int {
	say(stderr, "platformctl: %v\n", err)
	return exitError
}

// conn is how the muster commands reach the manager and print its answers.
type conn struct {
	output, binary, endpoint, configPath string
	timeout                              time.Duration
}

// flags registers the muster commands' flags on cmd, with their completions.
func (c *conn) flags(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.StringVar(&c.output, "output", outputText, "text or json")
	fs.StringVar(&c.binary, "muster", "", "the muster CLI (default: muster on PATH)")
	fs.StringVar(&c.endpoint, "endpoint", "", "the muster aggregator's MCP endpoint (default: muster's configuration)")
	fs.StringVar(&c.configPath, "config-path", "", "muster's configuration directory (default: muster's)")
	fs.DurationVar(&c.timeout, "timeout", 5*time.Minute, "timeout per call, in the muster CLI bridge and platformctl alike")
	completeFlag(cmd, "output", cobra.FixedCompletions([]cobra.Completion{outputText, outputJSON}, cobra.ShellCompDirectiveNoFileComp))
	completeFlag(cmd, "config-path", dirs)
	completeFlag(cmd, "endpoint", cobra.NoFileCompletions)
	completeFlag(cmd, "timeout", cobra.NoFileCompletions)
}

// bridgeGrace is how much longer than --timeout platformctl waits for one
// call: the bridge waits --timeout for muster's answer and reports its own
// deadline, so a call cut short names the bridge, not platformctl; only a
// bridge that answers nothing at all runs into platformctl's bound.
const bridgeGrace = 15 * time.Second

// open starts the bridge, bounded by --timeout: the connection and the
// person's sign-in.
func (c *conn) open(stderr io.Writer) (*muster.Session, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	return muster.Open(ctx, muster.Options{Binary: c.binary, Endpoint: c.endpoint, ConfigPath: c.configPath, Stderr: stderr, CallTimeout: c.timeout})
}

// callCtx bounds one call: --timeout plus the grace the bridge's own deadline
// needs to arrive first.
func (c *conn) callCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.timeout+bridgeGrace)
}

func (c *conn) valid() error {
	if c.output != outputText && c.output != outputJSON {
		return fmt.Errorf("--output is text or json, not %q", c.output)
	}
	return nil
}

// call runs one tool of the manager and prints its answer:
// as JSON when asked, else through show, which decodes the document into the
// manager's type and formats it.
func (c *conn) call(tool string, args map[string]any, stdout, stderr io.Writer, show func(json.RawMessage) error) int {
	s, err := c.open(stderr)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := c.callCtx()
	defer cancel()
	raw, err := s.Call(ctx, tool, args)
	if err != nil {
		if auth, ok := muster.IsAuthRequired(err); ok {
			if c.output == outputJSON {
				b, _ := json.Marshal(auth)
				_ = format.JSON(stdout, b)
			} else {
				_ = format.AuthRequired(stderr, auth)
			}
			return exitSignIn
		}
		return fail(stderr, err)
	}
	if c.output == outputJSON {
		err = format.JSON(stdout, raw)
	} else {
		err = show(raw)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitOK
}

// callBoth is call over the two halves of a result, both tools of the one
// registration: tool, then liveTool with the same arguments and the first
// answer's inputs; the live answer, or why there is none, goes to show with
// the first. A live tool that refuses — not served, no identity forwarded,
// an installation not connected — is not a failure of the command: the
// repository result stands and the live side says why. --output json prints the two documents as one
// object, {"repository": …, "live": …|null, "liveError": …}, with "liveCut"
// {layer, tool, timeout} when the live call ended without an answer. Each
// call has its own --timeout.
func (c *conn) callBoth(tool, liveTool string, args map[string]any, stdout, stderr io.Writer, show func(repo, live json.RawMessage, liveErr error) error) int {
	s, err := c.open(stderr)
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = s.Close() }()
	ctx, cancel := c.callCtx()
	repo, err := s.Call(ctx, tool, args)
	cancel()
	if err != nil {
		if auth, ok := muster.IsAuthRequired(err); ok {
			if c.output == outputJSON {
				b, _ := json.Marshal(auth)
				_ = format.JSON(stdout, b)
			} else {
				_ = format.AuthRequired(stderr, auth)
			}
			return exitSignIn
		}
		return fail(stderr, err)
	}
	liveCtx, cancelLive := c.callCtx()
	defer cancelLive()
	live, liveErr := s.Call(liveCtx, liveTool, carryInputs(args, repo))
	if c.output == outputJSON {
		doc := map[string]any{"repository": repo, "live": nil}
		if liveErr != nil {
			doc["liveError"] = liveErr.Error()
			if cut, ok := muster.IsCut(liveErr); ok {
				doc["liveCut"] = cut
			}
		} else {
			doc["live"] = live
		}
		b, _ := json.Marshal(doc)
		err = format.JSON(stdout, b)
	} else {
		err = show(repo, live, liveErr)
	}
	if err != nil {
		return fail(stderr, err)
	}
	return exitOK
}

// decode reads the tool's document into the manager's own type.
func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("the answer is not the document this version knows (a newer manager? run with --output json): %w", err)
	}
	return nil
}
