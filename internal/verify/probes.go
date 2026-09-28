package verify

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// ProbeResult is what an anonymous probe answered, request by request.
type ProbeResult struct {
	Expect   []int     `json:"expect"`
	Requests []Request `json:"requests"`
}

// Request is one anonymous HTTP request of a probe and its answer. For a
// probe that takes the Dex connector step, Connector is the connector Dex's
// /auth answer named and Status the connector's answer; Message says what a
// drifted answer means where the status alone does not (the client or the
// redirect URI Dex does not know, an answer that names no connector).
type Request struct {
	URL       string `json:"url"`
	Client    string `json:"client,omitempty"`
	Status    int    `json:"status,omitempty"`
	Connector string `json:"connector,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
	OK        bool   `json:"ok"`
}

// Answer is the request's answer in one line: the status, the connector
// whose answer it is and what a drifted answer means; the transport's error
// when there was none.
func (r Request) Answer() string {
	if r.Error != "" {
		return r.Error
	}
	s := reply{status: r.Status, connector: r.Connector}.String()
	if r.Message != "" {
		s += ": " + r.Message
	}
	return s
}

// The reasons a probe is not checked: ReasonNoDexClients, a per-client probe
// with no client to run for; ReasonUnreachable, a request the manager could
// not get an answer to (timeout, DNS, connection refused), the target's host
// appended and the transport's error in the detail.
const (
	ReasonNoDexClients = "the render declares no Dex client with a redirect URI for the inputs on record"
	ReasonUnreachable  = "unreachable from the manager"
)

// The bounds of one comparison's HTTP probes. A private installation's Dex,
// kagent and muster are unreachable from the hub by design, so waiting for
// each in turn bought nothing: every request of a comparison is in flight at
// once, ProbeConcurrency at most, and the phase ends at its deadline whatever
// is still hanging. A request is bounded on its own too, no longer than the
// phase, so a client of the caller's without a timeout cannot outlive it.
// A host that answers does so well within a second (four probes of an
// installation answer together in 0.2–0.3 s); one the manager cannot reach
// never answers, and is not checked whenever the phase ends. The request
// bound is what an unreachable host costs a comparison.
const (
	ProbeConcurrency    = 8
	ProbePhaseTimeout   = 6 * time.Second
	ProbeRequestTimeout = 5 * time.Second
)

// ProbeData is what a probe's URL template is executed over: the
// installation's base domain and codename; the portal's domain, the
// customer-portal inputs' portal.domain (its Missing marker while the choice
// is not on record, so a probe that names it is not checked for the choice,
// never a template error; unnamed by the agent-platform's probes); and, for
// a perDexClient probe, the id and first redirect URI of the client the
// request is for, query-escaped. A template over a field no verify fills is
// the definitions' test's to catch.
type ProbeData struct {
	BaseDomain, Installation, PortalDomain, ClientID, RedirectURI string
}

// fieldPortalDomain is the field of the customer-portal inputs that fills
// ProbeData's PortalDomain.
const fieldPortalDomain = "portal.domain"

// probeData is what every probe of one verify is executed over: the
// installation's codename and base domain, and the portal's domain of the
// inputs on record.
func probeData(installation, baseDomain string, values map[string]any) ProbeData {
	return ProbeData{BaseDomain: baseDomain, Installation: installation, PortalDomain: choice(values, fieldPortalDomain)}
}

// choice is the string at the dotted field of the inputs on record, or the
// field's Missing marker when no layer holds one.
func choice(values map[string]any, field string) string {
	if s := inputString(values, strings.Split(field, ".")...); s != "" {
		return s
	}
	return render.Missing(field)
}

// prober sends the HTTP probes of one comparison, ProbeConcurrency requests
// in flight at most. Each phase of a comparison — the definition's anonymous
// probes, the render's live HTTP probes — runs under one deadline on its
// context, ProbePhaseTimeout long.
type prober struct {
	client *http.Client
	slots  chan struct{}
}

// newProber probes with client; nil is a client that does not follow
// redirects and gives up on a request after ProbeRequestTimeout.
func newProber(client *http.Client) *prober {
	if client == nil {
		client = &http.Client{Timeout: ProbeRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &prober{client: client, slots: make(chan struct{}, ProbeConcurrency)}
}

// probeAll runs every anonymous probe of ps at once, one phase under
// ProbePhaseTimeout, and answers their dimensions in the order of ps.
//
// base is the data of every probe; clients are the Dex clients the render
// declares, nil when nothing was rendered (no inputs on record).
func (pr *prober) probeAll(ctx context.Context, base ProbeData, clients []plan.DexClient, rendered bool, ps []definitions.Probe) []Dimension {
	ctx, cancel := context.WithTimeout(ctx, ProbePhaseTimeout)
	defer cancel()
	dims := make([]Dimension, len(ps))
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dims[i] = pr.probe(ctx, base, clients, rendered, p)
		}()
	}
	wg.Wait()
	return dims
}

// probe runs one anonymous probe and answers it as a dimension of kind probe:
// as defined when every request answered an expected status, drifted when
// any answered another, not checked with ReasonUnreachable when one got no
// answer and none answered another status (an unexpected status is drift
// whatever the other requests did; an unreachable target is no comparison),
// not checked when it had no request to make, or when its template names a
// choice not on record (the reason names the field). The probe's requests
// are sent at once and answered in the order of the clients.
func (pr *prober) probe(ctx context.Context, base ProbeData, clients []plan.DexClient, rendered bool, p definitions.Probe) Dimension {
	d := Dimension{ID: p.ID, Kind: definitions.KindProbe, Key: p.Key, Mark: NotChecked, Probe: &ProbeResult{Expect: p.Expect, Requests: []Request{}}}
	tmpl, err := template.New(p.ID).Parse(p.URL)
	if err != nil {
		d.Reason = err.Error()
		return d
	}
	var data []ProbeData
	switch {
	case !p.PerDexClient:
		data = []ProbeData{base}
	case !rendered:
		d.Reason = ReasonNoRender
		return d
	default:
		for _, cl := range clients {
			if len(cl.RedirectURIs) > 0 {
				pd := base
				pd.ClientID, pd.RedirectURI = url.QueryEscape(cl.ID), url.QueryEscape(cl.RedirectURIs[0])
				data = append(data, pd)
			}
		}
		if len(data) == 0 {
			d.Reason = ReasonNoDexClients
			return d
		}
	}
	// Every URL is rendered before anything is sent: a template error or a
	// choice not on record is the whole dimension's, not one request's.
	urls := make([]string, len(data))
	for i, pd := range data {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, pd); err != nil {
			d.Reason = err.Error()
			return d
		}
		if fields := missingFields(buf.String()); len(fields) > 0 {
			d.Reason = missingChoice(fields)
			return d
		}
		urls[i] = buf.String()
	}
	reqs := make([]Request, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reqs[i] = pr.send(ctx, u, p)
		}()
	}
	wg.Wait()
	d.Mark = AsDefined
	for i, r := range reqs {
		if data[i].ClientID != "" {
			r.Client, _ = url.QueryUnescape(data[i].ClientID)
		}
		switch {
		case r.Error != "":
			if d.Mark == AsDefined {
				d.Mark, d.Reason, d.Detail = NotChecked, unreachable(r.URL), r.Error
			}
		case !r.OK:
			d.Mark, d.Reason, d.Detail = Drifted, "", ""
		}
		d.Probe.Requests = append(d.Probe.Requests, r)
	}
	return d
}

// send is one anonymous request of probe p and its answer (get): the status
// held against p's expectation, or the transport's error.
func (pr *prober) send(ctx context.Context, u string, p definitions.Probe) Request {
	r := Request{URL: u}
	rp, err := pr.get(ctx, u, p.DexConnectorStep)
	if err != nil {
		r.Error = strings.TrimSpace(err.Error())
		return r
	}
	r.Status, r.Connector, r.Message = rp.status, rp.connector, rp.fault
	r.OK = rp.fault == "" && slices.Contains(p.Expect, rp.status)
	return r
}

// do sends the anonymous GET of u as the manager, once a slot is free: the
// slot is held while the answer's headers are awaited, where an unreachable
// host hangs, and freed before the body is read. The caller closes the body.
// A phase over before the slot is free is the request's error too.
func (pr *prober) do(ctx context.Context, u string) (*http.Response, error) {
	select {
	case pr.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-pr.slots }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "giantswarm-platform-manager")
	return pr.client.Do(req)
}

// unreachable is the reason for a target the manager got no answer from: one
// sentence naming the host, never the URL (a Dex client's redirect URI is in
// its query). The transport's error is the detail.
func unreachable(u string) string {
	host := u
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		host = p.Host
	}
	return ReasonUnreachable + ": " + host
}
