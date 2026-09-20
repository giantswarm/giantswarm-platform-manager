package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

func verifyCapabilityTool() mcp.Tool {
	return mcp.NewTool(ToolVerifyCapability,
		mcp.WithDescription("Read-only. Verify one installation against a capability's definition and answer the result grouped into the definition's features, each rolled up to one mark — as defined, differs by input, drifted — and expanded to its dimensions. Compares the owning repositories' files, read as you, against the render from the inputs on record (the installation's record under the newest Action's inputs): every difference names the file, the path and either the input that drives it or drift. Runs the definition's anonymous HTTP probes direct. The live dimensions — objects on the installation, read with your authority — are verify_installation's, the tool of the registration muster forwards your own token to; here they read not checked. Works for any installation of the registry, opted in or not."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithString(ArgInstallation, mcp.Required(), mcp.Description("The installation to verify, by name.")),
		mcp.WithString(ArgCapability, mcp.Description(capabilityArgDescription), mcp.Enum(installations.CapabilityNames()...)),
		mcp.WithObject(ArgInputs, mcp.Description(inputsArgDescription)),
		mcp.WithBoolean(ArgContent, mcp.Description("Include the rendered content of every file (default false); true answers the files as a commit would write them.")),
	)
}

func (t *Tools) verifyCapability(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return result(t.verify(ctx, req.GetArguments()))
}

// verify is verify_capability: every read as the caller, the probes anonymous.
func (t *Tools) verify(ctx context.Context, args map[string]any) (any, error) {
	token, ok := identity.TokenFromContext(ctx)
	if !ok {
		return nil, errors.New(ToolVerifyCapability + " needs a caller: the request carried no GitHub user token to read the registry and the installation's repositories as; " + identity.SignIn)
	}
	def, err := capabilityArg(args)
	if err != nil {
		return nil, err
	}
	name, _ := args[ArgInstallation].(string)
	if name == "" {
		return nil, fmt.Errorf("%s needs %s", ToolVerifyCapability, ArgInstallation)
	}
	typed, _ := args[ArgInputs].(map[string]any)
	content, _ := args[ArgContent].(bool)
	return t.verifyInstallation(ctx, token, name, def, typed, content)
}

// verifyInstallation is the verify of one installation against def as the
// person whose token this is: the inputs from the record, the files read
// back and the typed inputs; the tool's body, and the wave's gate between
// two stages.
func (t *Tools) verifyInstallation(ctx context.Context, token, name string, def installations.Capability, typed map[string]any, content bool) (*verify.Result, error) {
	c, err := gh.AsPerson(t.d.GitHubAPIURL, token)
	if err != nil {
		return nil, err
	}
	reg, err := t.registry(ctx, c)
	if err != nil {
		return nil, err
	}
	selected, err := reg.Select([]string{name}, "")
	if err != nil {
		return nil, err
	}
	hub, _ := reg.Find(reg.Hub)
	r := reg.InspectAll(ctx, c, selected, installations.Capabilities())[0]
	if !r.Repositories.Known() {
		return nil, fmt.Errorf("%s has no repositories on record: nothing to compare the definition against", name)
	}
	byName := map[string]installations.Installation{}
	for _, inst := range reg.Installations {
		byName[inst.Name] = inst
	}
	env := &planned{c: c, hub: hub, byName: byName, reports: map[string]installations.Report{r.Name: r}, inputs: map[string]map[string]any{}}
	out, err := t.compare(ctx, env, r, def, typed, content)
	if err != nil {
		return nil, err
	}
	t.d.Log.Info(ToolVerifyCapability, identity.LogAttr(ctx), "installation", name, "inputs", out.Inputs.Source, "state", out.State, "summary", out.Summary)
	return out, nil
}
