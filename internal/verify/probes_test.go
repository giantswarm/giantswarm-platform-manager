package verify

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// roundTrip answers every request of a probe's client from a function.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// answering is a client whose transport answers the status listed for the
// request's client_id or host, and fails to connect for any other request.
func answering(status map[string]int) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		for _, k := range []string{r.URL.Query().Get("client_id"), r.URL.Host} {
			if s, ok := status[k]; ok {
				return &http.Response{StatusCode: s, Body: http.NoBody, Request: r}, nil
			}
		}
		return nil, errors.New("dial tcp: i/o timeout")
	})}
}

var (
	gate = definitions.Probe{ID: "oauth2-proxy-gate", Feature: "identity", Key: "the gate refuses an anonymous request", URL: "https://kagent.{{.BaseDomain}}/", Expect: []int{302, 403}}
	dex  = definitions.Probe{ID: "dex-auth-request", Feature: "identity", Key: "Dex accepts every client's auth request", URL: "https://dex.{{.BaseDomain}}/auth?client_id={{.ClientID}}&redirect_uri={{.RedirectURI}}", PerDexClient: true, Expect: []int{302}}
	base = ProbeData{BaseDomain: "example.test", Installation: "x", PortalDomain: "portal.example.test"}
)

// The customer portal's probes render their URLs from the portal's domain
// and the installation's codename: each one, answered what it expects at
// that host, reads as defined.
func TestProbePortalURLsRenderFromThePortalDomain(t *testing.T) {
	probes, err := definitions.Probes(installations.CustomerPortal)
	if err != nil {
		t.Fatal(err)
	}
	var overPortal int
	for _, p := range probes {
		if !strings.Contains(p.URL, ".PortalDomain") {
			continue
		}
		overPortal++
		d := probe(context.Background(), answering(map[string]int{base.PortalDomain: p.Expect[0]}), base, nil, false, p)
		if d.Mark != AsDefined || len(d.Probe.Requests) != 1 || !strings.HasPrefix(d.Probe.Requests[0].URL, "https://"+base.PortalDomain+"/") {
			t.Errorf("%s: mark %q, reason %q, requests %+v", p.ID, d.Mark, d.Reason, d.Probe.Requests)
		}
		if strings.Contains(p.URL, ".Installation") && !strings.Contains(d.Probe.Requests[0].URL, "oidc-"+base.Installation+"/") {
			t.Errorf("%s: the codename is not in %q", p.ID, d.Probe.Requests[0].URL)
		}
	}
	if overPortal < 2 {
		t.Fatalf("%d probe(s) over the portal's domain", overPortal)
	}
}

// Without a portal.domain on record, a probe over it is not checked for the
// choice, as a file dimension whose leaf carries it is, and sends nothing:
// never a template error. The probes over the installation's domain still run.
func TestCompareWithoutThePortalDomainIsNotChecked(t *testing.T) {
	keys := strings.Split(fieldPortalDomain, ".")
	if got := choice(map[string]any{keys[0]: map[string]any{keys[1]: base.PortalDomain}}, fieldPortalDomain); got != base.PortalDomain {
		t.Fatalf("choice on record: %q", got)
	}
	r := Compare(context.Background(), Options{
		Definition:   installations.Capability{Name: installations.CustomerPortal},
		Installation: installations.Installation{Name: "x", BaseDomain: "x.example.test"},
		State:        installations.StateNotEnabled,
		Probes:       answering(nil),
	})
	if r.Refused != "" {
		t.Fatal(r.Refused)
	}
	want := missingChoice([]string{fieldPortalDomain})
	var forTheChoice, others int
	for _, f := range r.Features {
		for _, d := range f.Dimensions {
			if d.Kind != definitions.KindProbe {
				continue
			}
			switch {
			case d.Reason == want:
				forTheChoice++
				if d.Mark != NotChecked || len(d.Probe.Requests) != 0 {
					t.Errorf("%s: mark %q, requests %+v", d.ID, d.Mark, d.Probe.Requests)
				}
			case strings.Contains(d.Reason, "can't evaluate field"):
				t.Errorf("%s: a template error: %q", d.ID, d.Reason)
			default:
				others++
			}
		}
	}
	if forTheChoice < 2 || others == 0 {
		t.Fatalf("%d probe(s) not checked for the choice, %d other(s)", forTheChoice, others)
	}
}

// A target the manager gets no answer from is no comparison: the dimension
// reads not checked, unreachable from the manager, with the transport's error.
func TestProbeUnreachableIsNotChecked(t *testing.T) {
	d := probe(context.Background(), answering(nil), base, nil, false, gate)
	if d.Mark != NotChecked || !strings.HasPrefix(d.Reason, ReasonUnreachable+": ") || !strings.Contains(d.Reason, "i/o timeout") {
		t.Fatalf("mark %q, reason %q", d.Mark, d.Reason)
	}
	if len(d.Probe.Requests) != 1 || d.Probe.Requests[0].Error == "" || d.Probe.Requests[0].Status != 0 || d.Probe.Requests[0].OK {
		t.Fatalf("requests %+v", d.Probe.Requests)
	}
}

