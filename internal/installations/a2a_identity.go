package installations

import (
	"context"
	"errors"
	"fmt"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

func readKubernetesUsernameClaim(ctx context.Context, read Reader, installation Installation) (string, error) {
	manifest, err := read(ctx, installation.Repositories.ManagementClusters, ClusterAppManifestPath(installation.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("kubernetes OIDC username claim on record: %w", err)
	}
	cluster, err := parseClusterApp(manifest)
	if err != nil {
		return "", fmt.Errorf("kubernetes OIDC username claim on record: %w", err)
	}
	if cluster == nil {
		return "", nil
	}
	return cluster.values.Cluster.ControlPlane.OIDC.UsernameClaim, nil
}
