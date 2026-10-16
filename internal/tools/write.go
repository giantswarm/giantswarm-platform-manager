package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/mcp-toolkit/tooldesc"

	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
)

// Mode is how a write lands. The managers' contract offers apply and commit;
// for this manager only commit exists: every target — an installation's
// capabilities, its Dex clients, its secrets' declarations — is owned by a
// GitOps repository, and a change applied to the cluster without its commit
// is drift the next reconcile reverts.
type Mode string

// ModeCommit is the only accepted mode: a pull request as the caller.
const ModeCommit Mode = "commit"

// modeApply is named so the refusal can explain itself.
const modeApply = "apply"

const (
	// ArgDryRun and ArgMode are the two arguments every write tool takes.
	ArgDryRun = "dryRun"
	ArgMode   = "mode"
)

// ApplyRefusal is the reason mode: apply is refused, word for word — the same
// for every write tool, present and future.
const ApplyRefusal = `mode "apply" is refused: every target of this manager is GitOps-owned, and a change applied without its commit is drift the next reconcile reverts. ` +
	`Use mode "commit" (a pull request opened as you) or dryRun: true for the rendered change`

// writeSuffix closes every write tool's description.
const writeSuffix = ` Writes as you: dryRun: true answers the plan and writes nothing; mode: "commit" opens the pull requests through an Action and its approval — wait for the answer.`

// ErrNotImplemented marks a commit path a later slice delivers.
var ErrNotImplemented = errors.New("not implemented yet")

// WriteTool is a write registered through the framework: the framework owns
// dryRun and mode, refuses apply before the tool runs, and dispatches to
// DryRun or Commit.
type WriteTool struct {
	Name string
	// Summary and Details are the tool's description (tooldesc.Describe);
	// Details gets writeSuffix appended.
	Summary, Details string
	// Destructive marks a write that removes what an installation runs.
	Destructive bool
	// Options are the tool's own arguments (mcp.WithString and friends).
	Options []mcp.ToolOption
	// DryRun renders the change and writes nothing.
	DryRun func(ctx context.Context, args map[string]any) (any, error)
	// Commit lands the change as the caller. nil means ErrNotImplemented.
	Commit func(ctx context.Context, args map[string]any) (any, error)
}

// registerWrite adds wt to s with the framework's arguments and guard.
func registerWrite(s *mcpserver.MCPServer, wt WriteTool) {
	opts := []mcp.ToolOption{
		tooldesc.Describe(wt.Summary, wt.Details+writeSuffix),
		mcp.WithBoolean(ArgDryRun, mcp.Description("Answer the plan and write nothing (default false).")),
		mcp.WithString(ArgMode, mcp.Description(`"commit": open the pull requests as you through an Action. Required unless dryRun.`), mcp.Enum(string(ModeCommit))),
		mcp.WithDestructiveHintAnnotation(wt.Destructive),
		mcp.WithIdempotentHintAnnotation(true),
	}
	opts = append(opts, wt.Options...)
	s.AddTool(mcp.NewTool(wt.Name, opts...), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		dryRun, _ := args[ArgDryRun].(bool)
		mode, _ := args[ArgMode].(string)
		if err := checkMode(mode, dryRun); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		delete(args, ArgDryRun)
		delete(args, ArgMode)
		if dryRun {
			return result(wt.DryRun(ctx, args))
		}
		if wt.Commit == nil {
			return mcp.NewToolResultError(fmt.Sprintf("%s: mode commit %v; run with dryRun: true for the rendered change", wt.Name, ErrNotImplemented)), nil
		}
		if _, ok := identity.FromContext(ctx); !ok {
			return mcp.NewToolResultError(fmt.Sprintf("%s: mode commit needs a caller: the request carried no identity to open the pull request as", wt.Name)), nil
		}
		return result(wt.Commit(ctx, args))
	})
}

// checkMode is the framework's guard: apply is refused, unknown modes are
// refused, and a write without dryRun needs a mode.
func checkMode(mode string, dryRun bool) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case string(ModeCommit):
		return nil
	case modeApply:
		return errors.New(ApplyRefusal)
	case "":
		if dryRun {
			return nil
		}
		return fmt.Errorf(`mode is required unless dryRun is true: "%s"`, ModeCommit)
	default:
		return fmt.Errorf(`mode %q is not known: "%s" is the only write mode (apply is refused)`, mode, ModeCommit)
	}
}
