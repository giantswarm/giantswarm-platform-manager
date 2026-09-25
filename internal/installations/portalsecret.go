package installations

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// PortalClientSecretPath is where the customer-portal definition renders the
// portal client's Secret (dex-client-backstage, in Dex's namespace) in an
// installation's management-clusters repository: its portal directory.
func PortalClientSecretPath(name string) string {
	return "management-clusters/" + name + "/extras/backstage/" + render.PortalDir + "/" + render.PortalDexClientSecretFile
}

// readPortalClientSecret says whether the portal client's Secret is on
// record for inst, as the person: the customer-portal definition's file in
// its portal directory, or the portal client (backstage) already an extra
// static client of its dex-app configmap patch — the client on record runs
// with whatever Secret carries it. An installation without a repository of
// either kind has none.
func readPortalClientSecret(ctx context.Context, c *gh.Client, inst Installation) (bool, error) {
	if repo := inst.Repositories.ManagementClusters; repo != "" {
		owner, name, err := gh.SplitRepo(repo)
		if err != nil {
			return false, err
		}
		ok, err := exists(ctx, c, owner, name, PortalClientSecretPath(inst.Name))
		if err != nil {
			return false, fmt.Errorf("the portal client's Secret on record: %w", err)
		}
		if ok {
			return true, nil
		}
	}
	if inst.Repositories.Configs == "" {
		return false, nil
	}
	owner, name, err := gh.SplitRepo(inst.Repositories.Configs)
	if err != nil {
		return false, err
	}
	data, err := gh.ReadFile(ctx, c, owner, name, DexPatchPath(inst.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("the portal client's Secret on record: %w", err)
	}
	ids, err := extraStaticClientIDs(data)
	if err != nil {
		return false, fmt.Errorf("the portal client's Secret on record: %s: %w", DexPatchPath(inst.Name), err)
	}
	return slices.Contains(ids, render.PortalDexClientID), nil
}

// extraStaticClientIDs are the ids of a dex-app configmap patch's
// oidc.extraStaticClients.
func extraStaticClientIDs(data string) ([]string, error) {
	var patch struct {
		OIDC struct {
			ExtraStaticClients []struct {
				ID string `yaml:"id"`
			} `yaml:"extraStaticClients"`
		} `yaml:"oidc"`
	}
	if err := yaml.Unmarshal([]byte(data), &patch); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(patch.OIDC.ExtraStaticClients))
	for _, client := range patch.OIDC.ExtraStaticClients {
		ids = append(ids, client.ID)
	}
	return ids, nil
}
