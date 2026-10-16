package tools

import (
	"testing"

	"github.com/giantswarm/mcp-toolkit/tooldesc"
	"github.com/giantswarm/mcp-toolkit/tooldesc/tooldesctest"

	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
)

// The live tools register only with a live client; any one will do here.

// TestToolsDocIsCurrent keeps docs/tools.md — every tool's description and
// input schema as a model reads them — equal to the registered tools.
// TOOLS_DOC_UPDATE=1 rewrites the file (`make tools-doc`).
func TestToolsDocIsCurrent(t *testing.T) {
	tooldesctest.AssertServerDoc(t, New(Deps{Live: &live.Client{}}).MCPServer(),
		"# The tools\n\nGenerated from the registered tools (`make tools-doc`); through muster every tool is `x_"+ToolPrefix+"_<name>`.",
		"../../docs/tools.md")
}

// TestToolDescriptionBudgets holds the registered descriptions to the
// budget of giantswarm/giantswarm#38592.
func TestToolDescriptionBudgets(t *testing.T) {
	if err := tooldesc.CheckServer(New(Deps{Live: &live.Client{}}).MCPServer(), tooldesc.DefaultBudget); err != nil {
		t.Error(err)
	}
}
