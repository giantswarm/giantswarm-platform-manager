package agentplatform

import (
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

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

// The organisation and the portal host of the inputs built by hand below.
const (
	testOrganisation = "acme"
	testPortalHost   = "maple"
)

// A portal's chart line admits versions from its floor: the lower bound of
// the bounded range the customer-portal definition writes, or the tag itself.
// The fragment names the agents' Flux identity where that floor lies before
// the plugin's removal of the key, and a line of another form is refused.
func TestPortalChartFloor(t *testing.T) {
	for _, tc := range []struct {
		line  string
		floor string
		reads bool
	}{
		{">=0.244.7 <1.0.0", "0.244.7", true},
		{">=1.0.0 <2.0.0", "1.0.0", true},
		{">=1.1.0 <2.0.0", "1.1.0", false},
		{">=2.1.0 <3.0.0", "2.1.0", false},
		{">=0.244.7 <3.0.0", "0.244.7", true},
		{"0.120.0", "0.120.0", true},
		{"2.53.2", "2.53.2", false},
	} {
		floor, err := portalChartFloor(tc.line)
		if err != nil || floor.String() != tc.floor {
			t.Errorf("%q: floor %v, %v; want %s", tc.line, floor, err, tc.floor)
			continue
		}
		in := &Input{Installation: Installation{Customer: testOrganisation, Portals: []PortalRef{{Installation: testPortalHost, Customer: testOrganisation, ChartLine: tc.line}}}}
		if got := in.portalReadsFluxServiceAccount(); got != tc.reads {
			t.Errorf("%q: reads the Flux identity %v, want %v", tc.line, got, tc.reads)
		}
	}
	for _, line := range []string{"", "x.x.x", "^0.244.7", ">=0.244.7", "<1.0.0", ">=0.244.7 <1.0.0 || >=2.0.0", "latest", ">=a.b.c <1.0.0"} {
		if _, err := portalChartFloor(line); err == nil {
			t.Errorf("%q: a line of another form is an error", line)
		}
	}
	if (&Input{Installation: Installation{Customer: testOrganisation}}).portalReadsFluxServiceAccount() {
		t.Error("an installation without a hosted portal reads nothing")
	}
}

// Where kagent runs on an installation whose hosted portal is on record
// without its chart line, or with one of another form, the plan is
// refused naming the portal and the file; a portal of another organisation,
// or an installation without kagent, is not held to it.
func TestCheckRecordRefusesAPortalWithoutItsChartLine(t *testing.T) {
	input, secrets := loadInput(t, shapePublicCustomer)
	portals := input["installation"].(map[string]any)["portals"].([]any)
	own := portals[0].(map[string]any)
	delete(own, "chartLine")
	_, err := Render(input, secrets, render.ModeCommit)
	if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "installation.portals[kestrel].chartLine") || !strings.Contains(err.Error(), "management-clusters/kestrel/extras/backstage/backstage/kustomization.yaml") {
		t.Fatalf("a portal without its chart line: %v", err)
	}
	own["chartLine"] = "x.x.x"
	if _, err := Render(input, secrets, render.ModeCommit); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), `"x.x.x"`) {
		t.Fatalf("a portal with a chart line of another form: %v", err)
	}
	own["chartLine"] = ">=0.244.7 <1.0.0"
	delete(portals[1].(map[string]any), "chartLine")
	if _, err := Render(input, secrets, render.ModeCommit); err != nil {
		t.Fatalf("the hub's portal without its chart line is not this organisation's: %v", err)
	}
	in := &Input{Installation: Installation{Customer: testOrganisation, ChartLine: lineThree, Portals: []PortalRef{{Installation: testPortalHost, Customer: testOrganisation}}}, Components: map[string]bool{}}
	if err := in.checkRecord(); err != nil {
		t.Fatalf("without kagent the fragment carries no agentPlatform section, so the line is not needed: %v", err)
	}
	in.Components[componentKagent] = true
	if err := in.checkRecord(); !errors.Is(err, ErrInput) {
		t.Fatalf("with kagent the line is needed: %v", err)
	}
}

// TestGoldensCoverPortalLines holds the golden shapes to both sides of the
// removal: the fragment of a shape whose hosted portal follows a line
// before backstage 1.1.0 names the agents' Flux identity, one whose portal
// follows a later line does not, and at least one shape renders each.
func TestGoldensCoverPortalLines(t *testing.T) {
	rendered := map[bool]bool{}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		host := in.portalHost()
		if host == "" || !in.kagent() {
			continue
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		fragment := string(result.Tree()["giantswarm/"+in.Installation.Customer+"-management-clusters/management-clusters/"+host+"/extras/backstage/"+portalDir+"/app-config.yaml"])
		named := strings.Contains(fragment, "fluxServiceAccountName: "+fluxServiceAccount+"\n")
		if want := in.portalReadsFluxServiceAccount(); named != want {
			t.Errorf("%s (portal %s on %s): the fragment names the Flux identity: %v, want %v\n%s", shape, host, in.hostedPortal().ChartLine, named, want, fragment)
		}
		rendered[named] = true
	}
	for _, named := range []bool{true, false} {
		if !rendered[named] {
			t.Errorf("no golden shape renders a portal whose fragment names the Flux identity: %v", named)
		}
	}
}
