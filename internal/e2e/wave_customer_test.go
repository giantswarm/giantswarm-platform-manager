package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

func TestWaveAdvancesPastCustomerModelActions(t *testing.T) {
	for _, test := range []struct {
		name         string
		mergeOutside bool
		bothWait     bool
		legacyWait   bool
	}{
		{name: "actor merges next stage"},
		{name: "later stage already merged", mergeOutside: true},
		{name: "existing waiting record", mergeOutside: true, legacyWait: true},
		{name: "each customer has a model action", bothWait: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := newStack(t)
			action, actor, first := waveStage1(t, st)
			admin := adminLive(t, st)
			setAccepted(first, statusFalse, modelKeyMissing)
			watched, text, isError := watchCall(t, admin, action.Name)
			require.False(t, isError, text)
			require.Equal(t, birch, watched.Installation)
			require.Equal(t, actions.StateWaitingForCustomer, watched.State)
			require.Equal(t, actions.StateRollingOut, watched.Action.Status.State)
			require.Nil(t, watched.Action.Status.Result)
			require.Nil(t, watched.Action.Status.Rollout.FinishedAt)
			require.Contains(t, watched.Report, openCustomerActions)
			require.Contains(t, watched.Next, rowan)

			if test.mergeOutside {
				if test.legacyWait {
					status := watched.Action.Status
					status.State = actions.StateWaitingForCustomer
					status.Result = &actions.Result{State: actions.StateWaitingForCustomer, Message: "waiting for the customer on birch"}
					finished := time.Now()
					status.Rollout.FinishedAt = &finished
					_, err := actions.New(st.dyn, actionsNamespace).UpdateStatus(t.Context(), watched.Action, status)
					require.NoError(t, err)
				}
				mergeAllOutside(t, st)
			} else {
				require.Contains(t, watched.Next, tools.ToolMergeAction)
				merged, text, isError := mergeCall(t, actor, action.Name)
				require.False(t, isError, text)
				require.Equal(t, rowan, merged.Stage)
				require.NotEmpty(t, merged.Merged)
				require.Equal(t, actions.StateWaitingForCustomer, stageOf(merged.Action, birch).State)
				action = *merged.Action
			}
			populateStage(t, st.inst, action, rowan)
			if test.legacyWait {
				setReady(st.inst, platformRelease, statusFalse, "reconciling", "4.44.1")
				watched, text, isError = watchCall(t, admin, action.Name)
				require.False(t, isError, text)
				require.Equal(t, rowan, watched.Installation)
				require.Equal(t, actions.StateRollingOut, watched.State)
				require.Equal(t, actions.StateRollingOut, watched.Action.Status.State)
				require.Nil(t, watched.Action.Status.Result)
				require.Nil(t, watched.Action.Status.Rollout.FinishedAt)
				setReady(st.inst, platformRelease, statusTrue, "reconciled", "4.44.1")
			}
			if test.bothWait {
				setAccepted(st.inst, statusFalse, modelKeyMissing)
			}
			watched, text, isError = watchCall(t, admin, action.Name)
			require.False(t, isError, text)
			require.Equal(t, rowan, watched.Installation)
			require.Equal(t, actions.StateWaitingForCustomer, watched.Action.Status.State)
			require.NotNil(t, watched.Action.Status.Result)
			require.Equal(t, actions.StateWaitingForCustomer, watched.Action.Status.Result.State)
			require.Contains(t, watched.Action.Status.Result.Message, birch)
			require.NotNil(t, watched.Action.Status.Rollout.FinishedAt)
			require.Equal(t, actions.StateWaitingForCustomer, stageOf(watched.Action, birch).State)
			if test.bothWait {
				require.Equal(t, actions.StateWaitingForCustomer, watched.State)
				require.Contains(t, watched.Action.Status.Result.Message, rowan)
			} else {
				require.Equal(t, actions.StateEnabled, watched.State)
			}

			setAccepted(first, statusTrue, "ok")
			verifyLive(t, admin, birch)
			if test.bothWait {
				recorded := getAction(t, actor, action.Name)
				require.Equal(t, actions.StateWaitingForCustomer, recorded.Status.State)
				require.Contains(t, recorded.Status.Result.Message, rowan)
				require.NotContains(t, recorded.Status.Result.Message, birch)
				setAccepted(st.inst, statusTrue, "ok")
				watched, text, isError = watchCall(t, admin, action.Name)
				require.False(t, isError, text)
				require.Equal(t, rowan, watched.Installation)
				require.Equal(t, actions.StateEnabled, watched.State)
			}
			recorded := getAction(t, actor, action.Name)
			require.Equal(t, actions.StateEnabled, recorded.Status.State)
			require.Equal(t, actions.StateEnabled, recorded.Status.Result.State)
			for _, stage := range recorded.Status.Rollout.Installations {
				require.Equal(t, actions.StateEnabled, stage.State)
			}
		})
	}
}

func TestVerifyCustomerActionDuringWavePreservesRolloutState(t *testing.T) {
	st := newStack(t)
	action, _, first := waveStage1(t, st)
	admin := adminLive(t, st)
	setAccepted(first, statusFalse, modelKeyMissing)
	watched, text, isError := watchCall(t, admin, action.Name)
	require.False(t, isError, text)
	require.Nil(t, watched.Action.Status.Result)
	setAccepted(first, statusTrue, "ok")
	verifyLive(t, admin, birch)
	recorded := getAction(t, admin, action.Name)
	require.Equal(t, actions.StateEnabled, stageOf(&recorded, birch).State)
	require.Equal(t, actions.StateRollingOut, recorded.Status.State)
	require.Nil(t, recorded.Status.Result)
	require.Nil(t, recorded.Status.Rollout.FinishedAt)
}

func TestCustomerActionDoesNotClearFailureInLaterWaveStage(t *testing.T) {
	st := newStack(t)
	action, actor, first := waveStage1(t, st)
	admin := adminLive(t, st)
	setAccepted(first, statusFalse, modelKeyMissing)
	_, text, isError := watchCall(t, admin, action.Name)
	require.False(t, isError, text)
	merged, text, isError := mergeCall(t, actor, action.Name)
	require.False(t, isError, text)
	populateStage(t, st.inst, *merged.Action, rowan)
	restore := redEdgeProbe(t, st, actor, rowan)
	watched, text, isError := watchCall(t, admin, action.Name)
	require.False(t, isError, text)
	require.Equal(t, rowan, watched.Installation)
	require.Equal(t, actions.StateFailed, watched.Action.Status.State)
	setAccepted(first, statusTrue, "ok")
	verifyLive(t, admin, birch)
	recorded := getAction(t, actor, action.Name)
	require.Equal(t, actions.StateFailed, recorded.Status.State)
	require.Equal(t, watched.Action.Status.Result, recorded.Status.Result)
	restore()
	watched, text, isError = watchCall(t, admin, action.Name)
	require.False(t, isError, text)
	require.Equal(t, rowan, watched.Installation)
	require.Equal(t, actions.StateEnabled, watched.Action.Status.State)
}
