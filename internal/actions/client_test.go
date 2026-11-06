package actions

import (
	"reflect"
	"testing"

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
