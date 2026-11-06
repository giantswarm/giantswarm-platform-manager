package actions

import (
	"errors"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCreateReturnsPersistedInitialStatus(t *testing.T) {
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme())
	dynamicClient.PrependReactor("create", "actions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		object := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		delete(object.Object, "status")
		return false, nil, nil
	})
	client := New(dynamicClient, "platform-manager")
	want := Status{State: StatePendingApproval, Rollout: &Rollout{Installations: []InstallationRollout{{Name: "rowan", State: StatePendingApproval}}}}
	created, err := client.Create(t.Context(), Action{Name: "reconcile-wave-example", Status: want})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created.Status, want) {
		t.Fatalf("returned status %+v, want %+v", created.Status, want)
	}
	stored, err := dynamicClient.Resource(GVR).Namespace(client.Namespace()).Get(t.Context(), created.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := fromUnstructured(stored)
	if err != nil || !reflect.DeepEqual(decoded.Status, created.Status) {
		t.Fatalf("stored status %+v differs from response %+v: %v", decoded, created.Status, err)
	}
}

func TestUpdateStatusRejectsStaleAction(t *testing.T) {
	seed := Action{Name: "example", Namespace: "platform-manager", ResourceVersion: "1", Status: Status{State: StatePendingApproval}}
	dynamicClient := fake.NewSimpleDynamicClient(runtime.NewScheme(), Unstructured(seed))
	client := New(dynamicClient, seed.Namespace)
	stale, err := client.Get(t.Context(), seed.Name)
	if err != nil {
		t.Fatal(err)
	}
	denied := seed
	denied.ResourceVersion = "2"
	denied.Status.State = StateDenied
	if _, err := dynamicClient.Resource(GVR).Namespace(seed.Namespace).Update(t.Context(), Unstructured(denied), metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	dynamicClient.PrependReactor("update", "actions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		object := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		if object.GetResourceVersion() != "2" {
			return true, nil, apierrors.NewConflict(GVR.GroupResource(), seed.Name, errors.New("stale resourceVersion"))
		}
		return false, nil, nil
	})
	if _, err := client.UpdateStatus(t.Context(), stale, stale.Status); !apierrors.IsConflict(err) {
		t.Fatalf("stale update: %v", err)
	}
	stored, err := client.Get(t.Context(), seed.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status.State != StateDenied || stored.ResourceVersion != "2" {
		t.Fatalf("denial overwritten: %+v", stored)
	}
}
