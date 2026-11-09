package format

import (
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// switchOff prints a plan's warning over the components running on the
// installation that it switches off, first, apart from the file rows and the
// drift lines: one line per component, naming what depends on it. Nothing
// for a plan that switches nothing off.
func (p *printer) switchOff(name string, s *plan.SwitchOff) {
	if s == nil || len(s.Components) == 0 {
		return
	}
	forced := ""
	if s.Forced {
		forced = " (forced: the commit proceeds)"
	}
	p.f("  Warning: this reconcile switches off components running on %s%s:\n", name, forced)
	for _, c := range s.Components {
		if c.DependedOnBy == "" {
			p.f("    %s\n", c.Component)
			continue
		}
		p.f("    %s: %s depend on it\n", c.Component, c.DependedOnBy)
	}
}
