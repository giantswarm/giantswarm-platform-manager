package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/format"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/template"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
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
		toolArgs["installations"] = pos
	}
	if *customer != "" {
		toolArgs["customer"] = *customer
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

// capability is enable and reconcile: the same tool arguments, reconcile with
// --all for every installation of the registry. Only the dry run is in this
// version; the commit follows the manager's mode commit.
func capability(tool string, allowAll bool, args []string, stdout, stderr io.Writer) int {
	name := strings.TrimSuffix(tool, "_capability")
	fs := newFlags("installation "+name, stderr)
	var c conn
	c.flags(fs)
	dryRun := fs.Bool("dry-run", false, "render the change and write nothing (required in this version)")
	content := fs.Bool("content", false, "print the rendered files, not only their paths and changes")
	var inputs kvFlag
	fs.Var(&inputs, "input", "a typed input of the definition as key=value, repeatable (kagent.enabled=true)")
	all := false
	if allowAll {
		fs.BoolVar(&all, "all", false, "every installation of the registry that opted in")
	}
	pos, err := parse(fs, args)
	if err != nil {
		return exitUsage
	}
	if err := c.valid(); err != nil {
		return usageError(stderr, err.Error())
	}
	syntax := "installation " + name + " <installation> <capability> --dry-run"
	if allowAll {
		syntax = "installation " + name + " <installation>|--all <capability> --dry-run"
	}
	want := 2
	if all {
		want = 1
	}
	if len(pos) != want {
		return usageError(stderr, syntax)
	}
	if !*dryRun {
		return usageError(stderr, "--dry-run is required: "+syntax+" — the commit follows in a later version")
	}
	toolArgs := map[string]any{"capability": pos[len(pos)-1], tools.ArgDryRun: true, "content": *content}
	if all {
		toolArgs["installations"] = []string{}
	} else {
		toolArgs["installation"] = pos[0]
	}
	if len(inputs) > 0 {
		typed, err := nest(inputs)
		if err != nil {
			return usageError(stderr, err.Error())
		}
		toolArgs["inputs"] = typed
	}
	return c.call(tool, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
		var r tools.CapabilityResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Plan(stdout, r, *content)
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
	return c.call(tools.ToolGetAction, map[string]any{"name": pos[0]}, stdout, stderr, func(raw json.RawMessage) error {
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
		toolArgs["installation"] = *installation
	}
	if *capabilityName != "" {
		toolArgs["capability"] = *capabilityName
	}
	return c.call(tools.ToolListActions, toolArgs, stdout, stderr, func(raw json.RawMessage) error {
		var r tools.ListActionsResult
		if err := decode(raw, &r); err != nil {
			return err
		}
		return format.Actions(stdout, r)
	})
}
