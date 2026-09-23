// Command platformctl is the laptop and CI surface of
// giantswarm-platform-manager, with no logic of its own. `template` renders a
// capability's fileset locally by importing the render library — no token, no
// network. `installation` and `action` are thin clients of the manager's tools
// through muster: they call a tool and format its answer for a terminal, or
// print it as JSON.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/format"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster"
	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

const usage = `platformctl — the laptop and CI surface of giantswarm-platform-manager

  platformctl template <shape> --inputs <file> [--out <dir>]
      Render a capability's fileset locally from an inputs document (` + "`input`" + ` and ` + "`secrets`" + `);
      no token, no network. Without --out the files are printed.
  platformctl installation list [<installation>...] [--customer <name>]
  platformctl installation enable <installation> <capability> --dry-run|--commit [--input k=v]... [--secret f=src]... [--content]
  platformctl installation reconcile <installation>...|--all <capability> --dry-run|--commit [--input k=v]... [--secret f=src]... [--content]
  platformctl installation verify <installation> <capability>
  platformctl action get <name>
  platformctl action list [--installation <name>] [--capability <name>]
  platformctl action approve <name>
  platformctl action deny <name> --reason <text>
  platformctl action merge <name>
  platformctl action watch <name>
  platformctl version

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
once, never printed, and never taken from the command line.
verify prints the features of the definition with their marks and dimensions. approve, deny and
merge are the review's tools called as you; the manager's answer says what follows. watch reads
the rollout of a merged action as you: the Flux objects, then the probes,
and carries the action to enabled, waiting for the customer or failed — call it again while it
is rolling out.

Exit codes: 0 done; 1 the tool refused or the call failed; 2 usage; 3 sign in required.
`

const (
	exitOK = iota
	exitError
	exitUsage
	exitSignIn
)

const (
	outputText = "text"
	outputJSON = "json"
)

// leaf is one subcommand.
type leaf func(args []string, stdout, stderr io.Writer) int

// say writes to one of the CLI's streams; a failed write to stdout or stderr
// has nowhere left to be reported.
func say(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, "%s", usage)
		return exitUsage
	}
	switch args[0] {
	case "template":
		return cmdTemplate(args[1:], stdout, stderr)
	case "installation":
		return group("installation", args[1:], stdout, stderr, map[string]leaf{"list": cmdInstallationList, "enable": cmdEnable, "reconcile": cmdReconcile, "verify": cmdVerify})
	case "action":
		return group("action", args[1:], stdout, stderr, map[string]leaf{"get": cmdActionGet, "list": cmdActionList, "approve": cmdActionApprove, "deny": cmdActionDeny, "merge": cmdActionMerge, "watch": cmdActionWatch})
	case "version":
		say(stdout, "%s\n", version.String())
		return exitOK
	case "help", "-h", "--help":
		say(stdout, "%s", usage)
		return exitOK
	}
	say(stderr, "platformctl: %q is not a command\n\n%s", args[0], usage)
	return exitUsage
}

func group(name string, args []string, stdout, stderr io.Writer, leaves map[string]leaf) int {
	if len(args) > 0 {
		if l, ok := leaves[args[0]]; ok {
			return l(args[1:], stdout, stderr)
		}
	}
	say(stderr, "platformctl %s: needs one of its subcommands\n\n%s", name, usage)
	return exitUsage
}

// parse lets flags follow the positional arguments (platformctl installation
// enable hazel agent-platform --dry-run), which the flag package alone stops at.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
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

func (c *conn) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.output, "output", outputText, "text or json")
	fs.StringVar(&c.binary, "muster", "", "the muster CLI (default: muster on PATH)")
	fs.StringVar(&c.endpoint, "endpoint", "", "the muster aggregator's MCP endpoint (default: muster's configuration)")
	fs.StringVar(&c.configPath, "config-path", "", "muster's configuration directory (default: muster's)")
	fs.DurationVar(&c.timeout, "timeout", 5*time.Minute, "timeout per call, in the muster CLI bridge and platformctl alike")
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
