package verify

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// maxProbeBody is how much of an HTTP probe's answer is read: enough for a
// body a probe looks for a substring in, and for Dex's connector picker.
const maxProbeBody = 64 << 10

// reply is the answer an HTTP probe is held to: its status, Location and the
// start of its body. For a probe that takes the Dex connector step it is the
// connector's answer, and connector names it. fault says why the answer is
// drifted whatever the expectation: Dex's /auth answer names no connector,
// or the connector refused the client (404) or its redirect URI (400).
type reply struct {
	status    int
	location  string
	body      []byte
	connector string
	fault     string
}

// String is the reply in a few words: the status, and the connector whose
// answer it is.
func (r reply) String() string {
	if r.connector == "" {
		return strconv.Itoa(r.status)
	}
	return strconv.Itoa(r.status) + " from connector " + r.connector
}

// get sends the GET of u and reads the answer. With dexConnectorStep, u is an
// authorization request on Dex's /auth, which only picks the connector and
// answers the same for any client id and redirect URI: a 302 to
// /auth/<connector> with one connector, its picker page (200) with a link to
// each with several. The step requests that connector, as Dex's own answer
// names it (the redirect's location, the picker's first link carrying the
// same query), and answers with the connector's reply: its handler is the one
// that validates the client and the redirect URI. Any other answer of /auth
// is the reply as it is.
func (pr *prober) get(ctx context.Context, u string, dexConnectorStep bool) (reply, error) {
	r, err := pr.fetch(ctx, u)
	if err != nil || !dexConnectorStep {
		return r, err
	}
	auth, _ := url.Parse(u) // the request fetch sent parsed it
	var next *url.URL
	var connector string
	switch r.status {
	case http.StatusFound:
		if next, connector = dexConnector(auth, r.location); next == nil {
			r.fault = "the redirect leads to no Dex connector (" + r.location + ")"
		}
	case http.StatusOK:
		if next, connector = dexPickerLink(auth, r.body); next == nil {
			r.fault = "the page links to no Dex connector"
		}
	}
	if next == nil {
		return r, nil
	}
	c, err := pr.fetch(ctx, next.String())
	if err != nil {
		return c, err
	}
	c.connector, c.fault = connector, dexRefusal(c.status, next.Query())
	return c, nil
}

// fetch sends the GET of u and reads the answer, the body up to maxProbeBody.
func (pr *prober) fetch(ctx context.Context, u string) (reply, error) {
	resp, err := pr.do(ctx, u)
	if err != nil {
		return reply{}, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProbeBody))
	_ = resp.Body.Close()
	return reply{status: resp.StatusCode, location: resp.Header.Get("Location"), body: body}, nil
}

// dexConnector is the connector ref leads to from the authorization request
// auth, and its id: ref resolved against auth, on auth's host, at auth's path
// and one more segment (Dex's /auth/<connector>, under the issuer's path).
// Anything else is nil.
func dexConnector(auth *url.URL, ref string) (*url.URL, string) {
	u, err := auth.Parse(ref)
	if err != nil || u.Host != auth.Host {
		return nil, ""
	}
	id, ok := strings.CutPrefix(u.Path, strings.TrimSuffix(auth.Path, "/")+"/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return nil, ""
	}
	return u, id
}

// hrefs are the link targets of an HTML page, as written: HTML-escaped.
var hrefs = regexp.MustCompile(`href\s*=\s*["']([^"']*)["']`)

// dexPickerLink is the first connector Dex's picker page links to (dexConnector),
// and its id; nil when it links to none.
func dexPickerLink(auth *url.URL, page []byte) (*url.URL, string) {
	for _, m := range hrefs.FindAllSubmatch(page, -1) {
		if u, id := dexConnector(auth, html.UnescapeString(string(m[1]))); u != nil {
			return u, id
		}
	}
	return nil, ""
}

// dexRefusal is what a connector's answer says about the authorization
// request q when Dex refused it: 404 is a client id Dex does not know, 400 a
// redirect URI not registered for the client. Empty for any other answer.
func dexRefusal(status int, q url.Values) string {
	switch status {
	case http.StatusNotFound:
		return "Dex does not know client " + q.Get("client_id")
	case http.StatusBadRequest:
		return "Dex does not know redirect URI " + q.Get("redirect_uri") + " for client " + q.Get("client_id")
	}
	return ""
}
