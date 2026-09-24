package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/format"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/template"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

func newTemplateCmd() *cobra.Command {
	var inputs, out string
	cmd := leaf("template <shape> --inputs <file> [--out <dir>]", "Render a capability's fileset locally from an inputs document; no token, no network", func(pos []string, stdout, stderr io.Writer) int {
		if len(pos) != 1 || inputs == "" {
			return usageError(stderr, "template takes one shape ("+strings.Join(template.Shapes(), ", ")+") and --inputs <file>")
		}
		doc, err := os.ReadFile(filepath.Clean(inputs))
		if err != nil {
			return fail(stderr, err)
		}
		tree, err := template.Render(pos[0], doc)
		if err != nil {
			return fail(stderr, err)
		}
		if out != "" {
			if err := template.Write(out, tree); err != nil {
				return fail(stderr, err)
			}
			say(stdout, "%d files written to %s\n", len(tree), out)
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
	})
	cmd.Flags().StringVar(&inputs, "inputs", "", "the inputs document: `input` (the definition's inputs) and `secrets` (the values you supply)")
	cmd.Flags().StringVar(&out, "out", "", "directory to write the fileset to (default: print the files)")
	cmd.ValidArgsFunction = capabilityAt(func(_ *cobra.Command, pos []string) bool { return len(pos) == 0 })
	completeFlag(cmd, "out", dirs)
	return cmd
}

func newInstallationListCmd() *cobra.Command {
	var c conn
	var customer string
	cmd := leaf("list [<installation>...] [--customer <name>]", "The installations and their capabilities", func(pos []string, stdout, stderr io.Writer) int {
		if err := c.valid(); err != nil {
			return usageError(stderr, err.Error())
		}
		toolArgs := map[string]any{}
		if len(pos) > 0 {
			toolArgs[tools.ArgInstallations] = pos
		}
		if customer != "" {
			toolArgs[tools.ArgCustomer] = customer
		}
		return c.call(tools.ToolListInstallations, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
			var r tools.ListInstallationsResult
			if err := decode(raw, &r); err != nil {
				return err
			}
			return format.Installations(stdout, r)
		})
	})
	c.flags(cmd)
	cmd.Flags().StringVar(&customer, "customer", "", "only this customer's installations")
	completeFlag(cmd, "customer", cobra.NoFileCompletions)
	return cmd
}

// newCapabilityCmd is enable and reconcile: the same tool arguments,
// reconcile over a set — two or more installations named, or --all for every
// installation of the registry (the tool's installations; one name is its
// installation). --dry-run is the tool's dryRun, --commit its mode commit —
// for one installation the action with the secret values the plan names read
// from --secret sources and sent once, for a set the wave the manager answers
// as one.
func newCapabilityCmd(tool string, allowSet bool) *cobra.Command {
	name := strings.TrimSuffix(tool, "_capability")
	var c conn
	var dryRun, commit, content, all bool
	var inputs kvFlag
	var secretFlags secretFlag
	var rotate []string
	syntax := "installation " + name + " <installation> <capability> --dry-run|--commit"
	short := "Enable a capability on one installation: a dry run, or the action"
	if allowSet {
		syntax = "installation " + name + " <installation>...|--all <capability> --dry-run|--commit"
		short = "Reconcile a capability on one installation, a set or --all: a dry run, the action or the wave"
	}
	cmd := leaf(strings.TrimPrefix(syntax, "installation "), short, func(pos []string, stdout, stderr io.Writer) int {
		if err := c.valid(); err != nil {
			return usageError(stderr, err.Error())
		}
		// The capability is the last word; the installations are the words
		// before it: none with --all, exactly one for enable, one or more
		// for reconcile.
		names := len(pos) - 1
		ok := names == 0
		if !all {
			ok = names == 1 || allowSet && names > 1
		}
		if !ok {
			return usageError(stderr, syntax)
		}
		set := all || names > 1
		if dryRun == commit {
			return usageError(stderr, "one of --dry-run and --commit: "+syntax)
		}
		if dryRun && len(secretFlags) > 0 {
			return usageError(stderr, "--secret goes with --commit; a dry run carries no secret value")
		}
		toolArgs := map[string]any{tools.ArgCapability: pos[len(pos)-1], tools.ArgContent: content}
		if commit {
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
		if len(rotate) > 0 {
			toolArgs[tools.ArgRotate] = rotate
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
		if commit && set {
			return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
				var r tools.WaveResult
				if err := decode(raw, &r); err != nil {
					return err
				}
				return format.Wave(stdout, r)
			})
		}
		if commit {
			return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
				var r tools.CommitResult
				if err := decode(raw, &r); err != nil {
					return err
				}
				return format.Commit(stdout, r, content)
			})
		}
		return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
			var r tools.CapabilityResult
			if err := decode(raw, &r); err != nil {
				return err
			}
			return format.Plan(stdout, r, content)
		})
	})
	c.flags(cmd)
	fs := cmd.Flags()
	fs.BoolVar(&dryRun, "dry-run", false, "render the change and write nothing")
	fs.BoolVar(&commit, "commit", false, "start the action: the manager's mode commit, the pull requests opened as you")
	fs.BoolVar(&content, "content", false, "print the rendered files, not only their paths and changes")
	fs.Var(&inputs, "input", "a typed input of the definition as key=value, repeatable (kagent.enabled=true)")
	fs.Var(&secretFlags, "secret", "with --commit: a secret the plan's suppliedSecrets name, as <field>=@<file>, <field>=env:<NAME> or <field>=- (stdin); repeatable")
	fs.StringArrayVar(&rotate, "rotate", nil, "a generated value to rotate on request, by its `name` in the dry run (<installation>-muster-valkey-password), repeatable: a new value in every file that holds it, its credentials revision rolling every workload that reads it")
	completeFlag(cmd, "input", cobra.NoFileCompletions)
	completeFlag(cmd, "secret", cobra.NoFileCompletions)
	completeFlag(cmd, "rotate", cobra.NoFileCompletions)
	// The capability is the last word: after the installation for enable,
	// after the first installation or first with --all for reconcile.
	cmd.ValidArgsFunction = capabilityAt(func(_ *cobra.Command, pos []string) bool { return len(pos) == 1 })
	if allowSet {
		fs.BoolVar(&all, "all", false, "every installation of the registry with the capability on record; with --commit the wave")
		cmd.ValidArgsFunction = capabilityAt(func(_ *cobra.Command, pos []string) bool { return all || len(pos) > 0 })
	}
	return cmd
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

