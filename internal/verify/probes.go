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
	"github.com/giantswarm/giantswarm-platform-manager/render"
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

// probe runs one anonymous probe and answers it as a dimension of kind probe:
// as defined when every request answered an expected status, drifted when
// any answered another, not checked with ReasonUnreachable when one got no
// answer and none answered another status (an unexpected status is drift
// whatever the other requests did; an unreachable target is no comparison),
// not checked when it had no request to make, or when its template names a
// choice not on record (the reason names the field).
//
// base is the data of every probe; clients are the Dex clients the render
// declares, nil when nothing was rendered (no inputs on record).
func probe(ctx context.Context, client *http.Client, base ProbeData, clients []plan.DexClient, rendered bool, p definitions.Probe) Dimension {
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
		if fields := missingFields(buf.String()); len(fields) > 0 {
			d.Reason, d.Mark = missingChoice(fields), NotChecked
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
