package verify

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"text/template"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// ProbeResult is what an anonymous probe answered, request by request.
type ProbeResult struct {
	Expect   []int     `json:"expect"`
	Requests []Request `json:"requests"`
}

// Request is one anonymous HTTP request of a probe and its answer.
type Request struct {
	URL    string `json:"url"`
	Client string `json:"client,omitempty"`
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	OK     bool   `json:"ok"`
}

// The reasons a probe is not checked: ReasonNoDexClients, a per-client probe
// with no client to run for; ReasonUnreachable, a request the manager could
// not get an answer to (timeout, DNS, connection refused), the error appended.
const (
	ReasonNoDexClients = "the render declares no Dex client with a redirect URI for the inputs on record"
	ReasonUnreachable  = "unreachable from the manager"
)

// probeData fills a probe's URL template.
type probeData struct {
	BaseDomain, ClientID, RedirectURI string
}

// probe runs one anonymous probe and answers it as a dimension of kind probe:
// as defined when every request answered an expected status, drifted when
// any answered another, not checked with ReasonUnreachable when one got no
// answer and none answered another status (an unexpected status is drift
// whatever the other requests did; an unreachable target is no comparison),
// not checked when it had no request to make.
//
// baseDomain is the installation's; clients are the Dex clients the render
// declares, nil when nothing was rendered (no inputs on record).
func probe(ctx context.Context, client *http.Client, baseDomain string, clients []plan.DexClient, rendered bool, p definitions.Probe) Dimension {
	d := Dimension{ID: p.ID, Kind: definitions.KindProbe, Key: p.Key, Mark: NotChecked, Probe: &ProbeResult{Expect: p.Expect, Requests: []Request{}}}
	tmpl, err := template.New(p.ID).Parse(p.URL)
	if err != nil {
		d.Reason = err.Error()
		return d
	}
	var data []probeData
	switch {
	case !p.PerDexClient:
		data = []probeData{{BaseDomain: baseDomain}}
	case !rendered:
		d.Reason = ReasonNoRender
		return d
	default:
		for _, cl := range clients {
			if len(cl.RedirectURIs) > 0 {
				data = append(data, probeData{BaseDomain: baseDomain, ClientID: url.QueryEscape(cl.ID), RedirectURI: url.QueryEscape(cl.RedirectURIs[0])})
			}
		}
		if len(data) == 0 {
			d.Reason = ReasonNoDexClients
			return d
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	d.Mark = AsDefined
	for _, pd := range data {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, pd); err != nil {
			d.Reason, d.Mark = err.Error(), NotChecked
			return d
		}
		r := send(ctx, client, buf.String(), p.Expect)
		if pd.ClientID != "" {
			r.Client, _ = url.QueryUnescape(pd.ClientID)
		}
		switch {
		case r.Error != "":
			if d.Mark == AsDefined {
				d.Mark, d.Reason = NotChecked, ReasonUnreachable+": "+r.Error
			}
		case !r.OK:
			d.Mark, d.Reason = Drifted, ""
		}
		d.Probe.Requests = append(d.Probe.Requests, r)
	}
	return d
}

func send(ctx context.Context, client *http.Client, u string, expect []int) Request {
	r := Request{URL: u}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	req.Header.Set("User-Agent", "giantswarm-platform-manager")
	resp, err := client.Do(req)
	if err != nil {
		r.Error = strings.TrimSpace(err.Error())
		return r
	}
	_ = resp.Body.Close()
	r.Status = resp.StatusCode
	r.OK = slices.Contains(expect, resp.StatusCode)
	return r
}
