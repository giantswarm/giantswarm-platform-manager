package e2e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

type interruptedCommitRemote struct {
	commit.Remote
	open func(context.Context, commit.Repository, string, string, string, string) (commit.PullRequest, error)
	find func(context.Context, commit.Repository, string) (commit.PullRequest, bool, error)
}

func (r interruptedCommitRemote) OpenPullRequest(ctx context.Context, repository commit.Repository, head, base, title, body string) (commit.PullRequest, error) {
	return r.open(ctx, repository, head, base, title, body)
}

func (r interruptedCommitRemote) FindPullRequest(ctx context.Context, repository commit.Repository, head string) (commit.PullRequest, bool, error) {
	if r.find != nil {
		return r.find(ctx, repository, head)
	}
	return r.Remote.FindPullRequest(ctx, repository, head)
}

func TestCommitRecoversLostCreateResponse(t *testing.T) {
	const lookupRefused = "lookup refused"
	const delayedVisibility = "not visible before denial"
	for _, scenario := range []string{"immediate lookup", lookupRefused, delayedVisibility, "cleanup refused"} {
		t.Run(scenario, func(t *testing.T) {
			visible := scenario != lookupRefused && scenario != delayedVisibility
			st := newStack(t, func(dependencies *tools.Deps) {
				factory := dependencies.Remote
				dependencies.Remote = func(token string) (commit.Remote, error) {
					remote, err := factory(token)
					return interruptedCommitRemote{Remote: remote, open: func(ctx context.Context, repository commit.Repository, head, base, title, body string) (commit.PullRequest, error) {
						_, err := remote.OpenPullRequest(ctx, repository, head, base, title, body)
						if err != nil {
							return commit.PullRequest{}, err
						}
						return commit.PullRequest{}, context.DeadlineExceeded
					}, find: func(ctx context.Context, repository commit.Repository, head string) (commit.PullRequest, bool, error) {
						if err := ctx.Err(); err != nil {
							return commit.PullRequest{}, false, err
						}
						if !visible {
							if scenario == lookupRefused {
								return commit.PullRequest{}, false, errors.New("lookup refused")
							}
							return commit.PullRequest{}, false, nil
						}
						return remote.FindPullRequest(ctx, repository, head)
					}}, err
				}
			})
			fixtures(st.ghs)
			sopsFixtures(t, st.ghs)
			seedRemote(t, st)
			if scenario == "cleanup refused" {
				st.remote.Fail[commit.OpClose] = errors.New("cleanup refused")
			}
			caller := st.mcpClient(t, aliceToken)
			_, text, isError := commitCall(t, caller, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
			if !isError || !strings.Contains(text, context.DeadlineExceeded.Error()) {
				t.Fatalf("lost response: %v %s", isError, text)
			}
			prs := st.remote.PullRequests()
			if len(prs) != 1 {
				t.Fatalf("opened %d PRs", len(prs))
			}
			name := strings.Split(prs[0].Head, "/")[1]
			store := actions.New(st.dyn, actionsNamespace)
			failed, err := store.Get(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}
			if !visible {
				if len(failed.Status.Commit.Pending) != 1 || len(failed.Status.PullRequests) != 0 || !strings.Contains(failed.Status.Result.Message, "unresolved") {
					t.Fatalf("uncertain result was lost: %+v", failed.Status)
				}
				if scenario == delayedVisibility {
					if _, text, isError := decide(t, caller, tools.ToolDenyAction, map[string]any{tools.ArgAction: name, tools.ArgReason: "stop uncertain commit"}); isError {
						t.Fatal(text)
					}
				}
			}
			visible = true
			delete(st.remote.Fail, commit.OpClose)
			if scenario != "immediate lookup" {
				if _, text, isError := decide(t, caller, tools.ToolDenyAction, map[string]any{tools.ArgAction: name, tools.ArgReason: "recover uncertain commit"}); isError {
					t.Fatal(text)
				}
			}
			stored, err := store.Get(t.Context(), name)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored.Status.Commit.Pending) != 0 || len(stored.Status.PullRequests) != 1 || stored.Status.PullRequests[0].State != actions.PullRequestClosed || !st.remote.PullRequests()[0].Closed {
				t.Fatalf("recovery: %+v", stored.Status)
			}
		})
	}
}

func TestCommitPreservesDenialDuringCreation(t *testing.T) {
	var st *stack
	attempts := 0
	st = newStack(t, func(dependencies *tools.Deps) {
		store := dependencies.Actions
		factory := dependencies.Remote
		dependencies.Remote = func(token string) (commit.Remote, error) {
			remote, err := factory(token)
			return interruptedCommitRemote{Remote: remote, open: func(ctx context.Context, repository commit.Repository, head, base, title, body string) (commit.PullRequest, error) {
				attempts++
				name := strings.Split(head, "/")[1]
				action, err := store.Get(ctx, name)
				if err != nil {
					return commit.PullRequest{}, err
				}
				if action.Status.Commit == nil || len(action.Status.Commit.Pending) != 1 || action.Status.Commit.Pending[0].Repository != repository.String() {
					return commit.PullRequest{}, fmt.Errorf("missing durable intent: %+v", action.Status.Commit)
				}
				if attempts == 2 {
					caller := st.mcpClient(t, aliceToken)
					for _, tool := range []string{tools.ToolApproveAction, tools.ToolMergeAction} {
						if text, isError := call(t, caller, tool, map[string]any{tools.ArgAction: name}); !isError || !strings.Contains(text, "still creating") {
							return commit.PullRequest{}, fmt.Errorf("%s accepted incomplete commit: %s", tool, text)
						}
					}
					if _, text, isError := decide(t, caller, tools.ToolDenyAction, map[string]any{tools.ArgAction: name, tools.ArgReason: "stop creation"}); isError {
						return commit.PullRequest{}, fmt.Errorf("deny failed: %s", text)
					}
				}
				return remote.OpenPullRequest(ctx, repository, head, base, title, body)
			}}, err
		}
	})
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	seedRemote(t, st)
	caller := st.mcpClient(t, aliceToken)
	_, text, isError := commitCall(t, caller, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if !isError || !strings.Contains(text, "creation stopped") {
		t.Fatalf("commit ignored denial: %v %s", isError, text)
	}
	prs := st.remote.PullRequests()
	if len(prs) != 2 {
		t.Fatalf("opened %d PRs", len(prs))
	}
	stored, err := actions.New(st.dyn, actionsNamespace).Get(t.Context(), strings.Split(prs[0].Head, "/")[1])
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.State != actions.StateDenied || stored.Status.Approval.Decision != actions.DecisionDenied || stored.Status.Approval.Reason != "stop creation" || stored.Status.Approval.ReviewID != "" || len(stored.Status.Commit.Pending) != 0 {
		t.Fatalf("denial overwritten: %+v", stored.Status)
	}
	for _, pr := range prs {
		if !pr.Closed {
			t.Fatalf("PR %d remained open", pr.Number)
		}
	}
}
