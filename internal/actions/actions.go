// Package actions is the Action record: a namespaced custom resource in the
// manager's API group on the hub, one per enablement or reconcile a person
// commits — who asked for what on which installations, the pull requests,
// the approval, the rollout, the probes and the result. get_action and
// list_actions read it; commit creates it and moves its state. The chart
// ships the CRD.
package actions

import (
	"context"
	"crypto/rand"
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

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
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
	// InputsByInstallation are the inputs each installation was rendered
	// from at commit — the typed inputs over the facts on record — so a
	// verify that has no GitHub token to re-read the facts (the live
	// registration) renders the same document. Keyed by installation.
	InputsByInstallation map[string]map[string]any `json:"inputsByInstallation,omitempty"`
	// Kind is "enable" or "reconcile".
	Kind string `json:"kind"`
	// Customer marks a customer installation as the target: the Account
	// Engineers' channel is told of the review.
	Customer bool `json:"customer,omitempty"`
	// Change is the plan's change in one clause — files by change, generated
	// secrets by name — on the record and in the pull requests; never a value.
	Change string `json:"change,omitempty"`
	// Skipped are the installations of the set a wave left out, and why:
	// never a target, no pull request.
	Skipped []Skipped `json:"skipped,omitempty"`
}

// Skipped is an installation of a wave's set that is not a target.
type Skipped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// Actor is the person the action ran as.
type Actor struct {
	Login string `json:"login"`
	ID    int64  `json:"id,omitempty"`
	Email string `json:"email,omitempty"`
}

// Status is how the action went, under the status subresource.
type Status struct {
	// State is one of the installations' states an action produces: pending
	// approval, rolling out, waiting for the customer, enabled, failed.
	State        string        `json:"state,omitempty"`
	PullRequests []PullRequest `json:"pullRequests,omitempty"`
	// Rotated names the generated values the commit drew anew over a value
	// on record: a new file needed a value frozen in an existing encrypted
	// file, so every file of the name got the new one. Names only.
	Rotated  []string  `json:"rotated,omitempty"`
	Approval *Approval `json:"approval,omitempty"`
	Rollout  *Rollout  `json:"rollout,omitempty"`
	Probes   []Probe   `json:"probes,omitempty"`
	Result   *Result   `json:"result,omitempty"`
}

// PullRequest is one PR the action opened.
type PullRequest struct {
	// Installation is the wave's stage the pull request belongs to.
	Installation string `json:"installation,omitempty"`
	Repository   string `json:"repository"`
	Number       int    `json:"number"`
	URL          string `json:"url,omitempty"`
	State        string `json:"state,omitempty"`
	// Head and HeadSHA are the branch and the commit the pull request was
	// opened with; the merge refuses a head that moved since.
	Head    string `json:"head,omitempty"`
	HeadSHA string `json:"headSha,omitempty"`
}

