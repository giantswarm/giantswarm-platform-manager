package verify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

// gateHost is the host the gate probe renders over base.
const gateHost = "kagent.example.test"

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
		d := newProber(answering(map[string]int{base.PortalDomain: p.Expect[0]})).probe(context.Background(), base, nil, false, p)
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
// reads not checked, unreachable from the manager naming the host, with the
// transport's error as the detail.
func TestProbeUnreachableIsNotChecked(t *testing.T) {
	d := newProber(answering(nil)).probe(context.Background(), base, nil, false, gate)
	if d.Mark != NotChecked || d.Reason != ReasonUnreachable+": "+gateHost || !strings.Contains(d.Detail, "i/o timeout") {
		t.Fatalf("mark %q, reason %q, detail %q", d.Mark, d.Reason, d.Detail)
	}
	if len(d.Probe.Requests) != 1 || d.Probe.Requests[0].Error == "" || d.Probe.Requests[0].Status != 0 || d.Probe.Requests[0].OK {
		t.Fatalf("requests %+v", d.Probe.Requests)
	}
}

// An answer outside the expectation is drift, with the status on the request and no reason.
func TestProbeUnexpectedStatusIsDrifted(t *testing.T) {
	d := newProber(answering(map[string]int{gateHost: http.StatusInternalServerError})).probe(context.Background(), base, nil, false, gate)
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
		d := newProber(answering(tc.status)).probe(context.Background(), base, clients, true, dex)
		if d.Mark != tc.want || len(d.Probe.Requests) != 2 || d.Probe.Requests[0].Client != "a" || d.Probe.Requests[1].Client != "b" {
			t.Errorf("%s: mark %q, requests %+v", tc.name, d.Mark, d.Probe.Requests)
			continue
		}
		switch tc.want {
		case NotChecked:
			if d.Reason != ReasonUnreachable+": dex.example.test" || d.Detail == "" {
				t.Errorf("%s: reason %q, detail %q", tc.name, d.Reason, d.Detail)
			}
		default:
			if d.Reason != "" || d.Detail != "" {
				t.Errorf("%s: reason %q, detail %q", tc.name, d.Reason, d.Detail)
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
			if d.Mark != NotChecked || !strings.HasPrefix(d.Reason, ReasonUnreachable+": ") || strings.Contains(d.Reason, "/") || d.Detail == "" {
				t.Errorf("%s: mark %q, reason %q, detail %q", d.ID, d.Mark, d.Reason, d.Detail)
			}
		}
	}
	if sent == 0 {
		t.Fatal("no probe sent a request")
	}
}

// liveHTTP runs the one HTTP probe of a live verify the way CompareLive does:
// sent by sendHTTP, read back into its dimension's check.
func liveHTTP(client *http.Client, p render.Probe) Check {
	x := &executor{lv: &liveRender{probes: []render.Probe{p}}, pr: newProber(client)}
	x.sendHTTP(context.Background())
	return x.dimension(context.Background(), definitions.Dimension{ID: p.ID, Kind: definitions.KindLive}).Live.Checks[0]
}

// The live path's HTTP probe follows the same rule: no answer is not checked
// naming the host, the error in the detail; another answer is drift.
func TestLiveHTTPProbeUnreachableIsNotChecked(t *testing.T) {
	p := render.Probe{ID: "gate", Kind: render.HTTP, URL: "https://kagent.example.test/", Expect: render.Expectation{Status: http.StatusFound}}
	c := liveHTTP(answering(nil), p)
	if c.Mark != NotChecked || c.Message != ReasonUnreachable+": "+gateHost || !strings.Contains(c.Detail, "i/o timeout") {
		t.Fatalf("mark %q, message %q, detail %q", c.Mark, c.Message, c.Detail)
	}
	c = liveHTTP(answering(map[string]int{gateHost: http.StatusInternalServerError}), p)
	if c.Mark != Drifted || !strings.HasPrefix(c.Message, "500, expected 302") || c.Detail != "" {
		t.Fatalf("mark %q, message %q, detail %q", c.Mark, c.Message, c.Detail)
	}
	if m, reason, detail := checkRollUp([]Check{{Mark: AsDefined}, {Mark: NotChecked, Message: ReasonUnreachable + ": x", Detail: "dial"}}); m != NotChecked || reason != ReasonUnreachable+": x" || detail != "dial" {
		t.Fatalf("roll-up %q %q %q", m, reason, detail)
	}
	if m, _, _ := checkRollUp([]Check{{Mark: NotChecked, Message: ReasonUnreachable + ": x"}, {Mark: Drifted}}); m != Drifted {
		t.Fatalf("roll-up %q", m)
	}
}