// An answer outside the expectation is drift, with the status on the request and no reason.
func TestProbeUnexpectedStatusIsDrifted(t *testing.T) {
	d := probe(context.Background(), answering(map[string]int{"kagent.example.test": http.StatusInternalServerError}), base, nil, false, gate)
	if d.Mark != Drifted || d.Reason != "" || len(d.Probe.Requests) != 1 || d.Probe.Requests[0].Status != http.StatusInternalServerError || d.Probe.Requests[0].OK {
		t.Fatalf("mark %q, reason %q, requests %+v", d.Mark, d.Reason, d.Probe.Requests)
	}
}

// Per Dex client, one dimension holds several requests: an unexpected status
// is drift whichever the order and whatever the other requests did, an
// unreachable target wins over as defined.
func TestProbePerClientPrecedence(t *testing.T) {
	clients := []plan.DexClient{{ID: "a", RedirectURIs: []string{"https://a.example.test/cb"}}, {ID: "b", RedirectURIs: []string{"https://b.example.test/cb"}}}
	for _, tc := range []struct {
		name   string
		status map[string]int
		want   Mark
	}{
		{"both as expected", map[string]int{"a": 302, "b": 302}, AsDefined},
		{"unexpected then unreachable", map[string]int{"a": 500}, Drifted},
		{"unreachable then unexpected", map[string]int{"b": 500}, Drifted},
		{"unexpected beside expected", map[string]int{"a": 500, "b": 302}, Drifted},
		{"expected then unreachable", map[string]int{"a": 302}, NotChecked},
		{"unreachable then expected", map[string]int{"b": 302}, NotChecked},
	} {
		d := probe(context.Background(), answering(tc.status), base, clients, true, dex)
		if d.Mark != tc.want || len(d.Probe.Requests) != 2 {
			t.Errorf("%s: mark %q, requests %+v", tc.name, d.Mark, d.Probe.Requests)
			continue
		}
		switch tc.want {
		case NotChecked:
			if !strings.HasPrefix(d.Reason, ReasonUnreachable+": ") {
				t.Errorf("%s: reason %q", tc.name, d.Reason)
			}
		default:
			if d.Reason != "" {
				t.Errorf("%s: reason %q", tc.name, d.Reason)
			}
		}
	}
}

// A verify whose probe targets are all unreachable does not turn the
// installation drifted: the probe dimensions read not checked and the state
// stays the one read from the repositories.
func TestCompareUnreachableProbesKeepTheState(t *testing.T) {
	r := Compare(context.Background(), Options{
		Definition:   installations.Capability{Name: "agent-platform"},
		Installation: installations.Installation{Name: "x", BaseDomain: "x.example.test"},
		State:        installations.StateEnabled,
		Probes:       answering(nil),
	})
	if r.Refused != "" || r.State != installations.StateEnabled || r.Summary[Drifted] != 0 {
		t.Fatalf("refused %q, state %q, summary %v", r.Refused, r.State, r.Summary)
	}
	var sent int
	for _, f := range r.Features {
		for _, d := range f.Dimensions {
			if d.Kind != definitions.KindProbe || len(d.Probe.Requests) == 0 {
				continue
			}
			sent++
			if d.Mark != NotChecked || !strings.HasPrefix(d.Reason, ReasonUnreachable+": ") {
				t.Errorf("%s: mark %q, reason %q", d.ID, d.Mark, d.Reason)
			}
		}
	}
	if sent == 0 {
		t.Fatal("no probe sent a request")
	}
}

// The live path's HTTP probe follows the same rule: no answer is not checked, another answer is drift.
func TestLiveHTTPProbeUnreachableIsNotChecked(t *testing.T) {
	p := render.Probe{ID: "gate", Kind: render.HTTP, URL: "https://kagent.example.test/", Expect: render.Expectation{Status: http.StatusFound}}
	x := &executor{opts: LiveOptions{Probes: answering(nil)}}
	c := Check{Mark: NotChecked}
	x.httpProbe(&c, p)
	if c.Mark != NotChecked || !strings.HasPrefix(c.Message, ReasonUnreachable+": ") {
		t.Fatalf("mark %q, message %q", c.Mark, c.Message)
	}
	x = &executor{opts: LiveOptions{Probes: answering(map[string]int{"kagent.example.test": http.StatusInternalServerError})}}
	c = Check{Mark: NotChecked}
	x.httpProbe(&c, p)
	if c.Mark != Drifted || !strings.HasPrefix(c.Message, "500, expected 302") {
		t.Fatalf("mark %q, message %q", c.Mark, c.Message)
	}
	if m, reason := checkRollUp([]Check{{Mark: AsDefined}, {Mark: NotChecked, Message: ReasonUnreachable + ": x"}}); m != NotChecked || reason != ReasonUnreachable+": x" {
		t.Fatalf("roll-up %q %q", m, reason)
	}
	if m, _ := checkRollUp([]Check{{Mark: NotChecked, Message: ReasonUnreachable + ": x"}, {Mark: Drifted}}); m != Drifted {
		t.Fatalf("roll-up %q", m)
	}
}
