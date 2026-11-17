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
// check carries the object's namespace, name, history and creation time. A
// HelmRelease the installation does not have is left out; any other failed
// read fails the whole answer.
func ReleaseHistories(ctx context.Context, opts LiveOptions) ([]Check, error) {
	lv, err := renderLive(opts)
	if err != nil {
		return nil, err
	}
	x := &executor{opts: opts, lv: lv}
	if x.cluster() == nil {
		return nil, nil
	}
	var out []Check
	for _, p := range lv.probes {
		kind, _, _ := strings.Cut(p.Resource, ".")
		if p.Kind != render.Drift || kind != helmReleaseKind || len(p.Expect.Compare) > 0 {
			continue
		}
		obj, err := x.cluster().Get(ctx, p.Namespace, p.Resource, p.Name, Configuration)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		c := check(p)
		c.Releases, c.Created = releasesOf(obj), createdOf(obj)
		out = append(out, c)
	}
	return out, nil
}
