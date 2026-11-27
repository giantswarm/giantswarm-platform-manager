package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func TestWavePreservesFailuresAcrossOutOfOrderMerges(t *testing.T) {
	st := newStack(t)
	action, actor, first := threeStageWave(t, st)
	admin := adminLive(t, st)
	setAccepted(first, statusFalse, modelKeyMissing)
	watch, message, isError := watchCall(t, admin, action.Name)
	require.False(t, isError, message)
	require.Equal(t, actions.StateWaitingForCustomer, watch.State)

	for _, pullRequest := range st.remote.PullRequests() {
		if strings.HasSuffix(pullRequest.Head, "/"+rowan) {
			st.mergeOutside(t, pullRequest.PullRequest, dave)
		}
	}
	populateStage(t, st.inst, action, rowan)
	restoreLast := redEdgeProbe(t, st, actor, rowan)
	watch, message, isError = watchCall(t, admin, action.Name)
	require.False(t, isError, message)
	require.Equal(t, rowan, watch.Installation)
	require.Equal(t, actions.StateFailed, watch.Action.Status.State)
	finished := watch.Action.Status.Rollout.FinishedAt

	for _, pullRequest := range st.remote.PullRequests() {
		if strings.HasSuffix(pullRequest.Head, "/"+alder) {
			st.mergeOutside(t, pullRequest.PullRequest, dave)
		}
	}
	middle := newFakeInstallation()
	st.muster.serve(alder, middle)
	populateStage(t, middle, action, alder)
	setReady(middle, platformRelease, statusFalse, "reconciling", "4.44.1")
	watch, message, isError = watchCall(t, admin, action.Name)
	require.False(t, isError, message)
	require.Equal(t, alder, watch.Installation)
	require.Equal(t, actions.StateRollingOut, watch.State)
	require.Equal(t, actions.StateFailed, watch.Action.Status.State)
	require.NotNil(t, watch.Action.Status.Result)
	require.Equal(t, actions.StateFailed, watch.Action.Status.Result.State)
	require.Contains(t, watch.Action.Status.Result.Message, rowan)
	require.NotNil(t, watch.Action.Status.Result.At)
	require.Equal(t, finished, watch.Action.Status.Rollout.FinishedAt)
	require.NotContains(t, watch.Next, tools.ToolMergeAction)
	_, message, isError = mergeCall(t, actor, action.Name)
	require.True(t, isError, message)
	require.Contains(t, message, actions.StateFailed)

	setReady(middle, platformRelease, statusTrue, "reconciled", "4.44.1")
	restoreMiddle := redEdgeProbe(t, st, actor, alder)
	watch, message, isError = watchCall(t, admin, action.Name)
	require.False(t, isError, message)
	require.Equal(t, alder, watch.Installation)
	require.Equal(t, actions.StateFailed, watch.State)
	require.Equal(t, actions.StateFailed, stageOf(watch.Action, rowan).State)
	require.Contains(t, watch.Action.Status.Result.Message, alder)

	restoreMiddle()
	watch, message, isError = watchCall(t, admin, action.Name)
	require.False(t, isError, message)
	require.Equal(t, alder, watch.Installation)
	require.Equal(t, actions.StateEnabled, watch.State)
	require.Equal(t, actions.StateFailed, watch.Action.Status.State)
	require.NotNil(t, watch.Action.Status.Result)
	require.Equal(t, actions.StateFailed, watch.Action.Status.Result.State)
	require.Contains(t, watch.Action.Status.Result.Message, rowan)
	require.NotContains(t, watch.Action.Status.Result.Message, alder)
	require.Equal(t, finished, watch.Action.Status.Rollout.FinishedAt)
	require.NotContains(t, watch.Next, tools.ToolMergeAction)
	require.NotContains(t, watch.Report, "Next:")
	require.Contains(t, watch.Report, "The action is failed.")

	restoreLast()
	watch, message, isError = watchCall(t, admin, action.Name)
	require.False(t, isError, message)
	require.Equal(t, rowan, watch.Installation)
	require.Equal(t, actions.StateWaitingForCustomer, watch.Action.Status.State)
	setAccepted(first, statusTrue, "ok")
	verifyLive(t, admin, birch)
	recorded := getAction(t, actor, action.Name)
	require.Equal(t, actions.StateEnabled, recorded.Status.State)
	require.Equal(t, actions.StateEnabled, recorded.Status.Result.State)
}

func threeStageWave(t *testing.T, st *stack) (actions.Action, *client.Client, *fakeInstallation) {
	t.Helper()
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	for _, installation := range []string{rowan, alder} {
		st.ghs.addFile(acmeConfigs, installations.Capabilities()[0].EnabledMarker(installation), "muster: {}\n")
	}
	st.ghs.addFile(acmeMCs, extrasKustomizationPath(alder), extrasKustomization)
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(alder), collectionsKustomization(platformDexApp))
	actor := st.mcpClient(t, aliceToken)
	order := []string{birch, alder, rowan}
	dry, message, isError := dryRun(t, actor, tools.ToolReconcileCapability, waveArgs(map[string]any{
		tools.ArgOrder: order, tools.ArgContent: true,
	}))
	require.False(t, isError, message)
	require.Equal(t, order, dry.Order)
	for _, installation := range dry.Installations {
		for _, file := range installation.Files {
			if isSecretFile(file.Path) && strings.Contains(file.Content, "SUPPLIED(") {
				st.ghs.addFile(file.Repository, file.Path, "sops: on record\n")
			}
		}
	}
	seedRemote(t, st)
	message, isError = call(t, actor, tools.ToolReconcileCapability, waveArgs(map[string]any{
		tools.ArgOrder: order, tools.ArgMode: string(tools.ModeCommit),
	}))
	require.False(t, isError, message)
	var wave tools.WaveResult
	require.NoError(t, json.Unmarshal([]byte(message), &wave))
	require.NotNil(t, wave.Action)
	_, message, isError = decide(t, st.mcpClient(t, carolToken), tools.ToolApproveAction, map[string]any{tools.ArgAction: wave.Action.Name})
	require.False(t, isError, message)
	for _, pullRequest := range st.remote.PullRequests() {
		st.remote.SetChecks(pullRequest.PullRequest, commit.ChecksSuccess)
	}
	merged, message, isError := mergeCall(t, actor, wave.Action.Name)
	require.False(t, isError, message)
	require.Equal(t, birch, merged.Stage)
	first := newFakeInstallation()
	st.muster.serve(birch, first)
	populateStage(t, first, *merged.Action, birch)
	return *merged.Action, actor, first
}