func newVerifyCmd() *cobra.Command {
	var c conn
	cmd := leaf("verify <installation> <capability>", "Verify a capability on an installation: the repositories, then the running objects", func(pos []string, stdout, stderr io.Writer) int {
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
	})
	c.flags(cmd)
	cmd.ValidArgsFunction = capabilityAt(func(_ *cobra.Command, pos []string) bool { return len(pos) == 1 })
	return cmd
}

func newActionGetCmd() *cobra.Command {
	var c conn
	cmd := leaf("get <name>", "One action: its pull requests, review, rollout and result", func(pos []string, stdout, stderr io.Writer) int {
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
	})
	c.flags(cmd)
	return cmd
}

func newActionListCmd() *cobra.Command {
	var c conn
	var installation, capabilityName string
	cmd := leaf("list [--installation <name>] [--capability <name>]", "The actions, by installation and capability", func(pos []string, stdout, stderr io.Writer) int {
		if err := c.valid(); err != nil {
			return usageError(stderr, err.Error())
		}
		if len(pos) != 0 {
			return usageError(stderr, "action list [--installation <name>] [--capability <name>]")
		}
		toolArgs := map[string]any{}
		if installation != "" {
			toolArgs[tools.ArgInstallation] = installation
		}
		if capabilityName != "" {
			toolArgs[tools.ArgCapability] = capabilityName
		}
		return c.call(tools.ToolListActions, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
			var r tools.ListActionsResult
			if err := decode(raw, &r); err != nil {
				return err
			}
			return format.Actions(stdout, r)
		})
	})
	c.flags(cmd)
	cmd.Flags().StringVar(&installation, "installation", "", "only actions that include this installation")
	cmd.Flags().StringVar(&capabilityName, "capability", "", "only actions of this capability")
	completeFlag(cmd, "installation", cobra.NoFileCompletions)
	completeFlag(cmd, "capability", func(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return capabilities(toComplete)
	})
	return cmd
}

func newActionApproveCmd() *cobra.Command {
	return actionByName("approve", "Approve an action as you: the review's approval", tools.ToolApproveAction, false, func(stdout io.Writer, raw json.RawMessage) error {
		var d tools.Decision
		if err := decode(raw, &d); err != nil {
			return err
		}
		return format.Decision(stdout, d)
	})
}

func newActionDenyCmd() *cobra.Command {
	return actionByName("deny", "Deny an action as you, with the reason; its actor withdraws a merged one that failed or was reverted", tools.ToolDenyAction, true, func(stdout io.Writer, raw json.RawMessage) error {
		var d tools.Decision
		if err := decode(raw, &d); err != nil {
			return err
		}
		return format.Decision(stdout, d)
	})
}

func newActionMergeCmd() *cobra.Command {
	return actionByName("merge", "Merge an approved action's pull requests as you (a wave: one stage per call)", tools.ToolMergeAction, false, func(stdout io.Writer, raw json.RawMessage) error {
		var r tools.MergeResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Merge(stdout, r)
	})
}

// newActionWatchCmd is the rollout watch: watch_action, the reads as you,
// printed as the picture of the stage and the Action.
func newActionWatchCmd() *cobra.Command {
	return actionByName("watch", "Read a merged action's rollout as you and carry it on", tools.ToolWatchAction, false, func(stdout io.Writer, raw json.RawMessage) error {
		var r tools.WatchResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Watch(stdout, r)
	})
}

// actionByName is approve, deny, merge and watch: one action by name to the
// manager's tool of that name, as you; deny with its --reason.
func actionByName(name, short, tool string, withReason bool, show func(stdout io.Writer, raw json.RawMessage) error) *cobra.Command {
	var c conn
	var reason string
	syntax := "action " + name + " <name>"
	if withReason {
		syntax += " --reason <text>"
	}
	cmd := leaf(strings.TrimPrefix(syntax, "action "), short, func(pos []string, stdout, stderr io.Writer) int {
		if err := c.valid(); err != nil {
			return usageError(stderr, err.Error())
		}
		if len(pos) != 1 || (withReason && reason == "") {
			return usageError(stderr, syntax)
		}
		toolArgs := map[string]any{tools.ArgAction: pos[0]}
		if withReason {
			toolArgs[tools.ArgReason] = reason
		}
		return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error { return show(stdout, raw) })
	})
	c.flags(cmd)
	if withReason {
		cmd.Flags().StringVar(&reason, tools.ArgReason, "", "why the action is denied or withdrawn (required)")
		completeFlag(cmd, tools.ArgReason, cobra.NoFileCompletions)
	}
	return cmd
}
