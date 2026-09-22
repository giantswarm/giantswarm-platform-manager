package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/format"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/template"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("platformctl "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func cmdTemplate(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("template", stderr)
	inputs := fs.String("inputs", "", "the inputs document: `input` (the definition's inputs) and `secrets` (the values you supply)")
	out := fs.String("out", "", "directory to write the fileset to (default: print the files)")
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(pos) != 1 || *inputs == "" {
		return usageError(stderr, "template takes one shape ("+strings.Join(template.Shapes(), ", ")+") and --inputs <file>")
	}
	doc, err := os.ReadFile(*inputs)
	if err != nil {
		return fail(stderr, err)
	}
	tree, err := template.Render(pos[0], doc)
	if err != nil {
		return fail(stderr, err)
	}
	if *out != "" {
		if err := template.Write(*out, tree); err != nil {
			return fail(stderr, err)
		}
		say(stdout, "%d files written to %s\n", len(tree), *out)
		for _, p := range template.Paths(tree) {
			say(stdout, "  %s\n", p)
		}
		return exitOK
	}
	for _, p := range template.Paths(tree) {
		content := tree[p]
		say(stdout, "--- %s\n%s", p, content)
		if len(content) > 0 && content[len(content)-1] != '\n' {
			say(stdout, "\n")
		}
	}
	return exitOK
}

func cmdInstallationList(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("installation list", stderr)
	var c conn
	c.flags(fs)
	customer := fs.String("customer", "", "only this customer's installations")
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := c.valid(); err != nil {
		return usageError(stderr, err.Error())
	}
	toolArgs := map[string]any{}
	if len(pos) > 0 {
		toolArgs[tools.ArgInstallations] = pos
	}
	if *customer != "" {
		toolArgs[tools.ArgCustomer] = *customer
	}
	return c.call(tools.ToolListInstallations, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
		var r tools.ListInstallationsResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Installations(stdout, r)
	})
}

func cmdEnable(args []string, stdout, stderr io.Writer) int {
	return capability(tools.ToolEnableCapability, false, args, stdout, stderr)
}

func cmdReconcile(args []string, stdout, stderr io.Writer) int {
	return capability(tools.ToolReconcileCapability, true, args, stdout, stderr)
}

// capability is enable and reconcile: the same tool arguments, reconcile over
// a set — two or more installations named, or --all for every installation
// of the registry (the tool's installations; one name is its installation).
// --dry-run is the tool's dryRun, --commit its mode commit — for one
// installation the action with the secret values the plan names read from
// --secret sources and sent once, for a set the wave the manager answers as
// one.
func capability(tool string, allowSet bool, args []string, stdout, stderr io.Writer) int {
	name := strings.TrimSuffix(tool, "_capability")
	fs := newFlags("installation "+name, stderr)
	var c conn
	c.flags(fs)
	dryRun := fs.Bool("dry-run", false, "render the change and write nothing")
	commit := fs.Bool("commit", false, "start the action: the manager's mode commit, the pull requests opened as you")
	content := fs.Bool("content", false, "print the rendered files, not only their paths and changes")
	var inputs kvFlag
	fs.Var(&inputs, "input", "a typed input of the definition as key=value, repeatable (kagent.enabled=true)")
	var secretFlags secretFlag
	fs.Var(&secretFlags, "secret", "with --commit: a secret the plan's suppliedSecrets name, as <field>=@<file>, <field>=env:<NAME> or <field>=- (stdin); repeatable")
	all := false
	if allowSet {
		fs.BoolVar(&all, "all", false, "every installation of the registry with the capability on record; with --commit the wave")
	}
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := c.valid(); err != nil {
		return usageError(stderr, err.Error())
	}
	syntax := "installation " + name + " <installation> <capability> --dry-run|--commit"
	if allowSet {
		syntax = "installation " + name + " <installation>...|--all <capability> --dry-run|--commit"
	}
	// The capability is the last word; the installations are the words
	// before it: none with --all, exactly one for enable, one or more for
	// reconcile.
	names := len(pos) - 1
	ok := names == 0
	if !all {
		ok = names == 1 || allowSet && names > 1
	}
	if !ok {
		return usageError(stderr, syntax)
	}
	set := all || names > 1
	if *dryRun == *commit {
		return usageError(stderr, "one of --dry-run and --commit: "+syntax)
	}
	if *dryRun && len(secretFlags) > 0 {
		return usageError(stderr, "--secret goes with --commit; a dry run carries no secret value")
	}
	toolArgs := map[string]any{tools.ArgCapability: pos[len(pos)-1], tools.ArgContent: *content}
	if *commit {
		toolArgs[tools.ArgMode] = string(tools.ModeCommit)
	} else {
		toolArgs[tools.ArgDryRun] = true
	}
	switch {
	case all:
		toolArgs[tools.ArgInstallations] = []string{}
	case set:
		toolArgs[tools.ArgInstallations] = pos[:names]
	default:
		toolArgs[tools.ArgInstallation] = pos[0]
	}
	if len(inputs) > 0 {
		typed, err := nest(inputs)
		if err != nil {
			return usageError(stderr, err.Error())
		}
		toolArgs[tools.ArgInputs] = typed
	}
	if len(secretFlags) > 0 {
		srcs, err := sources(secretFlags)
		if err != nil {
			return usageError(stderr, err.Error())
		}
		values, err := readSecrets(srcs, os.Stdin, os.LookupEnv)
		if err != nil {
			return fail(stderr, err)
		}
		toolArgs[tools.ArgSecrets] = values
	}
	if *commit && set {
		return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
			var r tools.WaveResult
			if err := decode(raw, &r); err != nil {
				return err
			}
			return format.Wave(stdout, r)
		})
	}
	if *commit {
		return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
			var r tools.CommitResult
			if err := decode(raw, &r); err != nil {
				return err
			}
			return format.Commit(stdout, r, *content)
		})
	}
	return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
		var r tools.CapabilityResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Plan(stdout, r, *content)
	})
}

