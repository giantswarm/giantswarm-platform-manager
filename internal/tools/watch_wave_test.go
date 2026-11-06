package tools

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
)

func TestWatchPrioritizesMergedStagesOverCustomerWaits(t *testing.T) {
	for _, merged := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "merged"}[merged], func(t *testing.T) {
			state := actions.PullRequestOpen
			if merged {
				state = actions.PullRequestMerged
			}
			action := &actions.Action{Spec: actions.Spec{Installations: []string{birch, rowan}}, Status: actions.Status{
				PullRequests: []actions.PullRequest{{Installation: birch, State: actions.PullRequestMerged}, {Installation: rowan, State: state}},
				Rollout:      &actions.Rollout{Installations: []actions.InstallationRollout{{Name: birch, State: actions.StateWaitingForCustomer}, {Name: rowan, State: actions.StateRollingOut, Message: stageQueued}}},
			}}
			index, why := watchedStage(action, action.Status.Rollout)
			require.Empty(t, why)
			if merged {
				require.Equal(t, 1, index)
				require.Contains(t, nextAfter(action, action.Status, 0), ToolWatchAction)
				require.NotContains(t, nextAfter(action, action.Status, 0), ToolMergeAction)
			} else {
				require.Zero(t, index)
				require.Contains(t, nextAfter(action, action.Status, 0), ToolMergeAction)
			}
		})
	}
}

func TestWatchRotatesThroughCustomerWaits(t *testing.T) {
	older := time.Now().Add(-time.Minute)
	newer := time.Now()
	rollout := &actions.Rollout{Installations: []actions.InstallationRollout{
		{Name: birch, State: actions.StateWaitingForCustomer, WatchedAt: &older},
		{Name: rowan, State: actions.StateWaitingForCustomer, WatchedAt: &newer},
	}}
	index, why := watchedStage(&actions.Action{}, rollout)
	require.Empty(t, why)
	require.Zero(t, index)
	latest := newer.Add(time.Minute)
	rollout.Installations[0].WatchedAt = &latest
	index, why = watchedStage(&actions.Action{}, rollout)
	require.Empty(t, why)
	require.Equal(t, 1, index)
	rollout.Installations[0].WatchedAt = nil
	index, why = watchedStage(&actions.Action{}, rollout)
	require.Empty(t, why)
	require.Zero(t, index)
}

func TestWatchReadsMergedStagesBeyondAnUnmergedStage(t *testing.T) {
	const alder = "alder"
	action := &actions.Action{Spec: actions.Spec{Installations: []string{birch, rowan, alder}}, Status: actions.Status{
		PullRequests: []actions.PullRequest{{Installation: birch, State: actions.PullRequestMerged}, {Installation: rowan, State: actions.PullRequestOpen}, {Installation: alder, State: actions.PullRequestMerged}},
		Rollout: &actions.Rollout{Installations: []actions.InstallationRollout{
			{Name: birch, State: actions.StateWaitingForCustomer}, {Name: rowan, State: actions.StateRollingOut}, {Name: alder, State: actions.StateRollingOut},
		}},
	}}
	index, why := watchedStage(action, action.Status.Rollout)
	require.Empty(t, why)
	require.Equal(t, 2, index)
}

