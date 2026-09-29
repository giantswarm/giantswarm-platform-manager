package plan

import (
	"fmt"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// ReleaseCandidateRefusal says why a commit of p is refused for the release
// candidate rec's installation runs: p drops gitops.prereleases
// (DropsPrereleases) while agent-platform's release candidate
// rec.PlatformCandidate is ahead of its latest stable release. The
// stable-only range would then select rec.PlatformStable, and helm-controller
// would downgrade the platform. Empty where nothing refuses: a plan that
// keeps the candidates, or no candidate ahead. The comparison runs either
// way; only the commit is held until the candidate is promoted.
func (p Installation) ReleaseCandidateRefusal(rec *installations.Record) string {
	if !p.DropsPrereleases || rec == nil || rec.PlatformCandidate == "" {
		return ""
	}
	to := "the latest stable release " + rec.PlatformStable
	if rec.PlatformStable == "" {
		to = "no stable release at all"
	}
	return fmt.Sprintf("%s runs agent-platform's release candidates, and %s is ahead of the latest stable release: dropping gitops.prereleases now would move it down to %s. Promote %s first (devctl release promote %s), then commit",
		p.Name, rec.PlatformCandidate, to, rec.PlatformCandidate, installations.PlatformRepository)
}

// dropsPrereleases says the rendered agent-platform values patch drops
// gitops.prereleases that the patch on record sets; a patch that does not
// decode drops nothing.
func dropsPrereleases(current, rendered string) bool {
	was, err := installations.PrereleasesIn(current)
	if err != nil || !was {
		return false
	}
	now, err := installations.PrereleasesIn(rendered)
	return err == nil && !now
}
