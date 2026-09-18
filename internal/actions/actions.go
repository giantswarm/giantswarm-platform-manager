// Package actions is the Action record: a namespaced custom resource in the
// manager's API group on the hub, one per enablement or reconcile a person
// commits — who asked for what on which installations, the pull requests,
// the approval, the rollout, the probes and the result. get_action and
// list_actions read it; commit creates it. The chart ships the CRD.
package actions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// The API group and version of the Action, as the chart's CRD declares them.
const (
	Group    = "platform-manager.giantswarm.io"
	Version  = "v1alpha1"
	Kind     = "Action"
	Resource = "actions"
)

// GVR is the Action's group, version and resource.
var GVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: Resource}

// ErrNotFound is an Action name the hub does not have.
var ErrNotFound = errors.New("action not found")

// Action is the record as the tools answer it.
type Action struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	CreatedAt time.Time `json:"createdAt"`
	Spec      Spec      `json:"spec"`
	Status    Status    `json:"status"`
}

// Spec is what was asked: by whom, on which installations, which capability,
// with which inputs. Written once by commit.
type Spec struct {
	Actor         Actor          `json:"actor"`
	Capability    string         `json:"capability"`
	Installations []string       `json:"installations"`
	Inputs        map[string]any `json:"inputs,omitempty"`
	// Kind is "enable" or "reconcile".
	Kind string `json:"kind"`
}

// Actor is the person the action ran as.
type Actor struct {
	Login string `json:"login"`
	ID    int64  `json:"id,omitempty"`
}

// Status is how the action went, under the status subresource.
type Status struct {
	// State is one of the installations' states an action produces: pending
	// approval, rolling out, waiting for the customer, enabled, failed.
	State        string        `json:"state,omitempty"`
	PullRequests []PullRequest `json:"pullRequests,omitempty"`
	Approval     *Approval     `json:"approval,omitempty"`
	Rollout      *Rollout      `json:"rollout,omitempty"`
	Probes       []Probe       `json:"probes,omitempty"`
	Result       *Result       `json:"result,omitempty"`
}

// PullRequest is one PR the action opened.
type PullRequest struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url,omitempty"`
	State      string `json:"state,omitempty"`
}

// Approval is the team review the action asked for and its decision.
type Approval struct {
	Channel   string     `json:"channel,omitempty"`
	ReviewID  string     `json:"reviewId,omitempty"`
	Decision  string     `json:"decision,omitempty"`
	DecidedBy string     `json:"decidedBy,omitempty"`
	Reason    string     `json:"reason,omitempty"`
	At        *time.Time `json:"at,omitempty"`
}

// Rollout is the wave over the installations, in order.
type Rollout struct {
	StartedAt     *time.Time            `json:"startedAt,omitempty"`
	FinishedAt    *time.Time            `json:"finishedAt,omitempty"`
	Installations []InstallationRollout `json:"installations,omitempty"`
}

// InstallationRollout is one installation's place in the wave.
type InstallationRollout struct {
	Name    string `json:"name"`
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
}

// Probe is one probe's outcome on one installation.
type Probe struct {
	ID           string     `json:"id"`
	Installation string     `json:"installation,omitempty"`
	Result       string     `json:"result,omitempty"`
	Message      string     `json:"message,omitempty"`
	At           *time.Time `json:"at,omitempty"`
}

// Result is the action's final word.
type Result struct {
	State   string     `json:"state"`
	Message string     `json:"message,omitempty"`
	At      *time.Time `json:"at,omitempty"`
}

// Filter narrows list_actions.
type Filter struct {
	Installation string
	Capability   string
}

// Reader reads Actions on the hub.
type Reader interface {
	Get(ctx context.Context, name string) (*Action, error)
	// List answers every Action matching f, newest first.
	List(ctx context.Context, f Filter) ([]Action, error)
	// Namespace is where the Actions live.
	Namespace() string
}

// Client reads Actions through the dynamic client, as the manager's own
// ServiceAccount: the record is the manager's, not the person's.
type Client struct {
	c  dynamic.Interface
	ns string
}

// InCluster builds the reader from the pod's ServiceAccount, for namespace.
func InCluster(namespace string) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("actions: in-cluster config: %w", err)
	}
	c, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("actions: client: %w", err)
	}
	return New(c, namespace), nil
}

// New builds the reader over c for namespace (the fake in tests).
func New(c dynamic.Interface, namespace string) *Client { return &Client{c: c, ns: namespace} }

// Namespace is where the Actions live.
func (c *Client) Namespace() string { return c.ns }

// Get reads one Action by name.
func (c *Client) Get(ctx context.Context, name string) (*Action, error) {
	u, err := c.c.Resource(GVR).Namespace(c.ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: %s/%s", ErrNotFound, c.ns, name)
		}
		return nil, fmt.Errorf("actions: get %s/%s: %w", c.ns, name, err)
	}
	return fromUnstructured(u)
}

// List answers every Action matching f, newest first.
func (c *Client) List(ctx context.Context, f Filter) ([]Action, error) {
	list, err := c.c.Resource(GVR).Namespace(c.ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("actions: list in %s: %w", c.ns, err)
	}
	out := make([]Action, 0, len(list.Items))
	for i := range list.Items {
		a, err := fromUnstructured(&list.Items[i])
		if err != nil {
			return nil, err
		}
		if f.matches(*a) {
			out = append(out, *a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (f Filter) matches(a Action) bool {
	if f.Capability != "" && a.Spec.Capability != f.Capability {
		return false
	}
	return f.Installation == "" || a.Includes(f.Installation)
}

func fromUnstructured(u *unstructured.Unstructured) (*Action, error) {
	var body struct {
		Spec   Spec   `json:"spec"`
		Status Status `json:"status"`
	}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &body); err != nil {
		return nil, fmt.Errorf("actions: %s/%s: %w", u.GetNamespace(), u.GetName(), err)
	}
	return &Action{Name: u.GetName(), Namespace: u.GetNamespace(), CreatedAt: u.GetCreationTimestamp().Time, Spec: body.Spec, Status: body.Status}, nil
}

// Unstructured renders a as the resource the API server stores — the shape
// commit writes and the tests seed.
func Unstructured(a Action) *unstructured.Unstructured {
	spec, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&a.Spec)
	status, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&a.Status)
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version, "kind": Kind,
		"metadata": map[string]any{"name": a.Name, "namespace": a.Namespace, "creationTimestamp": a.CreatedAt.UTC().Format(time.RFC3339)},
		"spec":     spec, "status": status,
	}}
	return u
}

// Includes says whether the action names installation.
func (a Action) Includes(installation string) bool {
	return slices.Contains(a.Spec.Installations, installation)
}
