package agentplatform

import "testing"

// The names of the portal cases: the fleet's hub and its organisation, a
// Giant Swarm-owned installation with a portal of its own, a customer's
// aggregator that hosts the organisation's portal and a sibling of its.
const (
	portalCaseHub        = "aspen"
	portalCaseOrg        = "giantswarm"
	portalCaseOwn        = "gopher"
	portalCaseCustomer   = "oakridge"
	portalCaseAggregator = "kestrel"
	portalCaseSibling    = "plover"
)

// The platform's portal section lands in the portal hosted on the installation
// itself, else in the organisation's portal on a sibling that is not hand-kept,
// else nowhere: a hand-kept sibling portal (the hub's Dev Portal) and another
// organisation's portal take no Component. The Component sets the portal's
// lists only where the portal is not hand-kept.
func TestHostedPortal(t *testing.T) {
	hub := PortalRef{Installation: portalCaseHub, Customer: portalCaseOrg, Domain: "portal." + portalCaseHub + ".example.io", HandKept: true}
	own := PortalRef{Installation: portalCaseOwn, Customer: portalCaseOrg, Domain: "portal." + portalCaseOwn + ".example.io", HandKept: true}
	aggregator := PortalRef{Installation: portalCaseAggregator, Customer: portalCaseCustomer, Domain: "portal." + portalCaseAggregator + ".oakridge.example"}
	cases := []struct {
		name      string
		in        Installation
		host      string
		ownsLists bool
	}{
		{"own portal first, whatever the record's order", Installation{Name: portalCaseOwn, Customer: portalCaseOrg, Portals: []PortalRef{hub, own}}, portalCaseOwn, false},
		{"own portal, rendered", Installation{Name: portalCaseAggregator, Customer: portalCaseCustomer, Portals: []PortalRef{hub, aggregator}}, portalCaseAggregator, true},
		{"the organisation's portal on a sibling", Installation{Name: portalCaseSibling, Customer: portalCaseCustomer, Portals: []PortalRef{hub, aggregator}}, portalCaseAggregator, true},
		{"the hub's hand-kept portal alone", Installation{Name: "marmot", Customer: portalCaseOrg, Portals: []PortalRef{hub}}, "", false},
		{"another organisation's portal alone", Installation{Name: portalCaseSibling, Customer: portalCaseCustomer, Portals: []PortalRef{hub}}, "", false},
		{"no portal", Installation{Name: portalCaseSibling, Customer: portalCaseCustomer}, "", false},
	}
	for _, c := range cases {
		in := &Input{Installation: c.in}
		if got := in.portalHost(); got != c.host {
			t.Errorf("%s: host %q, want %q", c.name, got, c.host)
		}
		if got := in.portalOwnsLists(); got != c.ownsLists {
			t.Errorf("%s: owns lists %v, want %v", c.name, got, c.ownsLists)
		}
	}
}
