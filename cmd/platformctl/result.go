package main

import (
	"encoding/json"
	"io"

	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func dryRunExitCode(tool string, arguments map[string]any, raw json.RawMessage, stderr io.Writer) int {
	if arguments[tools.ArgDryRun] != true || (tool != tools.ToolEnableCapability && tool != tools.ToolReconcileCapability) {
		return exitOK
	}
	var result tools.CapabilityResult
	if err := decode(raw, &result); err != nil {
		return fail(stderr, err)
	}
	for _, installation := range result.Installations {
		if installation.Refused != "" {
			return exitError
		}
	}
	return exitOK
}
