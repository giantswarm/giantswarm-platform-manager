package actions

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The reviewed-governance record (decision #99): one per installation, the
// governance the team last reviewed there, written only by merge_action
// when an approved governance change of an Action is merged through the
// manager. It is never derived from the Actions' order or states: every plan
// compares the configs repository's governance with it, and a difference it
// does not explain (an edit outside the manager, a revert by hand, an action
// merged out of order) is an unreviewed change the next commit asks the team
// to confirm or restore. Only the manager's ServiceAccount writes it.
const (
	RecordKind     = "GovernanceRecord"
	RecordResource = "governancerecords"
)

// RecordGVR is the reviewed-governance record's resource.
var RecordGVR = schema.GroupVersionResource{Group: Group, Version: Version, Resource: RecordResource}

// GovernanceRecord is what the team last reviewed for an installation.
type GovernanceRecord struct {
	ResourceVersion string `json:"-"`
	// Installation names the record (the object's name).
	Installation string `json:"installation"`
	// Governance is the reviewed governance as plan.GovernanceSnapshot JSON.
	Governance string `json:"governance"`
	// Action is the Action whose merge wrote it, ApprovedBy who approved it,
	// HeadSHA the configs pull request's merged head.
	Action     string `json:"action"`
	ApprovedBy string `json:"approvedBy"`
	HeadSHA    string `json:"headSHA,omitempty"`
	// RecordedAt is when it was written, RFC 3339.
	RecordedAt string `json:"recordedAt"`
}

// RecordStore reads and writes the reviewed-governance records.
type RecordStore interface {
	// GetRecord answers ErrNotFound for an installation without a record.
	GetRecord(ctx context.Context, installation string) (*GovernanceRecord, error)
	// PutRecord creates or replaces an installation's record.
	PutRecord(ctx context.Context, r GovernanceRecord) error
}

// GetRecord reads installation's reviewed-governance record.
func (c *Client) GetRecord(ctx context.Context, installation string) (*GovernanceRecord, error) {
	u, err := c.c.Resource(RecordGVR).Namespace(c.ns).Get(ctx, installation, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: governance record %s/%s", ErrNotFound, c.ns, installation)
		}
		return nil, fmt.Errorf("actions: get governance record %s/%s: %w", c.ns, installation, err)
	}
	var r GovernanceRecord
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object["spec"].(map[string]any), &r); err != nil {
		return nil, fmt.Errorf("actions: governance record %s/%s: %w", c.ns, installation, err)
	}
	r.ResourceVersion = u.GetResourceVersion()
	return &r, nil
}

// PutRecord creates installation's record, or replaces it.
func (c *Client) PutRecord(ctx context.Context, r GovernanceRecord) error {
	if r.RecordedAt == "" {
		r.RecordedAt = time.Now().UTC().Format(time.RFC3339)
	}
	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&r)
	if err != nil {
		return err
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/" + Version, "kind": RecordKind,
		"metadata": map[string]any{"name": r.Installation, "namespace": c.ns},
		"spec":     spec,
	}}
	res := c.c.Resource(RecordGVR).Namespace(c.ns)
	old, err := res.Get(ctx, r.Installation, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		_, err = res.Create(ctx, u, metav1.CreateOptions{})
	case err == nil:
		u.SetResourceVersion(old.GetResourceVersion())
		_, err = res.Update(ctx, u, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("actions: write governance record %s/%s: %w", c.ns, r.Installation, err)
	}
	return nil
}