// Approval is the team review the action asked for and its decision.
type Approval struct {
	Channel       string     `json:"channel,omitempty"`
	ReviewID      string     `json:"reviewId,omitempty"`
	NoticeChannel string     `json:"noticeChannel,omitempty"`
	PostedAt      *time.Time `json:"postedAt,omitempty"`
	Decision      string     `json:"decision,omitempty"`
	DecidedBy     string     `json:"decidedBy,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	At            *time.Time `json:"at,omitempty"`
}

// Rollout is the wave over the installations, in order.
type Rollout struct {
	StartedAt     *time.Time            `json:"startedAt,omitempty"`
	FinishedAt    *time.Time            `json:"finishedAt,omitempty"`
	Installations []InstallationRollout `json:"installations,omitempty"`
}

// InstallationRollout is one installation's place in the wave, with the
// picture the last watch read of it.
type InstallationRollout struct {
	Name    string `json:"name"`
	State   string `json:"state,omitempty"`
	Message string `json:"message,omitempty"`
	// Objects are the Flux objects the render names on the installation as
	// the last watch read them, each with its Ready condition and revision.
	Objects []RolloutObject `json:"objects,omitempty"`
	// WatchedAt and WatchedBy say when the last watch read the installation
	// and as whom; ReportedAt when the stage's report reached the review's
	// thread (cleared when the state moves on).
	WatchedAt  *time.Time `json:"watchedAt,omitempty"`
	WatchedBy  string     `json:"watchedBy,omitempty"`
	ReportedAt *time.Time `json:"reportedAt,omitempty"`
}

// RolloutObject is one Flux object of the rollout as the watch read it:
// Ready is the condition's status (True, False, Unknown; "" when the object
// could not be read, Message saying why), Revision what the object reports
// as applied or attempted.
type RolloutObject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Ready     string `json:"ready,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Message   string `json:"message,omitempty"`
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

// InputsOnRecord are the inputs installation was rendered from, as the
// record carries them: its entry of InputsByInstallation, else the action's
// inputs when they are a single installation's merged document (the shape
// commit records for one installation), else nil.
func (a Action) InputsOnRecord(installation string) map[string]any {
	if in, ok := a.Spec.InputsByInstallation[installation]; ok {
		return in
	}
	if facts, ok := a.Spec.Inputs["installation"].(map[string]any); ok {
		if name, _ := facts["name"].(string); name == installation {
			return a.Spec.Inputs
		}
	}
	return nil
}

// The states an Action carries in status.state. pending approval, rolling
// out, waiting for the customer, enabled, drifted and failed are the
// installations' states an action produces (installations.State); refused is
// the action's own: the opt-in gate refused it before any write, and the
// installation's state read from its repositories stands.
const (
	StatePendingApproval    = string(installations.StatePendingApproval)
	StateFailed             = string(installations.StateFailed)
	StateRefused            = "refused"
	StateRollingOut         = string(installations.StateRollingOut)
	StateWaitingForCustomer = string(installations.StateWaitingForCustomer)
	StateEnabled            = string(installations.StateEnabled)
	StateDrifted            = string(installations.StateDrifted)
	StateDenied             = "denied"
)

// InstallationState is the state the action gives installation: its stage of
// the rollout when the rollout has one, else the action's state. A stage the
// wave never reached answers "" — the state read from the files stands.
func (a Action) InstallationState(installation string) string {
	if a.Status.Rollout != nil {
		for _, r := range a.Status.Rollout.Installations {
			if r.Name == installation {
				return r.State
			}
		}
	}
	return a.Status.State
}

// StagePullRequests are the pull requests of one stage of the wave; a pull
// request recorded without a stage belongs to the action's first installation.
func (a Action) StagePullRequests(installation string) []int {
	var idx []int
	for i, pr := range a.Status.PullRequests {
		stage := pr.Installation
		if stage == "" && len(a.Spec.Installations) > 0 {
			stage = a.Spec.Installations[0]
		}
		if stage == installation {
			idx = append(idx, i)
		}
	}
	return idx
}

// The kinds of an action, spec.kind.
const (
	KindEnable    = "enable"
	KindReconcile = "reconcile"
)

// The states of a pull request the action opened: open until merged as the
// actor or closed by a denial.
const (
	PullRequestOpen   = "open"
	PullRequestMerged = "merged"
	PullRequestClosed = "closed"
)

// The decisions of an approval.
const (
	DecisionApproved = "approved"
	DecisionDenied   = "denied"
)

// Writer creates Actions and moves their status; commit writes with it, as
// the manager's own ServiceAccount.
type Writer interface {
	// Create stores a new Action; its name must be unique in the namespace.
	Create(ctx context.Context, a Action) (*Action, error)
	// UpdateStatus replaces the status of the Action called name.
	UpdateStatus(ctx context.Context, name string, s Status) (*Action, error)
}

// Store reads and writes Actions: what the tools run with.
type Store interface {
	Reader
	Writer
}

// Create stores a new Action.
func (c *Client) Create(ctx context.Context, a Action) (*Action, error) {
	a.Namespace = c.ns
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	u, err := c.c.Resource(GVR).Namespace(c.ns).Create(ctx, Unstructured(a), metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("actions: create %s/%s: %w", c.ns, a.Name, err)
	}
	// The status subresource is written apart from the object: a Create
	// carries none, so the initial status follows in its own write.
	if _, err := c.UpdateStatus(ctx, a.Name, a.Status); err != nil {
		return nil, err
	}
	return fromUnstructured(u)
}

// UpdateStatus replaces the status of the Action called name.
func (c *Client) UpdateStatus(ctx context.Context, name string, s Status) (*Action, error) {
	res := c.c.Resource(GVR).Namespace(c.ns)
	u, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("actions: status of %s/%s: %w", c.ns, name, err)
	}
	status, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(&s)
	u.Object["status"] = status
	u, err = res.UpdateStatus(ctx, u, metav1.UpdateOptions{})
	if err != nil {
		return nil, fmt.Errorf("actions: update status of %s/%s: %w", c.ns, name, err)
	}
	return fromUnstructured(u)
}

// NewName is a fresh Action name: <kind>-<installation>-<six random
// lowercase alphanumerics>, a DNS label the API server accepts.
func NewName(kind, installation string) (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("actions: name: %w", err)
	}
	for i, b := range suffix {
		suffix[i] = alphabet[int(b)%len(alphabet)]
	}
	return kind + "-" + installation + "-" + string(suffix), nil
}
