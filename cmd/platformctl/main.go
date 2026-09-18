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
  platformctl installation enable <installation> <capability> --dry-run [--input k=v]... [--content]
  platformctl installation reconcile <installation>|--all <capability> --dry-run [--input k=v]... [--content]
  platformctl action get <name>
  platformctl action list [--installation <name>] [--capability <name>]
  platformctl version

The installation and action commands call the manager's tools through muster's own
bridge (muster agent --mcp-server), which signs you in to muster when needed. Their flags:
  --output text|json    text for a terminal (default); json prints the manager's answer as it is
  --muster <path>       the muster CLI (default: muster on PATH)
  --endpoint <url>      the muster aggregator (default: muster's configuration)
  --config-path <dir>   muster's configuration directory (default: muster's)
  --timeout <duration>  per call (default 5m)

--input kagent.enabled=true nests dotted keys into the tool's inputs; a value that parses as
JSON is that value (true, 3, ["hazel"]), anything else is a string.

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
		return group("installation", args[1:], stdout, stderr, map[string]leaf{"list": cmdInstallationList, "enable": cmdEnable, "reconcile": cmdReconcile})
	case "action":
		return group("action", args[1:], stdout, stderr, map[string]leaf{"get": cmdActionGet, "list": cmdActionList})
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
	fs.DurationVar(&c.timeout, "timeout", 5*time.Minute, "timeout per call")
}

func (c *conn) valid() error {
	if c.output != outputText && c.output != outputJSON {
		return fmt.Errorf("--output is text or json, not %q", c.output)
	}
	return nil
}

// call runs one tool and prints its answer: as JSON when asked, else through
// show, which decodes the document into the manager's type and formats it.
func (c *conn) call(tool string, args map[string]any, stdout, stderr io.Writer, show func(json.RawMessage) error) int {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	s, err := muster.Open(ctx, muster.Options{Binary: c.binary, Endpoint: c.endpoint, ConfigPath: c.configPath, Stderr: stderr})
	if err != nil {
		return fail(stderr, err)
	}
	defer func() { _ = s.Close() }()
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

// decode reads the tool's document into the manager's own type.
func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("the answer is not the document this version knows (a newer manager? run with --output json): %w", err)
	}
	return nil
}