// carryInputs hands the repository answer's inputs to the live call, so the
// two halves render from the same inputs.
func carryInputs(args map[string]any, repo json.RawMessage) map[string]any {
	var answer struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal(repo, &answer); err != nil || answer.Inputs == nil {
		return args
	}
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out[tools.ArgInputs] = answer.Inputs
	return out
}

func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("installation verify", stderr)
	var c conn
	c.flags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := c.valid(); err != nil {
		return usageError(stderr, err.Error())
	}
	if len(pos) != 2 {
		return usageError(stderr, "installation verify <installation> <capability>")
	}
	toolArgs := map[string]any{tools.ArgInstallation: pos[0], tools.ArgCapability: pos[1]}
	return c.callBoth(tools.ToolVerifyCapability, tools.ToolVerifyInstallation, toolArgs, stdout, stderr, func(repo, live json.RawMessage, liveErr error) error {
		var r verify.Result
		if err := decode(repo, &r); err != nil {
			return err
		}
		if liveErr != nil {
			return format.Verify(stdout, r, liveErr)
		}
		var l verify.Result
		if err := decode(live, &l); err != nil {
			return err
		}
		return format.Verify(stdout, verify.Merge(r, l), nil)
	})
}

func cmdActionGet(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("action get", stderr)
	var c conn
	c.flags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := c.valid(); err != nil {
		return usageError(stderr, err.Error())
	}
	if len(pos) != 1 {
		return usageError(stderr, "action get <name>")
	}
	return c.call(tools.ToolGetAction, map[string]any{tools.ArgName: pos[0]}, stdout, stderr, func(raw json.RawMessage) error {
		var a actions.Action
		if err := decode(raw, &a); err != nil {
			return err
		}
		return format.Action(stdout, a)
	})
}

func cmdActionList(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("action list", stderr)
	var c conn
	c.flags(fs)
	installation := fs.String("installation", "", "only actions that include this installation")
	capabilityName := fs.String("capability", "", "only actions of this capability")
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := c.valid(); err != nil {
		return usageError(stderr, err.Error())
	}
	if len(pos) != 0 {
		return usageError(stderr, "action list [--installation <name>] [--capability <name>]")
	}
	toolArgs := map[string]any{}
	if *installation != "" {
		toolArgs[tools.ArgInstallation] = *installation
	}
	if *capabilityName != "" {
		toolArgs[tools.ArgCapability] = *capabilityName
	}
	return c.call(tools.ToolListActions, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
		var r tools.ListActionsResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Actions(stdout, r)
	})
}

func cmdActionApprove(args []string, stdout, stderr io.Writer) int {
	return actionByName("approve", tools.ToolApproveAction, false, args, stdout, stderr, func(raw json.RawMessage) error {
		var d tools.Decision
		if err := decode(raw, &d); err != nil {
			return err
		}
		return format.Decision(stdout, d)
	})
}

func cmdActionDeny(args []string, stdout, stderr io.Writer) int {
	return actionByName("deny", tools.ToolDenyAction, true, args, stdout, stderr, func(raw json.RawMessage) error {
		var d tools.Decision
		if err := decode(raw, &d); err != nil {
			return err
		}
		return format.Decision(stdout, d)
	})
}

func cmdActionMerge(args []string, stdout, stderr io.Writer) int {
	return actionByName("merge", tools.ToolMergeAction, false, args, stdout, stderr, func(raw json.RawMessage) error {
		var r tools.MergeResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Merge(stdout, r)
	})
}

// cmdActionWatch is the rollout watch: watch_action on the live registration,
// the reads as you, printed as the picture of the stage and the Action.
func cmdActionWatch(args []string, stdout, stderr io.Writer) int {
	return actionOnServer("watch", muster.LiveServer, tools.ToolWatchAction, false, args, stdout, stderr, func(raw json.RawMessage) error {
		var r tools.WatchResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Watch(stdout, r)
	})
}

// actionByName is approve, deny and merge: one action by name to the review's
// tool of that name on the App-pinned registration, as you; deny with its --reason.
func actionByName(name, tool string, withReason bool, args []string, stdout, stderr io.Writer, show func(json.RawMessage) error) int {
	return actionOnServer(name, muster.Server, tool, withReason, args, stdout, stderr, show)
}

// actionOnServer is one action by name to tool on the named registration of
// the manager (the App-pinned one, or the live one for the watch).
func actionOnServer(name, server, tool string, withReason bool, args []string, stdout, stderr io.Writer, show func(json.RawMessage) error) int {
	fs := newFlags("action "+name, stderr)
	var c conn
	c.flags(fs)
	var reason string
	if withReason {
		fs.StringVar(&reason, tools.ArgReason, "", "why the action is denied (required)")
	}
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := c.valid(); err != nil {
		return usageError(stderr, err.Error())
	}
	syntax := "action " + name + " <name>"
	if withReason {
		syntax += " --reason <text>"
	}
	if len(pos) != 1 || (withReason && reason == "") {
		return usageError(stderr, syntax)
	}
	toolArgs := map[string]any{tools.ArgAction: pos[0]}
	if withReason {
		toolArgs[tools.ArgReason] = reason
	}
	return c.callOn(server, tool, toolArgs, stdout, stderr, show)
}
