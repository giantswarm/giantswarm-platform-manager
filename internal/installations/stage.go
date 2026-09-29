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

// collections is the installation's collections kustomization on record,
// read and decoded once for every fact taken from it. found is false where
// the installation has none.
type collections struct {
	found         bool
	kustomization collectionsKustomization
}

// readCollections reads the installation's collections kustomization as the
// person. An installation without it has none on record: the zero value, no
// error. A file that cannot be read or decoded is an error of the report,
// naming the file.
func readCollections(ctx context.Context, read refReader, inst Installation) (collections, error) {
	path := CollectionsKustomizationPath(inst.Name)
	data, err := read(ctx, inst.Repositories.ManagementClusters, path, "")
	if errors.Is(err, gh.ErrNotFound) {
		return collections{}, nil
	}
	if err != nil {
		return collections{}, fmt.Errorf("the collections on record: %w", err)
	}
	var k collectionsKustomization
	if err := yaml.Unmarshal([]byte(data), &k); err != nil {
		return collections{}, fmt.Errorf("the collections on record: %s in %s: decode: %w", path, inst.Repositories.ManagementClusters, err)
	}
	return collections{found: true, kustomization: k}, nil
}

// stage is the stage the installation's app collection follows (stable,
// testing, staging, ...): the stage of the first remote resource that names
// a collection base. Where the resources name bases on different stages, the
// first one decides. The testing stage runs release candidates, which
// management-cluster-bases' testing stages select for their collections. No
// kustomization, or resources that name no stage, is the empty answer.
func (c collections) stage() string {
	for _, r := range c.kustomization.Resources {
		if _, ok := parseRemoteBase(r); !ok {
			continue
		}
		if m := collectionsStagePath.FindStringSubmatch(r); m != nil {
			return m[1]
		}
	}
	return ""
}
