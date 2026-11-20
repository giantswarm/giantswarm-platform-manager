package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster/mustertest"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

const (
	dryRunResultBridge = "dry-run-result"
	dryRunResultEnv    = "PLATFORMCTL_TEST_DRY_RUN_RESULT"
	allFlag            = "--all"
)

func serveDryRunResultBridge() {
	manager := mustertest.Manager(true)
	for _, tool := range []string{tools.ToolEnableCapability, tools.ToolReconcileCapability} {
		manager["x_"+tools.ToolPrefix+"_"+tool] = func(context.Context, map[string]any) *mcp.CallToolResult {
			return mcp.NewToolResultText(os.Getenv(dryRunResultEnv))
		}
	}
	if err := mcpserver.ServeStdio(mustertest.Bridge(manager)); err != nil {
		os.Exit(1)
	}
}

func TestCapabilityDryRunExitStatus(t *testing.T) {
	const refusal = "sessions.idleTTL: 14d is not accepted"
	for _, target := range []struct {
		name string
		tool string
		args []string
	}{
		{enableCmd, tools.ToolEnableCapability, []string{enableCmd, lab, agentPlatform}},
		{reconcileCmd, tools.ToolReconcileCapability, []string{reconcileCmd, lab, agentPlatform}},
		{"set", tools.ToolReconcileCapability, []string{reconcileCmd, lab, hazel, agentPlatform}},
		{"all", tools.ToolReconcileCapability, []string{reconcileCmd, allFlag, agentPlatform}},
	} {
		for _, output := range []string{outputText, outputJSON} {
			for _, result := range []struct {
				name          string
				installations []tools.DryRun
				code          int
			}{
				{"accepted", []tools.DryRun{{Installation: plan.Installation{Name: lab}}}, exitOK},
				{"refused", []tools.DryRun{{Installation: plan.Installation{Name: lab, Refused: refusal}}}, exitError},
				{"later-refused", []tools.DryRun{{Installation: plan.Installation{Name: lab}}, {Installation: plan.Installation{Name: hazel, Refused: refusal}}}, exitError},
				{"commit-refused", []tools.DryRun{{Installation: plan.Installation{Name: lab, CommitRefused: "review required"}}}, exitOK},
			} {
				t.Run(target.name+"/"+output+"/"+result.name, func(t *testing.T) {
					document, err := json.Marshal(tools.CapabilityResult{
						Tool: target.tool, Capability: agentPlatform, DryRun: true,
						Installations: result.installations,
					})
					require.NoError(t, err)
					t.Setenv(dryRunResultEnv, string(document))
					arguments := append([]string{installationCmd}, target.args...)
					arguments = append(arguments, dryRunFlag, "--output", output)
					if result.code == exitError {
						arguments = append(arguments, "--input", "sessions.idleTTL=14d")
					}
					code, stdout, stderr := bridge(t, dryRunResultBridge, arguments...)
					require.Equal(t, result.code, code, "stdout: %s\nstderr: %s", stdout, stderr)
					require.Empty(t, stderr)
					if output == outputJSON {
						require.JSONEq(t, string(document), stdout)
					} else if result.code == exitError {
						require.Contains(t, stdout, "Refused: "+refusal)
					}
				})
			}
		}
	}
}
