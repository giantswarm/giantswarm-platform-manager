package format

import (
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// join prints a plan's warning over what its agent-platform values patch
// takes off the installation's muster beyond the shared defaults — the
// broker's exchange targets, the identity providers, the MCP servers, the
// public-registration redirect URIs that move to or from another
// installation of the set —, apart from the file rows and the comparison's
// counts, one line per kind naming each entry. Nothing for a plan without one.
func (p *printer) join(name string, j *plan.Join) {
	if j == nil {
		return
	}
	forced := ""
	if j.Forced {
		forced = " (forced: the commit proceeds)"
	}
	p.f("  Warning: this reconcile changes what %s's muster serves beyond the shared defaults%s:\n", name, forced)
	if len(j.ExchangeTargets) > 0 {
		p.f("    exchange targets removed: %s\n", strings.Join(j.ExchangeTargets, ", "))
	}
	if len(j.IdentityProviders) > 0 {
		p.f("    identity providers removed: %s\n", strings.Join(j.IdentityProviders, ", "))
	}
	if len(j.Servers) > 0 {
		servers := make([]string, len(j.Servers))
		for i, s := range j.Servers {
			servers[i] = s.String()
		}
		p.f("    MCP servers removed: %s\n", strings.Join(servers, "; "))
	}
	var removed, added []string
	for _, u := range j.RedirectURIs {
		if u.Added {
			added = append(added, u.String())
		} else {
			removed = append(removed, u.String())
		}
	}
	if len(removed) > 0 {
		p.f("    public-registration redirect URIs removed: %s\n", strings.Join(removed, ", "))
	}
	if len(added) > 0 {
		p.f("    public-registration redirect URIs added: %s\n", strings.Join(added, ", "))
	}
}
