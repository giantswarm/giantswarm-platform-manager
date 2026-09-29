package installations

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// collectionsStagePath matches the stage of a collection base in a remote
// resource: bases/collections/<provider>/stages/<stage> of the fleet's base.
var collectionsStagePath = regexp.MustCompile(`//bases/collections/[^/?]+/stages/([^/?]+)`)

// readCollectionsStage reads the stage the installation's app collection
// follows (stable, testing, staging, ...): the stage of the collection base
// its collections kustomization names as a remote resource. The testing stage
// runs release candidates, which management-cluster-bases' testing stages
// select for their collections. An installation without the kustomization,
// or one whose resources name no stage, has none on record: the empty answer,
// no error.
func readCollectionsStage(ctx context.Context, read refReader, inst Installation) (string, error) {
	path := CollectionsKustomizationPath(inst.Name)
	data, err := read(ctx, inst.Repositories.ManagementClusters, path, "")
	if errors.Is(err, gh.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("the collections stage on record: %w", err)
	}
	var k collectionsKustomization
	if err := yaml.Unmarshal([]byte(data), &k); err != nil {
		return "", fmt.Errorf("the collections stage on record: %s in %s: decode: %w", path, inst.Repositories.ManagementClusters, err)
	}
	for _, r := range k.Resources {
		if _, ok := parseRemoteBase(r); !ok {
			continue
		}
		if m := collectionsStagePath.FindStringSubmatch(r); m != nil {
			return m[1], nil
		}
	}
	return "", nil
}
