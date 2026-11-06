package tools

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/giantswarm/gitops-commit/commit"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
)

type commitStatusStore struct {
	actions.Store
	update func(context.Context, string, actions.Status) (*actions.Action, error)
}

func (s commitStatusStore) UpdateStatus(ctx context.Context, name string, status actions.Status) (*actions.Action, error) {
	return s.update(ctx, name, status)
}

type stalledCleanupRemote struct{ commit.Remote }

func (stalledCleanupRemote) Close(ctx context.Context, _ commit.PullRequest, _ bool) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestCommitRecordsPullRequestAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	pr := actions.PullRequest{Repository: "acme/configs", Number: 1, State: actions.PullRequestOpen}
	a := &actions.Action{Name: "reconcile-wave-example", Status: actions.Status{State: actions.StatePendingApproval, Rotated: []string{"client-secret"}}}
	store := commitStatusStore{update: func(ctx context.Context, name string, status actions.Status) (*actions.Action, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("receipt inherits caller cancellation: %v", err)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) <= 0 || time.Until(deadline) > commitRecoveryTimeout {
			t.Fatalf("receipt deadline: %v, %v", deadline, ok)
		}
		return &actions.Action{Name: name, Status: status}, nil
	}}
	if err := New(Deps{Actions: store}).recordPullRequest(ctx, a, pr); err != nil {
		t.Fatal(err)
	}
	if len(a.Status.PullRequests) != 1 || a.Status.PullRequests[0] != pr || len(a.Status.Rotated) != 1 {
		t.Fatalf("receipt: %+v", a.Status)
	}
}

func TestCommitRecordsFailureAfterCleanupDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		started := time.Now()
		var recorded actions.Status
		store := commitStatusStore{update: func(ctx context.Context, name string, status actions.Status) (*actions.Action, error) {
			if err := ctx.Err(); err != nil {
				t.Fatalf("failure record shares the exhausted cleanup deadline: %v", err)
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) != commitRecoveryTimeout {
				t.Fatalf("failure record has no fresh deadline: %v, %v", deadline, ok)
			}
			recorded = status
			return &actions.Action{Name: name, Status: status}, nil
		}}
		pr := actions.PullRequest{Repository: "acme/configs", Number: 1, State: actions.PullRequestOpen}
		a := &actions.Action{Name: "reconcile-wave-example", Status: actions.Status{State: actions.StatePendingApproval, Rotated: []string{"client-secret"}}}
		err := New(Deps{Actions: store}).fail(ctx, ToolReconcileCapability, a, stalledCleanupRemote{}, []actions.PullRequest{pr}, context.Canceled)
		if !errors.Is(err, context.Canceled) || time.Since(started) != commitRecoveryTimeout {
			t.Fatalf("cleanup duration %s, error %v", time.Since(started), err)
		}
		if recorded.State != actions.StateFailed || len(recorded.PullRequests) != 1 || recorded.PullRequests[0].State != actions.PullRequestOpen || len(recorded.Rotated) != 1 {
			t.Fatalf("failure receipt: %+v", recorded)
		}
	})
}
