// Package mustertest is muster's bridge as a test double. `muster agent
// --mcp-server` exposes muster's meta tools only — call_tool, list_tools, … —
// and the aggregator's tools (the manager's x_<server>_<tool>, muster's
// core_*) are reached through call_tool {name, arguments}, which answers the
// tool's own result as one JSON document. A client that calls an aggregator
// tool by its name on the bridge is told the tool does not exist, as muster's
// bridge tells it.
package mustertest

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// Tool answers one aggregator tool; ctx is the request's, carrying whatever
// the HTTP layer put there (the bearer, in a fake behind a listener).
type Tool func(ctx context.Context, args map[string]any) *mcp.CallToolResult

// Answers of the fake manager, for tests to compare against.
const (
	// Info is get_info's document.
	Info = `{"version": "0.7.0"}`
	// Refusal is list_installations' refusal.
	Refusal = "list_installations needs a caller"
	// LoginURL is the sign-in core_auth_login offers for a server not
	// connected yet.
	LoginURL = "https://example.test/login"
	// Caller and Hub are the fake manager's answers to who asks and where;
	// LiveCaller is who the live registration says the reads ran as.
	Caller     = "admin"
	Hub        = "hub"
	LiveCaller = "admin@example.test"
)

// Bridge is an in-process bridge over the aggregator tools given by their
// aggregator names.
func Bridge(aggregator map[string]Tool) *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("muster", "test")
	s.AddTool(mcp.NewTool("call_tool",
		mcp.WithString("name", mcp.Required()),
		mcp.WithObject("arguments"),
	), callTool(aggregator))
	s.AddTool(mcp.NewTool("list_tools"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		names := make([]map[string]string, 0, len(aggregator))
		for name := range aggregator {
			names = append(names, map[string]string{"name": name})
		}
		doc, err := json.Marshal(map[string]any{"tools": names})
		if err != nil {
			return nil, err
		}
		return mcp.NewToolResultText(string(doc)), nil
	})
	return s
}

// callTool is muster's call_tool: the named tool's result as one JSON
// document, the outer isError following the tool's; a name the aggregator
// does not have is call_tool's own refusal.
func callTool(aggregator map[string]Tool) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := req.GetString("name", "")
		tool, ok := aggregator[name]
		if !ok {
			return mcp.NewToolResultError("Tool execution failed: tool not found: " + name), nil
		}
		args, _ := req.GetArguments()["arguments"].(map[string]any)
		res := tool(ctx, args)
		content := make([]map[string]string, 0, len(res.Content))
		for _, c := range res.Content {
			if t, ok := mcp.AsTextContent(c); ok {
				content = append(content, map[string]string{"type": "text", "text": t.Text})
			}
		}
		doc, err := json.Marshal(map[string]any{"isError": res.IsError, "content": content, "structuredContent": res.StructuredContent})
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{IsError: res.IsError, Content: []mcp.Content{mcp.NewTextContent(string(doc))}}, nil
	}
}

// Manager is the aggregator as a person sees it: the manager's tools under
// x_<server>_ when connected, hidden when not, and core_auth_login answering
// the sign-in challenge in the latter case. The connected manager's
// enable_capability and verify_capability echo the installation, capability
// and dryRun they were called with.
func Manager(connected bool) map[string]Tool {
	server := tools.ToolPrefix
	m := map[string]Tool{}
	if !connected {
		m["core_auth_login"] = func(context.Context, map[string]any) *mcp.CallToolResult {
			return &mcp.CallToolResult{
				Content:           []mcp.Content{mcp.NewTextContent("Authentication Required\n\nServer: " + server)},
				StructuredContent: map[string]any{"authUrl": LoginURL},
			}
		}
		return m
	}
	m["core_auth_login"] = func(context.Context, map[string]any) *mcp.CallToolResult {
		return mcp.NewToolResultText("Already authenticated to " + server)
	}
	m["x_"+server+"_"+tools.ToolGetInfo] = func(context.Context, map[string]any) *mcp.CallToolResult {
		return mcp.NewToolResultText(Info)
	}
	m["x_"+server+"_"+tools.ToolListInstallations] = func(context.Context, map[string]any) *mcp.CallToolResult {
		return mcp.NewToolResultError(Refusal)
	}
	m["x_"+server+"_"+tools.ToolEnableCapability] = func(_ context.Context, args map[string]any) *mcp.CallToolResult {
		dryRun, _ := args[tools.ArgDryRun].(bool)
		return document(tools.CapabilityResult{
			Caller: Caller, Hub: Hub, Tool: tools.ToolEnableCapability,
			Capability: str(args[tools.ArgCapability]), DryRun: dryRun,
			Order: []string{str(args[tools.ArgInstallation])},
		})
	}
	m["x_"+server+"_"+tools.ToolVerifyCapability] = func(_ context.Context, args map[string]any) *mcp.CallToolResult {
		return document(verify.Result{
			Caller: Caller, Hub: Hub,
			Installation: str(args[tools.ArgInstallation]), Capability: str(args[tools.ArgCapability]),
			State: "enabled", Inputs: verify.Inputs{Source: verify.SourceRecord},
		})
	}
	m["x_"+tools.LiveToolPrefix+"_"+tools.ToolVerifyInstallation] = func(_ context.Context, args map[string]any) *mcp.CallToolResult {
		return document(verify.Result{
			Caller:       LiveCaller,
			Installation: str(args[tools.ArgInstallation]), Capability: str(args[tools.ArgCapability]),
			State: "enabled", Inputs: verify.Inputs{Source: verify.SourceNone},
		})
	}
	return m
}

// document is a tool's JSON answer, indented the way the manager prints it.
func document(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal: %v", err))
	}
	return mcp.NewToolResultText(string(b))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