func TestApplyStageAggregatesCustomerWaitsAcrossTheWave(t *testing.T) {
	for _, test := range []struct {
		name   string
		states []string
		index  int
		want   string
	}{
		{"first waits", []string{actions.StateWaitingForCustomer, actions.StateRollingOut}, 0, actions.StateRollingOut},
		{"last enabled", []string{actions.StateWaitingForCustomer, actions.StateEnabled}, 1, actions.StateWaitingForCustomer},
		{"last waits", []string{actions.StateEnabled, actions.StateWaitingForCustomer}, 1, actions.StateWaitingForCustomer},
		{"both wait", []string{actions.StateWaitingForCustomer, actions.StateWaitingForCustomer}, 1, actions.StateWaitingForCustomer},
		{"earlier wait clears", []string{actions.StateEnabled, actions.StateWaitingForCustomer}, 0, actions.StateWaitingForCustomer},
		{"last wait clears", []string{actions.StateEnabled, actions.StateEnabled}, 0, actions.StateEnabled},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := actions.Status{State: actions.StateWaitingForCustomer, Result: &actions.Result{State: actions.StateWaitingForCustomer}, Rollout: &actions.Rollout{FinishedAt: now()}}
			for i, state := range test.states {
				status.Rollout.Installations = append(status.Rollout.Installations, actions.InstallationRollout{Name: []string{birch, rowan}[i], State: state})
			}
			applyStage(&status, test.index, actions.StateWaitingForCustomer)
			require.Equal(t, test.want, status.State)
			if test.want == actions.StateRollingOut {
				require.Nil(t, status.Result)
				require.Nil(t, status.Rollout.FinishedAt)
			} else {
				require.NotNil(t, status.Result)
				require.Equal(t, test.want, status.Result.State)
				require.NotNil(t, status.Rollout.FinishedAt)
				if test.want == actions.StateWaitingForCustomer {
					for i, state := range test.states {
						if state == actions.StateWaitingForCustomer {
							require.Contains(t, status.Result.Message, status.Rollout.Installations[i].Name)
						}
					}
				}
			}
		})
	}
}

func TestCustomerWaitCannotClearAnotherStagesFailure(t *testing.T) {
	failure := &actions.Result{State: actions.StateFailed, Message: "rowan failed"}
	status := actions.Status{State: actions.StateFailed, Result: failure, Rollout: &actions.Rollout{FinishedAt: now(), Installations: []actions.InstallationRollout{
		{Name: birch, State: actions.StateEnabled}, {Name: rowan, State: actions.StateFailed, Message: probeRed + "failed"},
	}}}
	applyStage(&status, 0, actions.StateWaitingForCustomer)
	require.Equal(t, actions.StateFailed, status.State)
	require.Same(t, failure, status.Result)
	require.NotNil(t, status.Rollout.FinishedAt)
}

func TestStageFailureAndRecoveryPreserveLaterMergedResults(t *testing.T) {
	for _, laterState := range []string{actions.StateEnabled, actions.StateWaitingForCustomer, actions.StateRollingOut} {
		t.Run(laterState, func(t *testing.T) {
			status := actions.Status{State: actions.StateRollingOut,
				PullRequests: []actions.PullRequest{{Installation: birch, State: actions.PullRequestMerged}, {Installation: rowan, State: actions.PullRequestMerged}},
				Rollout: &actions.Rollout{Installations: []actions.InstallationRollout{
					{Name: birch, State: actions.StateFailed, Message: probeRed + "failed"}, {Name: rowan, State: laterState, Message: "recorded"},
				}},
			}
			later := status.Rollout.Installations[1]
			applyStage(&status, 0, actions.StateRollingOut)
			require.Equal(t, actions.StateFailed, status.State)
			require.Equal(t, later, status.Rollout.Installations[1])
			status.Rollout.Installations[0].State = actions.StateEnabled
			applyStage(&status, 0, actions.StateFailed)
			require.Equal(t, laterState, status.State)
			require.Equal(t, later, status.Rollout.Installations[1])
		})
	}
}

func TestStartingAStagePreservesLaterCustomerWaits(t *testing.T) {
	status := actions.Status{State: actions.StateWaitingForCustomer, Result: &actions.Result{}, Rollout: &actions.Rollout{FinishedAt: now(), Installations: []actions.InstallationRollout{
		{Name: birch, State: actions.StateRollingOut}, {Name: rowan, State: actions.StateWaitingForCustomer, Message: "recorded"},
	}}}
	later := status.Rollout.Installations[1]
	startStage(&status, 0)
	require.Equal(t, later, status.Rollout.Installations[1])
	require.Equal(t, actions.StateRollingOut, status.State)
	require.Nil(t, status.Result)
	require.Nil(t, status.Rollout.FinishedAt)
	require.NotNil(t, status.Rollout.StartedAt)
}
