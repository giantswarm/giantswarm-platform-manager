package verify

import (
	"context"
	"errors"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// ReleaseHistories reads, as the person, the release history of each
// HelmRelease whose whole values a drift probe of opts' render compares: the
// releases the rollout watch proves a merge against (Check.Releases). Each
// check of read carries the object's namespace, name, history and creation
// time. A HelmRelease the installation does not have — the apiserver's
// NotFound for it, nothing else — is a check of absent with its namespace and
// name alone; any other failed read (forbidden, no answer) fails the whole
// answer. With no cluster to read, both are empty.
func ReleaseHistories(ctx context.Context, opts LiveOptions) (read, absent []Check, err error) {
	lv, err := renderLive(opts)
	if err != nil {
		return nil, nil, err
	}
	x := &executor{opts: opts, lv: lv}
	if x.cluster() == nil {
		return nil, nil, nil
	}
	for _, p := range lv.probes {
		kind, _, _ := strings.Cut(p.Resource, ".")
		if p.Kind != render.Drift || kind != helmReleaseKind || len(p.Expect.Compare) > 0 {
			continue
		}
		c := check(p)
		obj, err := x.cluster().Get(ctx, p.Namespace, p.Resource, p.Name, Configuration)
		if errors.Is(err, ErrNotFound) {
			absent = append(absent, c)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		c.Releases, c.Created = releasesOf(obj), createdOf(obj)
		read = append(read, c)
	}
	return read, absent, nil
}
