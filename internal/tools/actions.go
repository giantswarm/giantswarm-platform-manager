package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// ArgName is get_action's argument.
const ArgName = "name"

// ActionsInfo is where the Action records live, as get_info reports it.
type ActionsInfo struct {
	// Configured says whether the manager reaches the hub's API server for
	// the records; without it get_action and list_actions refuse, and say so.
	Configured bool   `json:"configured"`
	Group      string `json:"group"`
	Version    string `json:"version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
}

// ListActionsResult is list_actions' answer, newest first.
type ListActionsResult struct {
	Namespace string           `json:"namespace"`
	Filter    actions.Filter   `json:"filter"`
	Actions   []actions.Action `json:"actions"`
}

func (t *Tools) actionsInfo() ActionsInfo {
	info := ActionsInfo{Configured: t.d.Actions != nil, Group: actions.Group, Version: actions.Version, Kind: actions.Kind}
	if t.d.Actions != nil {
		info.Namespace = t.d.Actions.Namespace()
	}
	return info
}

func (t *Tools) registerActionTools(s *mcpserver.MCPServer) {
	s.AddTool(mcp.NewTool(ToolGetAction,
		mcp.WithDescription("Read-only. One Action record by name — an enablement or reconcile a person committed: the actor, the installations, the capability, the inputs, the pull requests, the approval, the rollout, the probes and the result. The record is a custom resource on the hub ("+actions.Group+"/"+actions.Version+" "+actions.Kind+"), read with the manager's own ServiceAccount."),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString(ArgName, mcp.Required(), mcp.Description("The Action's name.")),
	), t.getAction)
	s.AddTool(mcp.NewTool(ToolListActions,
		mcp.WithDescription("Read-only. The Action records on the hub, newest first; narrow with installation and capability."),
		mcp.WithReadOnlyHintAnnotation(true), mcp.WithIdempotentHintAnnotation(true),
		mcp.WithString(ArgInstallation, mcp.Description("Only actions that include this installation.")),
		mcp.WithString(ArgCapability, mcp.Description("Only actions of this capability."), mcp.Enum(installations.AgentPlatform)),
	), t.listActions)
}

func (t *Tools) actionsReader(tool string) (actions.Reader, error) {
	if t.d.Actions == nil {
		return nil, fmt.Errorf("%s: the Action records are not reachable: the manager runs without access to the hub's API server (no in-cluster ServiceAccount); get_info reports actions.configured", tool)
	}
	return t.d.Actions, nil
}

func (t *Tools) getAction(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	r, err := t.actionsReader(ToolGetAction)
	if err != nil {
		return result(nil, err)
	}
	name := strings.TrimSpace(req.GetString(ArgName, ""))
	if name == "" {
		return result(nil, errors.New(ToolGetAction+" needs "+ArgName))
	}
	a, err := r.Get(ctx, name)
	return result(a, err)
}

func (t *Tools) listActions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	r, err := t.actionsReader(ToolListActions)
	if err != nil {
		return result(nil, err)
	}
	f := actions.Filter{Installation: strings.TrimSpace(req.GetString(ArgInstallation, "")), Capability: strings.TrimSpace(req.GetString(ArgCapability, ""))}
	list, err := r.List(ctx, f)
	if err != nil {
		return result(nil, err)
	}
	return result(ListActionsResult{Namespace: r.Namespace(), Filter: f, Actions: list}, nil)
}

// lastActions fills every report's lastAction from the newest Action naming
// the installation and the capability, and lets an unfinished action's state
// stand over the repositories' state: pending approval, rolling out and
// waiting for the customer are read from the record, not the files.
func (t *Tools) lastActions(ctx context.Context, reports []installations.Report) error {
	if t.d.Actions == nil {
		return nil
	}
	list, err := t.d.Actions.List(ctx, actions.Filter{})
	if err != nil {
		return err
	}
	for i := range reports {
		for j := range reports[i].Capabilities {
			cs := &reports[i].Capabilities[j]
			for _, a := range list {
				if a.Spec.Capability != cs.Name || !a.Includes(reports[i].Name) {
					continue
				}
				cs.LastAction = &installations.ActionRef{Name: a.Name, Result: a.Status.State}
				if s := installations.State(a.Status.State); cs.State != installations.StateUnknown && s.FromAction() {
					cs.State = s
				}
				break
			}
		}
	}
	return nil
}
