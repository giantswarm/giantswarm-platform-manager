package tools

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

func (t *Tools) snapshotIssuers(ctx context.Context, action *actions.Action, status *actions.Status, definition installations.Capability, installation string) error {
	if definition.Name != installations.AgentPlatform {
		return nil
	}
	if _, recorded := status.KeptByInstallation[installation]; recorded {
		return nil
	}
	kept := action.KeptOnRecord(installation)
	for _, entry := range kept {
		if entry.List == plan.ListTrustedIssuers && entry.Digest != "" {
			return nil
		}
	}
	client, err := t.gitHubAs(ctx, ToolWatchAction)
	if err != nil {
		return err
	}
	current := *action
	current.Status = *status
	merged := mergedPRs(&current, installation)
	slices.SortStableFunc(merged, func(left, right int) int {
		a, b := status.PullRequests[left].MergedAt, status.PullRequests[right].MergedAt
		if a == nil || b == nil {
			return 0
		}
		return b.Compare(*a)
	})
	var content string
	for _, index := range merged {
		_, content, err = changesValues(ctx, client, definition, installation, status.PullRequests[index])
		if err != nil {
			return err
		}
		if content != "" {
			break
		}
	}
	if content == "" {
		repository, path, ok := strings.Cut(action.Spec.Markers[installation], ":")
		if !ok || path != "installations/"+installation+verify.ValuesSuffix(definition.Name) {
			return nil
		}
		owner, repo, err := gh.SplitRepo(repository)
		if err != nil {
			return err
		}
		content, err = gh.ReadFile(ctx, client, owner, repo, path)
		if err != nil {
			return fmt.Errorf("read issuer configuration on record: %w", err)
		}
	}
	snapshot, err := plan.TrustedIssuerSnapshot(content)
	if err != nil {
		return err
	}
	status.KeptByInstallation = maps.Clone(status.KeptByInstallation)
	if status.KeptByInstallation == nil {
		status.KeptByInstallation = map[string][]plan.Kept{}
	}
	status.KeptByInstallation[installation] = append([]plan.Kept{}, snapshot...)
	return nil
}
