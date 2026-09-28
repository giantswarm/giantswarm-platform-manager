package verify

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The fake Dex's connectors ("local" a password connector), its one client
// and the feature its probes belong to.
const (
	connectorGitHub = "github"
	connectorLocal  = "local"
	clientMuster    = "muster"
	featureIdentity = "identity"
)

// fakeDex answers as Dex does: /auth the same for any client and redirect
// URI — a redirect to the one connector, the picker page with a link to each
// of several — and /auth/<connector> validates the request: 404 for an
// unknown client, 400 for a redirect URI not registered for it, else a
// redirect to the identity provider (a password connector, "local", serves
// its form).
type fakeDex struct {
	prefix     string              // the issuer's path
	connectors []string            // in the picker's order
	clients    map[string][]string // client id → its redirect URIs
}

// dexPicker is the picker page as Dex's login template renders it: the
// links HTML-escaped, a stylesheet link before them.
var dexPicker = template.Must(template.New("login").Parse(`<html><head><link href="static/main.css" rel="stylesheet"></head><body>
{{ range . }}<a href="{{ .URL }}" target="_self"><button>Log in with {{ .ID }}</button></a>
{{ end }}</body></html>`))

func (d fakeDex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth, q := d.prefix+"/auth", r.URL.Query()
	connector, onConnector := strings.CutPrefix(r.URL.Path, auth+"/")
	switch {
	case r.URL.Path == auth && len(d.connectors) == 1:
		http.Redirect(w, r, auth+"/"+d.connectors[0]+"?"+q.Encode(), http.StatusFound) // #nosec G710 -- a test double redirecting to its own connector
	case r.URL.Path == auth:
		type link struct {
			ID  string
			URL template.URL
		}
		var links []link
		for _, c := range d.connectors {
			links = append(links, link{c, template.URL(auth + "/" + c + "?" + q.Encode())}) // #nosec G203 -- a test double's own links
		}
		_ = dexPicker.Execute(w, links)
	case onConnector && d.clients[q.Get("client_id")] == nil:
		http.Error(w, "Invalid client_id", http.StatusNotFound)
	case onConnector && !slices.Contains(d.clients[q.Get("client_id")], q.Get("redirect_uri")):
		http.Error(w, "Unregistered redirect_uri", http.StatusBadRequest)
	case onConnector && !slices.Contains(d.connectors, connector):
		http.Error(w, "Requested resource does not exist", http.StatusBadRequest)
	case onConnector && connector == connectorLocal:
		_, _ = w.Write([]byte(`<form method="post"><input name="login"></form>`))
	case onConnector:
		http.Redirect(w, r, "https://idp.example.test/authorize", http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

// tlsServing serves h over TLS: its host and a client that trusts it and
// does not follow redirects, as the manager probes.
func tlsServing(t *testing.T, h http.Handler) (string, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return srv.Listener.Addr().String(), client
}

// dexStep runs the Dex auth probe of client and redirectURI against the Dex
// at host (and the issuer's path) both ways: the anonymous probe of the
// definitions (verify_capability's) and the live one of the render.
func dexStep(t *testing.T, client *http.Client, host, prefix, id, redirectURI string) (Dimension, Check) {
	t.Helper()
	anonymous := definitions.Probe{ID: "dex-auth-request", Feature: featureIdentity,
		URL:          "https://" + host + prefix + "/auth?client_id={{.ClientID}}&redirect_uri={{.RedirectURI}}&response_type=code&scope=openid",
		PerDexClient: true, DexConnectorStep: true, Expect: []int{200, 302}}
	d := newProber(client).probe(context.Background(), base, []plan.DexClient{{ID: id, RedirectURIs: []string{redirectURI}}}, true, anonymous)
	return d, liveHTTP(client, render.DexAuthProbe("live-dex-auth-per-client", featureIdentity, host+prefix, id, redirectURI))
}

// The Dex auth probes take the connector step: whichever way /auth answers
// (the picker, the redirect to the one connector), the connector's answer
// decides, the connector named as Dex's answer names it. A redirect to the
// identity provider or a password form is as defined; an unknown client
// (404) and an unregistered redirect URI (400) are drifted, naming the
// client and the redirect URI. The anonymous and the live probe read alike.
func TestDexAuthProbesTakeTheConnectorStep(t *testing.T) {
	const callback = "https://muster.example.test/oauth/callback"
	known := map[string][]string{clientMuster: {callback}}
	picker := fakeDex{connectors: []string{connectorGitHub, connectorLocal}, clients: known}
	single := fakeDex{connectors: []string{connectorGitHub}, clients: known}
	for _, tc := range []struct {
		name                string
		dex                 fakeDex
		client, redirectURI string
		mark                Mark
		status              int
		connector, message  string
	}{
		{"the picker, a known client", picker, clientMuster, callback, AsDefined, http.StatusFound, connectorGitHub, ""},
		{"the one connector's redirect, a known client", single, clientMuster, callback, AsDefined, http.StatusFound, connectorGitHub, ""},
		{"a password connector's form", fakeDex{connectors: []string{connectorLocal}, clients: known}, clientMuster, callback, AsDefined, http.StatusOK, connectorLocal, ""},
		{"under the issuer's path", fakeDex{prefix: "/dex", connectors: []string{connectorGitHub}, clients: known}, clientMuster, callback, AsDefined, http.StatusFound, connectorGitHub, ""},
		{"the picker, an unknown client", picker, "bogus", "https://bogus.example.test/cb", Drifted, http.StatusNotFound, connectorGitHub, "Dex does not know client bogus"},
		{"the one connector, an unknown client", single, "bogus", callback, Drifted, http.StatusNotFound, connectorGitHub, "Dex does not know client bogus"},
		{"an unregistered redirect URI", single, clientMuster, "https://elsewhere.example.test/cb", Drifted, http.StatusBadRequest, connectorGitHub,
			"Dex does not know redirect URI https://elsewhere.example.test/cb for client muster"},
	} {
		host, client := tlsServing(t, tc.dex)
		d, c := dexStep(t, client, host, tc.dex.prefix, tc.client, tc.redirectURI)
		want := Request{Client: tc.client, Status: tc.status, Connector: tc.connector, Message: tc.message, OK: tc.mark == AsDefined}
		if len(d.Probe.Requests) != 1 {
			t.Errorf("%s: anonymous: %+v", tc.name, d)
			continue
		}
		got := d.Probe.Requests[0]
		got.URL = ""
		if d.Mark != tc.mark || got != want {
			t.Errorf("%s: anonymous: mark %q, request %+v, want %q %+v", tc.name, d.Mark, got, tc.mark, want)
		}
		if c.Mark != tc.mark || c.Message != want.Answer() {
			t.Errorf("%s: live: mark %q, message %q, want %q %q", tc.name, c.Mark, c.Message, tc.mark, want.Answer())
		}
	}
}

// An answer of /auth that names no connector is not Dex's: a page without a
// connector link, or a redirect off Dex's /auth, is drifted with its status
// and why, the step never taken.
func TestDexAuthProbesWithoutAConnectorAreDrifted(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  http.HandlerFunc
		status  int
		message string
	}{
		{"a page that links to no connector", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html><head><link href="static/main.css" rel="stylesheet"></head><body><a href="/auth">sign in</a> <a href="/auth/github/login">login</a></body></html>`))
		}, http.StatusOK, "the page links to no Dex connector"},
		{"a redirect to another host", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://login.example.test/auth/github", http.StatusFound)
		}, http.StatusFound, "the redirect leads to no Dex connector (https://login.example.test/auth/github)"},
	} {
		var requests atomic.Int32
		host, client := tlsServing(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			tc.answer(w, r)
		}))
		d, c := dexStep(t, client, host, "", clientMuster, "https://muster.example.test/oauth/callback")
		want := Request{Client: clientMuster, Status: tc.status, Message: tc.message}
		if len(d.Probe.Requests) != 1 {
			t.Errorf("%s: anonymous: %+v", tc.name, d)
			continue
		}
		got := d.Probe.Requests[0]
		got.URL = ""
		if d.Mark != Drifted || got != want {
			t.Errorf("%s: anonymous: mark %q, request %+v, want %+v", tc.name, d.Mark, got, want)
		}
		if c.Mark != Drifted || c.Message != want.Answer() {
			t.Errorf("%s: live: mark %q, message %q, want %q", tc.name, c.Mark, c.Message, want.Answer())
		}
		if n := requests.Load(); n != 2 {
			t.Errorf("%s: %d requests, want /auth once per probe", tc.name, n)
		}
	}
}

// The picker's links are read HTML-unescaped and resolved against /auth:
// the connector's query is the request's own, whatever the page escaped.
func TestDexPickerLinkUnescapesTheQuery(t *testing.T) {
	auth, err := url.Parse("https://dex.example.test/auth?client_id=muster&redirect_uri=https%3A%2F%2Fm%2Fcb")
	if err != nil {
		t.Fatal(err)
	}
	u, id := dexPickerLink(auth, []byte(`<a href='/auth/ldap?client_id=muster&amp;redirect_uri=https%3A%2F%2Fm%2Fcb'>`))
	if u == nil || id != "ldap" || u.Query().Get("redirect_uri") != "https://m/cb" || u.Query().Get("client_id") != clientMuster {
		t.Fatalf("link %v, connector %q", u, id)
	}
}
