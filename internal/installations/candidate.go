package installations

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// PlatformRepository is where agent-platform's releases are cut: a stable
// vX.Y.Z by promotion, a candidate vX.Y.Z-rc.N on every releasable push.
const PlatformRepository = "giantswarm/agent-platform"

// releaseCandidateMajor is the major line that runs release candidates:
// gitops.prereleases is the 4 chart line's.
const releaseCandidateMajor = 4

// maxReleasePages bounds the releases read, 100 to a page: the bound devctl
// release promote reads within.
const maxReleasePages = 10

// The tags agent-platform's releases carry.
var (
	platformStableTag    = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	platformCandidateTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$`)
)

// PlatformCandidate is agent-platform's newest release candidate of a major
// line that no stable release of the line has caught up with yet, and the
// line's latest stable release ("" where the releases read carry none).
type PlatformCandidate struct {
	Candidate string
	Stable    string
}

// releasesPage reads one page of a repository's releases as the person,
// newest first, and whether a further page exists.
type releasesPage func(ctx context.Context, repository string, page int) ([]gh.Release, bool, error)

// releasesAs reads through the person's GitHub client.
func releasesAs(c *gh.Client) releasesPage {
	return func(ctx context.Context, repository string, page int) ([]gh.Release, bool, error) {
		owner, repo, err := gh.SplitRepo(repository)
		if err != nil {
			return nil, false, err
		}
		return gh.ReleasesPage(ctx, c, owner, repo, page)
	}
}

// readPlatformCandidate reads, as the person, agent-platform's highest
// release candidate of major that is newer than the line's highest stable
// release: the version an installation that follows release candidates can
// run and a stable-only range cannot select, so that dropping the candidates
// moves the installation down to Stable. Both are highest by semver over every
// release read, as Flux and devctl release promote select them, not first in
// GitHub's order: a hotfix of an older line cut after a candidate is listed
// before it. Zero where no candidate is ahead.
func readPlatformCandidate(ctx context.Context, list releasesPage, major uint64) (PlatformCandidate, error) {
	var candidate, stable *semver.Version
	var candidateTag, stableTag string
	for page := 1; page <= maxReleasePages; page++ {
		releases, more, err := list(ctx, PlatformRepository, page)
		if err != nil {
			return PlatformCandidate{}, fmt.Errorf("agent-platform's release candidates: %w", err)
		}
		for _, r := range releases {
			v, err := semver.NewVersion(r.Tag)
			if r.Draft || err != nil || v.Major() != major {
				continue
			}
			switch {
			case platformStableTag.MatchString(r.Tag) && !r.Prerelease:
				if stable == nil || v.GreaterThan(stable) {
					stable, stableTag = v, r.Tag
				}
			case platformCandidateTag.MatchString(r.Tag) && r.Prerelease:
				if candidate == nil || v.GreaterThan(candidate) {
					candidate, candidateTag = v, r.Tag
				}
			}
		}
		if !more {
			break
		}
	}
	if candidate == nil || stable != nil && !candidate.GreaterThan(stable) {
		return PlatformCandidate{}, nil
	}
	return PlatformCandidate{Candidate: candidateTag, Stable: stableTag}, nil
}

// PlatformExtrasKustomizationPath is the kustomization of the installation's
// extras/agent-platform tree: the fleet base, with the patch on the
// agent-platform OCIRepository the 4 chart line pins its range with.
func PlatformExtrasKustomizationPath(name string) string {
	return "management-clusters/" + name + "/extras/agent-platform/kustomization.yaml"
}

// readPrereleasesOnRecord says whether the installation follows the
// platform's release candidates now: its extras kustomization on record
// patches a semverFilter onto the agent-platform OCIRepository, which the
// definition renders together with gitops.prereleases beside a range or a
// held release. A hold on a development build carries no filter and reads
// false: pinned exactly, nothing a dropped gitops.prereleases could move.
// No kustomization is false, no error.
func readPrereleasesOnRecord(ctx context.Context, read Reader, inst Installation) (bool, error) {
	path := PlatformExtrasKustomizationPath(inst.Name)
	data, err := read(ctx, inst.Repositories.ManagementClusters, path)
	if errors.Is(err, gh.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("the release candidates on record: %w", err)
	}
	var k collectionsKustomization
	if err := yaml.Unmarshal([]byte(data), &k); err != nil {
		return false, fmt.Errorf("the release candidates on record: %s in %s: decode: %w", path, inst.Repositories.ManagementClusters, err)
	}
	for _, p := range k.Patches {
		if p.Target.Kind != "OCIRepository" || p.Target.Name != "agent-platform" {
			continue
		}
		on, err := setsSemverFilter(p.Patch)
		if err != nil {
			return false, fmt.Errorf("the release candidates on record: %s in %s: decode the patch on the OCIRepository agent-platform: %w", path, inst.Repositories.ManagementClusters, err)
		}
		if on {
			return true, nil
		}
	}
	return false, nil
}

// setsSemverFilter says whether a patch on the OCIRepository sets
// spec.ref.semverFilter: a JSON 6902 op on /spec/ref/semverFilter, or a
// strategic merge patch that carries it.
func setsSemverFilter(patch string) (bool, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(patch), &node); err != nil {
		return false, err
	}
	if len(node.Content) == 0 {
		return false, nil
	}
	if node.Content[0].Kind == yaml.SequenceNode {
		var ops []struct {
			Path string `yaml:"path"`
		}
		if err := node.Decode(&ops); err != nil {
			return false, err
		}
		for _, op := range ops {
			if op.Path == "/spec/ref/semverFilter" {
				return true, nil
			}
		}
		return false, nil
	}
	var merge struct {
		Spec struct {
			Ref struct {
				SemverFilter string `yaml:"semverFilter"`
			} `yaml:"ref"`
		} `yaml:"spec"`
	}
	if err := node.Decode(&merge); err != nil {
		return false, err
	}
	return merge.Spec.Ref.SemverFilter != "", nil
}

// PrereleasesIn says whether an agent-platform values patch sets
// gitops.prereleases.
func PrereleasesIn(patch string) (bool, error) {
	var doc struct {
		GitOps struct {
			Prereleases bool `yaml:"prereleases"`
		} `yaml:"gitops"`
	}
	if err := yaml.Unmarshal([]byte(patch), &doc); err != nil {
		return false, fmt.Errorf("decode: %w", err)
	}
	return doc.GitOps.Prereleases, nil
}
