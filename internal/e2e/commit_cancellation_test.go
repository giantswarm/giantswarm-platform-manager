package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

type checkedActionStore struct {
	actions.Store
	afterUpdate func(actions.Action)
	failReceipt bool
}

func (s *checkedActionStore) UpdateStatus(ctx context.Context, action *actions.Action, status actions.Status) (*actions.Action, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.failReceipt && len(status.PullRequests) > 0 {
		s.failReceipt = false
		return nil, errors.New("receipt write refused")
	}
	a, err := s.Store.UpdateStatus(ctx, action, status)
	if err == nil && s.afterUpdate != nil {
		s.afterUpdate(*a)
	}
	return a, err
}

type checkedCommitRemote struct {
	commit.Remote
	beforeOpen func(context.Context, string) error
}

func (r checkedCommitRemote) OpenPullRequest(ctx context.Context, repository commit.Repository, head, base, title, body string) (commit.PullRequest, error) {
	if err := r.beforeOpen(ctx, head); err != nil {
		return commit.PullRequest{}, err
	}
	return r.Remote.OpenPullRequest(ctx, repository, head, base, title, body)
}

func (r checkedCommitRemote) Close(ctx context.Context, pr commit.PullRequest, deleteBranch bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.Remote.Close(ctx, pr, deleteBranch)
}

func TestCommitCancellationRecordsEveryOpenedPullRequest(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		wave        bool
		refuseClose bool
	}{
		{name: "single"},
		{name: "wave", wave: true},
		{name: "wave with refused cleanup", wave: true, refuseClose: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			waiting := make(chan struct{})
			recovered := make(chan actions.Action, 1)
			var store *checkedActionStore
			attempts := 0
			cancelAt := 2
			if scenario.wave {
				cancelAt = 3
			}
			st := newStack(t, func(dependencies *tools.Deps) {
				store = &checkedActionStore{Store: dependencies.Actions, afterUpdate: func(a actions.Action) {
					if a.Status.State == actions.StateFailed {
						select {
						case recovered <- a:
						default:
						}
					}
				}}
				dependencies.Actions = store
				factory := dependencies.Remote
				dependencies.Remote = func(token string) (commit.Remote, error) {
					remote, err := factory(token)
					return checkedCommitRemote{Remote: remote, beforeOpen: func(ctx context.Context, head string) error {
						a, err := store.Get(ctx, strings.Split(head, "/")[1])
						if err != nil {
							return err
						}
						if len(a.Status.PullRequests) != attempts {
							return fmt.Errorf("before PR %d, Action records %d PRs", attempts+1, len(a.Status.PullRequests))
						}
						if scenario.wave && (a.Status.Rollout == nil || len(a.Status.Rollout.Installations) != 2) {
							return fmt.Errorf("wave metadata missing: %+v", a.Status)
						}
						attempts++
						if attempts == cancelAt {
							close(waiting)
							<-ctx.Done()
							return ctx.Err()
						}
						return nil
					}}, err
				}
			})
			fixtures(st.ghs)
			sopsFixtures(t, st.ghs)
			if scenario.wave {
				st.ghs.addFile(acmeConfigs, installations.Capabilities()[0].EnabledMarker(rowan), "muster: {}\n")
			}
			seedRemote(t, st)
			if scenario.refuseClose {
				st.remote.Fail[commit.OpClose] = errors.New("close refused")
			}
			caller := st.mcpClient(t, aliceToken)
			tool := tools.ToolEnableCapability
			arguments := map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil), tools.ArgReason: commitReason, tools.ArgMode: string(tools.ModeCommit)}
			if scenario.wave {
				tool = tools.ToolReconcileCapability
				arguments = waveArgs(map[string]any{tools.ArgMode: string(tools.ModeCommit)})
			}
			requestContext, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			finished := make(chan string, 1)
			go func() {
				result, err := caller.CallTool(requestContext, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: tool, Arguments: arguments}})
				finished <- fmt.Sprintf("%+v, %v", result, err)
			}()
			select {
			case <-waiting:
				cancel()
			case result := <-finished:
				t.Fatalf("commit finished before cancellation: %s", result)
			case <-requestContext.Done():
				t.Fatal("commit never reached the cancellation point")
			}
			var failed actions.Action
			select {
			case failed = <-recovered:
			case <-time.After(10 * time.Second):
				t.Fatal("cancelled commit did not record its failure")
			}
			if len(failed.Status.PullRequests) != cancelAt-1 || len(st.remote.PullRequests()) != cancelAt-1 || !strings.Contains(failed.Status.Result.Message, context.Canceled.Error()) {
				t.Fatalf("failure receipt: %+v", failed.Status)
			}
			for i, pr := range st.remote.PullRequests() {
				wantState := actions.PullRequestClosed
				if scenario.refuseClose {
					wantState = actions.PullRequestOpen
				}
				if pr.Closed == scenario.refuseClose || failed.Status.PullRequests[i].State != wantState || failed.Status.PullRequests[i].Number != pr.Number || failed.Status.PullRequests[i].HeadSHA != pr.HeadSHA {
					t.Fatalf("remote %+v, receipt %+v", pr, failed.Status.PullRequests[i])
				}
			}
			if scenario.refuseClose {
				if _, text, isError := mergeCall(t, caller, failed.Name); !isError || !strings.Contains(text, actions.StateFailed) {
					t.Fatalf("merge of interrupted wave: %v %s", isError, text)
				}
				delete(st.remote.Fail, commit.OpClose)
				if _, text, isError := decide(t, caller, tools.ToolDenyAction, map[string]any{tools.ArgAction: failed.Name, tools.ArgReason: "withdraw interrupted wave"}); isError {
					t.Fatalf("deny interrupted wave: %s", text)
				}
				for _, pr := range st.remote.PullRequests() {
					if !pr.Closed {
						t.Fatalf("deny left PR open: %+v", pr)
					}
				}
			}
		})
	}
}

func TestCommitStopsWhenAPullRequestCannotBeRecorded(t *testing.T) {
	st := newStack(t, func(dependencies *tools.Deps) {
		dependencies.Actions = &checkedActionStore{Store: dependencies.Actions, failReceipt: true}
	})
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	seedRemote(t, st)
	caller := st.mcpClient(t, aliceToken)
	_, text, isError := commitCall(t, caller, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if !isError || !strings.Contains(text, "receipt write refused") {
		t.Fatalf("receipt failure: %v %s", isError, text)
	}
	if prs := st.remote.PullRequests(); len(prs) != 1 || !prs[0].Closed {
		t.Fatalf("PRs after receipt failure: %+v", prs)
	}
	if got := listActionsOf(t, caller, rowan); len(got) != 1 || got[0].Status.State != actions.StateFailed || len(got[0].Status.PullRequests) != 1 || got[0].Status.PullRequests[0].State != actions.PullRequestClosed {
		t.Fatalf("failure receipt: %+v", got)
	}
}
