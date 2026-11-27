package installations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKubernetesUsernameClaimOnRecord(t *testing.T) {
	for _, claim := range []string{"email", "sub", ""} {
		t.Run(claim, func(t *testing.T) {
			manifest := clusterAppManifest("cluster-aws", "10.4.0", "cluster:\n  controlPlane:\n    oidc:\n      usernameClaim: "+claim+"\n")
			repositories := fakeRepos{mapleInst.Repositories.ManagementClusters: {ClusterAppManifestPath(mapleInst.Name): manifest}}
			actual, err := readKubernetesUsernameClaim(t.Context(), repositories.reader(map[string]int{}), mapleInst)
			require.NoError(t, err)
			require.Equal(t, claim, actual)
		})
	}
	actual, err := readKubernetesUsernameClaim(t.Context(), fakeRepos{}.reader(map[string]int{}), mapleInst)
	require.NoError(t, err)
	require.Empty(t, actual)
}