// hanging is a client whose every request lands at a server that never
// answers until the test ends: a host the manager cannot reach. The request's
// own URL stays what the probe rendered.
func hanging(t *testing.T) *http.Client {
	t.Helper()
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-unblock }))
	t.Cleanup(func() { close(unblock); srv.Close() })
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return srv.Client().Transport.RoundTrip(r)
	})}
}

// phase is a context that ends the probes' phase after d, in place of ProbePhaseTimeout.
func phase(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

// The probes of one comparison run at once under one deadline for the phase:
// five requests to hosts that never answer end together when the phase does,
// not one after the other. Each dimension reads not checked, unreachable from
// the manager naming its host, the deadline's error as the detail, and the
// dimensions and requests keep the definition's and the clients' order.
func TestProbesRunAtOnceUnderThePhaseDeadline(t *testing.T) {
	muster := definitions.Probe{ID: "muster-protected-resource-metadata", Feature: "tool-access", Key: "muster publishes its metadata", URL: "https://muster.{{.BaseDomain}}/.well-known/oauth-protected-resource", Expect: []int{200}}
	clients := []plan.DexClient{{ID: "a", RedirectURIs: []string{"https://a.example.test/cb"}}, {ID: "b", RedirectURIs: []string{"https://b.example.test/cb"}}, {ID: "c", RedirectURIs: []string{"https://c.example.test/cb"}}}
	const deadline = 500 * time.Millisecond
	start := time.Now()
	dims := newProber(hanging(t)).probeAll(phase(t, deadline), base, clients, true, []definitions.Probe{gate, dex, muster})
	if elapsed := time.Since(start); elapsed >= 2*deadline {
		t.Fatalf("five hanging requests took %s: not at once", elapsed)
	}
	if len(dims) != 3 || dims[0].ID != gate.ID || dims[1].ID != dex.ID || dims[2].ID != muster.ID {
		t.Fatalf("dimensions %+v", dims)
	}
	for i, host := range []string{gateHost, "dex.example.test", "muster.example.test"} {
		d := dims[i]
		if d.Mark != NotChecked || d.Reason != ReasonUnreachable+": "+host || !strings.Contains(d.Detail, context.DeadlineExceeded.Error()) {
			t.Errorf("%s: mark %q, reason %q, detail %q", d.ID, d.Mark, d.Reason, d.Detail)
		}
		for _, r := range d.Probe.Requests {
			if r.Error == "" || r.OK || r.Status != 0 {
				t.Errorf("%s: request %+v", d.ID, r)
			}
		}
	}
	if reqs := dims[1].Probe.Requests; len(reqs) != 3 || reqs[0].Client != "a" || reqs[1].Client != "b" || reqs[2].Client != "c" {
		t.Errorf("per-client requests %+v", reqs)
	}
}

// The live verify's HTTP probes are sent at once too, before any dimension is
// built: four probes in four dimensions to hosts that never answer end with
// the phase, and each dimension reads its host in the reason.
func TestLiveHTTPProbesAreSentAtOnce(t *testing.T) {
	var probes []render.Probe
	for _, id := range []string{"live-kagent-api-protected", "live-kagent-login-redirect", "live-dex-auth-per-client", "live-muster-protected-resource"} {
		probes = append(probes, render.Probe{ID: id, Kind: render.HTTP, URL: "https://" + id + ".example.test/", Expect: render.Expectation{Status: http.StatusOK}})
	}
	x := &executor{lv: &liveRender{probes: probes}, pr: newProber(hanging(t))}
	const deadline = 500 * time.Millisecond
	start := time.Now()
	x.sendHTTP(phase(t, deadline))
	if elapsed := time.Since(start); elapsed >= 2*deadline {
		t.Fatalf("four hanging requests took %s: not at once", elapsed)
	}
	for _, p := range probes {
		d := x.dimension(context.Background(), definitions.Dimension{ID: p.ID, Kind: definitions.KindLive})
		if d.Mark != NotChecked || d.Reason != ReasonUnreachable+": "+p.ID+".example.test" || !strings.Contains(d.Detail, context.DeadlineExceeded.Error()) || len(d.Live.Checks) != 1 || d.Live.Checks[0].URL != p.URL {
			t.Errorf("%s: mark %q, reason %q, detail %q, checks %+v", p.ID, d.Mark, d.Reason, d.Detail, d.Live.Checks)
		}
	}
}

// The default client gives a request up within the phase: no request of a
// comparison outlives it, whatever the caller's context.
func TestDefaultProbeClientIsBoundedWithinThePhase(t *testing.T) {
	pr := newProber(nil)
	if pr.client.Timeout != ProbeRequestTimeout || ProbeRequestTimeout > ProbePhaseTimeout || cap(pr.slots) != ProbeConcurrency {
		t.Fatalf("timeout %s of %s, %d slots", pr.client.Timeout, ProbePhaseTimeout, cap(pr.slots))
	}
	if err := pr.client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirects followed: %v", err)
	}
}
