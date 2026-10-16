package tools

import (
	"testing"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/giantswarm/mcp-toolkit/tooldesc/tooldesctest"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
)

// docsServer registers every tool: the live tools register only with a live
// client, and any one will do for their descriptions.
func docsServer() *mcpserver.MCPServer {
	return New(Deps{Live: &live.Client{}}).MCPServer()
}

// TestToolsDocIsCurrent keeps docs/tools.md — every tool's description and
// input schema as a model reads them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	tooldesctest.AssertServerDoc(t, docsServer(),
		"# The tools\n\nGenerated from the registered tools (`make tools-doc`); through muster every tool is `x_"+ToolPrefix+"_<name>`. "+
			"`"+ToolVerifyInstallation+"` and `"+ToolWatchAction+"` are registered only with the live tools on (`live.enabled`).",
		"../../docs/tools.md")
}

// TestToolDescriptionBudgets holds the registered descriptions to the
// budget of giantswarm/giantswarm#38592.
func TestToolDescriptionBudgets(t *testing.T) {
	if err := tooldesc.CheckServer(docsServer(), tooldesc.DefaultBudget); err != nil {
		t.Error(err)
	}
}
