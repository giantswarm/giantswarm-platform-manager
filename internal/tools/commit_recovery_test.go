package tools

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/giantswarm/gitops-commit/commit"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
)

const commitRepository = "acme/configs"

type commitStatusStore struct {
	actions.Store
	action *actions.Action
	update func(context.Context, string, actions.Status) (*actions.Action, error)
}

func TestCommitReceiptRetriesConflictWithoutOverwritingDenial(t *testing.T) {
	seed := actions.Action{Name: "example", Namespace: "platform-manager", ResourceVersion: "1", Status: actions.Status{State: actions.StatePendingApproval, Commit: &actions.CommitStatus{Pending: []actions.PullRequestIntent{{Repository: commitRepository, Head: "platform/example/rowan"}}}}}
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), actions.Unstructured(seed))
	store := actions.New(dynamicClient, seed.Namespace)
	attempts := 0
	dynamicClient.PrependReactor("update", "actions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		attempts++
		object := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		if attempts == 1 {
			denied := seed
			denied.ResourceVersion = "2"
			denied.Status.State = actions.StateDenied
			denied.Status.Approval = &actions.Approval{Decision: actions.DecisionDenied, Reason: "stop"}
			if err := dynamicClient.Tracker().Update(actions.GVR, actions.Unstructured(denied), seed.Namespace); err != nil {
				t.Fatal(err)
			}
			return true, nil, apierrors.NewConflict(actions.GVR.GroupResource(), seed.Name, errors.New("concurrent denial"))
		}
		if object.GetResourceVersion() != "2" {
			t.Fatalf("retry version: %s", object.GetResourceVersion())
		}
		object.SetResourceVersion("3")
		return false, nil, nil
	})
	pr := actions.PullRequest{Repository: commitRepository, Number: 1, Head: "platform/example/rowan", State: actions.PullRequestOpen}
	if err := New(Deps{Actions: store}).recordPullRequest(t.Context(), &seed, pr); err != nil {
		t.Fatal(err)
	}
	stored, err := dynamicClient.Resource(actions.GVR).Namespace(seed.Namespace).Get(t.Context(), seed.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || seed.ResourceVersion != "3" || seed.Status.State != actions.StateDenied || seed.Status.Approval.Reason != "stop" || len(seed.Status.PullRequests) != 1 || len(seed.Status.Commit.Pending) != 0 || stored.GetResourceVersion() != "3" {
		t.Fatalf("receipt overwrote decision: %+v", seed)
	}
}

func (s commitStatusStore) Get(context.Context, string) (*actions.Action, error) {
	action := *s.action
	return &action, nil
}

func (s commitStatusStore) UpdateStatus(ctx context.Context, action *actions.Action, status actions.Status) (*actions.Action, error) {
	return s.update(ctx, action.Name, status)
}

type stalledCleanupRemote struct{ commit.Remote }

func (stalledCleanupRemote) Close(ctx context.Context, _ commit.PullRequest, _ bool) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestCommitRecordsPullRequestAfterCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	pr := actions.PullRequest{Repository: commitRepository, Number: 1, State: actions.PullRequestOpen}
	a := &actions.Action{Name: "reconcile-wave-example", Status: actions.Status{State: actions.StatePendingApproval, Rotated: []string{"client-secret"}}}
	store := commitStatusStore{action: a, update: func(ctx context.Context, name string, status actions.Status) (*actions.Action, error) {
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
		a := &actions.Action{Name: "reconcile-wave-example", Status: actions.Status{State: actions.StatePendingApproval, Rotated: []string{"client-secret"}}}
		store := commitStatusStore{action: a, update: func(ctx context.Context, name string, status actions.Status) (*actions.Action, error) {
			if err := ctx.Err(); err != nil {
				t.Fatalf("failure record shares the exhausted cleanup deadline: %v", err)
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) != commitRecoveryTimeout {
				t.Fatalf("failure record has no fresh deadline: %v, %v", deadline, ok)
			}
			recorded = status
			return &actions.Action{Name: name, Status: status}, nil
		}}
		pr := actions.PullRequest{Repository: commitRepository, Number: 1, State: actions.PullRequestOpen}
		err := New(Deps{Actions: store}).fail(ctx, ToolReconcileCapability, a, stalledCleanupRemote{}, []actions.PullRequest{pr}, context.Canceled)
		if !errors.Is(err, context.Canceled) || time.Since(started) != commitRecoveryTimeout {
			t.Fatalf("cleanup duration %s, error %v", time.Since(started), err)
		}
		if recorded.State != actions.StateFailed || len(recorded.PullRequests) != 1 || recorded.PullRequests[0].State != actions.PullRequestOpen || len(recorded.Rotated) != 1 {
			t.Fatalf("failure receipt: %+v", recorded)
		}
	})
}
