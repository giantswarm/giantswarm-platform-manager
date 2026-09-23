// Package live is the manager's reads as the person: verify_installation and
// watch_action on the one registration, which muster calls with the person's
// App user token as the bearer and their platform ID token next to it
// (MCPServer auth.forwardIdentity, the X-Muster-Id-Token header), and the
// loop-back that reads an installation with that ID token. The forwarded
// token is the platform identity provider's ID token for the person; it is
// validated the way the platform's forward-mode servers validate theirs
// (mcp-oauth's OIDC primitives: the issuer's JWKS, the issuer, the audiences
// the forwarded tokens carry). A live read then opens — or reuses, per
// person — an MCP session at muster's own endpoint with that token and calls
// the installation's kubernetes tools through it: muster's per-installation
// token exchange, the tunnel to a private installation, the installation's
// mcp-kubernetes and the person's RBAC decide what is read. The manager holds
// no credential of its own on any installation.
package live

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/giantswarm/mcp-oauth/providers/oidc"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/aggregator"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// Config is the live reads' configuration, from the chart's values.
type Config struct {
	// Issuer is the platform identity provider's issuer: the iss every
	// forwarded token carries.
	Issuer string
	// Audiences are the OAuth clients a forwarded token may carry in aud: a
	// person's ID token names the client they signed in with — the
	// platform's own, a portal's — and the audiences the registration
	// requires of muster, which every forwarded token carries by
	// construction. A token is accepted when one of its audiences is listed.
	// At least one is required: an empty list would accept any audience.
	Audiences []string
	// JWKSURL is the issuer's key set, an https URL; empty reads it from the
	// issuer's OpenID discovery document. The key set is read over TLS only
	// (the JWKS client refuses anything else on every fetch), so a plain-http
	// URL is refused here, at start-up, instead of on every token.
	JWKSURL string
	// AllowPrivateIPJWKS lets the issuer's discovery document or its key set
	// be read from a private address (an in-cluster Dex): the SSRF guard's
	// allowance, not the transport's.
	AllowPrivateIPJWKS bool
	// CAFile is a PEM bundle the issuer's certificate chains to; empty is
	// the system trust.
	CAFile string
	// MusterURL is muster's own MCP endpoint as reached from the pod: where
	// the loop-back session goes.
	MusterURL string
	// KubernetesFamily is the muster family (or singleton server name) the
	// installations' kubernetes tools are aggregated under: the tools are
	// x_<family>_get, _list, _logs and _api_resources. KubernetesInstanceArg is the family's
	// argument that selects the member; empty for a singleton.
	// KubernetesMember names the member that serves an installation, as
	// muster names the server: a text/template over {{ .Installation }}
	// (`{{ .Installation }}-mcp-kubernetes` on the platform, whose
	// agent-platform-mcps chart registers every installation's mcp-kubernetes
	// under that name). muster resolves the instance argument to a member by
	// its server name, never by the installation's.
	KubernetesFamily      string
	KubernetesInstanceArg string
	KubernetesMember      string
	// IdleLifetime is how long a person's loop-back session is kept without
	// a call before it is closed. Zero is DefaultIdleLifetime.
	IdleLifetime time.Duration
	// Version is what the loop-back session says it is.
	Version string
}

// DefaultIdleLifetime is how long an idle loop-back session lives.
const DefaultIdleLifetime = 15 * time.Minute

// OpenTimeout bounds the opening of a person's loop-back session at muster
// (the connection and the MCP initialize): a muster that does not answer is
// named, and every other person's reads — which wait on the same lock —
// are held no longer than this.
const OpenTimeout = 30 * time.Second

// ClientName is the clientInfo.name of the loop-back session at muster.
const ClientName = "giantswarm-platform-manager"

// Validate checks required fields.
func (c Config) Validate() error {
	for _, f := range []struct{ what, v string }{{"issuer", c.Issuer}, {"muster URL", c.MusterURL}, {"kubernetes family", c.KubernetesFamily}} {
		if strings.TrimSpace(f.v) == "" {
			return fmt.Errorf("live: %s is required", f.what)
		}
	}
	if c.KubernetesInstanceArg != "" {
		if strings.TrimSpace(c.KubernetesMember) == "" {
			return errors.New("live: kubernetes member is required with an instance argument: the template of the member's server name over {{ .Installation }}")
		}
		if _, err := memberTemplate(c.KubernetesMember); err != nil {
			return err
		}
	}
	if len(c.audiences()) == 0 {
		return errors.New("live: audiences is required: at least one OAuth client whose ID tokens the live tools accept (an empty list would accept every audience)")
	}
	for _, f := range []struct{ what, v string }{{"issuer", c.Issuer}, {"muster URL", c.MusterURL}} {
		u, err := url.Parse(f.v)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return fmt.Errorf("live: %s must be an absolute http(s) URL: %q", f.what, f.v)
		}
	}
	if c.JWKSURL != "" {
		if u, err := url.Parse(c.JWKSURL); err != nil || u.Host == "" || u.Scheme != "https" {
			return fmt.Errorf("live: JWKS URL must be an absolute https:// URL, got %q: the key set is read over TLS only — name an in-cluster identity provider by the Service name its certificate carries, with the private-IP allowance for the address and the CA file for its CA", c.JWKSURL)
		}
	}
	return nil
}

// audiences is the configured list, normalized.
func (c Config) audiences() []string { return ParseAudiences(strings.Join(c.Audiences, ",")) }

// ParseAudiences reads a comma-separated audience list as the flag and the
// chart pass it: trimmed, without empties and without duplicates, in order.
func ParseAudiences(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range strings.Split(s, ",") {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// Info is the live reads' configuration as get_info reports it.
type Info struct {
	Issuer                string   `json:"issuer"`
	Audiences             []string `json:"audiences"`
	MusterURL             string   `json:"musterUrl"`
	KubernetesFamily      string   `json:"kubernetesFamily"`
	KubernetesInstanceArg string   `json:"kubernetesInstanceArg,omitempty"`
	KubernetesMember      string   `json:"kubernetesMember,omitempty"`
}

// Client validates forwarded tokens and holds the loop-back sessions.
type Client struct {
	cfg  Config
	log  *slog.Logger
	jwks *oidc.JWKSClient
	http *http.Client
	// member names the family member that serves an installation
	// (Config.KubernetesMember parsed); nil for a singleton server.
	member *template.Template

	mu       sync.Mutex
	jwksURL  string
	sessions map[string]*session
}

// New builds the client; the key set is read on the first token.
func New(cfg Config, log *slog.Logger) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.Audiences = cfg.audiences()
	if cfg.IdleLifetime <= 0 {
		cfg.IdleLifetime = DefaultIdleLifetime
	}
	if log == nil {
		log = slog.Default()
	}
	pool, err := rootCAs(cfg.CAFile)
	if err != nil {
		return nil, fmt.Errorf("live: %w", err)
	}
	var hc *http.Client
	if cfg.AllowPrivateIPJWKS {
		hc = oidc.NewPrivateIPAllowedHTTPClient(oidc.DefaultHTTPTimeout, pool)
	} else {
		hc = oidc.NewSSRFSafeHTTPClient(oidc.DefaultHTTPTimeout, pool)
	}
	c := &Client{cfg: cfg, log: log, http: hc, jwksURL: cfg.JWKSURL, sessions: map[string]*session{},
		jwks: oidc.NewJWKSClientWithOptions(oidc.JWKSClientOptions{HTTPClient: hc, AllowPrivateIP: cfg.AllowPrivateIPJWKS, RootCAs: pool, Logger: log})}
	if cfg.KubernetesInstanceArg != "" {
		if c.member, err = memberTemplate(cfg.KubernetesMember); err != nil {
			return nil, err
		}
	}
	log.Info("live reads enabled", "issuer", cfg.Issuer, "audiences", cfg.Audiences, "jwks", cfg.JWKSURL, "muster", cfg.MusterURL, "kubernetesFamily", cfg.KubernetesFamily, "instanceArg", cfg.KubernetesInstanceArg, "member", cfg.KubernetesMember)
	return c, nil
}

// memberTemplate parses Config.KubernetesMember: a text/template over
// {{ .Installation }} that names the family member serving an installation.
func memberTemplate(s string) (*template.Template, error) {
	t, err := template.New("member").Option("missingkey=error").Parse(s)
	if err != nil {
		return nil, fmt.Errorf("live: kubernetes member %q is not a template over {{ .Installation }}: %w", s, err)
	}
	return t, nil
}

// rootCAs is the system pool plus the PEM bundle at file; nil without a file.
func rootCAs(file string) (*x509.CertPool, error) {
	if file == "" {
		return nil, nil
	}
	pem, err := os.ReadFile(file) // #nosec G304 -- the operator's path
	if err != nil {
		return nil, fmt.Errorf("read CA file %s: %w", file, err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no CA certificate in %s", file)
	}
	return pool, nil
}

// Info is the configuration as reported.
func (c *Client) Info() Info {
	return Info{Issuer: c.cfg.Issuer, Audiences: c.cfg.Audiences, MusterURL: c.cfg.MusterURL, KubernetesFamily: c.cfg.KubernetesFamily, KubernetesInstanceArg: c.cfg.KubernetesInstanceArg, KubernetesMember: c.cfg.KubernetesMember}
}

// Verify validates a forwarded token: signature against the issuer's key
// set, issuer, one of the audiences, time; the person it names comes back.
func (c *Client) Verify(ctx context.Context, token string) (*identity.Identity, error) {
	jwksURL, err := c.keySet(ctx)
	if err != nil {
		return nil, err
	}
	claims, err := oidc.ValidateIDToken(ctx, token, c.jwks, jwksURL, c.cfg.Issuer, c.cfg.Audiences)
	if err != nil {
		return nil, err
	}
	if claims.Subject == "" {
		return nil, errors.New("the token names no subject")
	}
	return &identity.Identity{Subject: claims.Subject, Email: claims.Email}, nil
}

// keySet is the JWKS URL: configured, or read once from the issuer's
// discovery document.
func (c *Client) keySet(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwksURL != "" {
		return c.jwksURL, nil
	}
	doc, err := oidc.NewDiscoveryClient(c.http, 0, c.log).Discover(ctx, c.cfg.Issuer)
	if err != nil {
		return "", fmt.Errorf("discover the issuer %s: %w", c.cfg.Issuer, err)
	}
	c.jwksURL = doc.JWKSUri
	c.log.Info("issuer discovered", "issuer", c.cfg.Issuer, "jwks", c.jwksURL)
	return c.jwksURL, nil
}

// session is one person's loop-back session at muster.
type session struct {
	s     *aggregator.Session
	mu    sync.Mutex
	token string
	last  time.Time
}

func (s *session) bearer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// Cluster opens, or reuses, the person's loop-back session with their token
// and answers the reads of one installation through it.
func (c *Client) Cluster(ctx context.Context, token string, id *identity.Identity, installation string) (verify.Cluster, error) {
	s, err := c.session(ctx, id.Subject, token)
	if err != nil {
		return nil, err
	}
	return &cluster{c: c, s: s, installation: installation, person: id.String()}, nil
}

func (c *Client) session(ctx context.Context, sub, token string) (*session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, s := range c.sessions {
		if k != sub && now.Sub(s.last) > c.cfg.IdleLifetime {
			_ = s.s.Close()
			delete(c.sessions, k)
		}
	}
	if s, ok := c.sessions[sub]; ok {
		s.mu.Lock()
		s.token, s.last = token, now
		s.mu.Unlock()
		return s, nil
	}
	s := &session{token: token, last: now}
	octx, cancel := context.WithTimeout(ctx, OpenTimeout)
	defer cancel()
	agg, err := aggregator.Open(octx, c.cfg.MusterURL, s.bearer, ClientName, c.cfg.Version, nil)
	if err != nil {
		if errors.Is(octx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, fmt.Errorf("the loop-back to muster at %s did not answer within %s", c.cfg.MusterURL, OpenTimeout)
		}
		return nil, fmt.Errorf("the loop-back to muster at %s: %w", c.cfg.MusterURL, err)
	}
	s.s = agg
	c.sessions[sub] = s
	c.log.Info("loop-back session opened", "sub", sub, "muster", c.cfg.MusterURL)
	return s, nil
}

// Close ends every loop-back session.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, s := range c.sessions {
		_ = s.s.Close()
		delete(c.sessions, k)
	}
}

// cluster reads one installation through the person's session.
type cluster struct {
	c            *Client
	s            *session
	installation string
	person       string
}

// The kubernetes tools' operations, x_<family>_<op>.
const (
	opGet          = "get"
	opList         = "list"
	opLogs         = "logs"
	opAPIResources = "api_resources"
)

// fanOutWait bounds how long a first call waits for muster to connect the
// person's session to the installation's servers: muster's SSO fan-out runs
// in the background right after initialize, and the tools appear when it is
// done.
const fanOutWait = 15 * time.Second

func (k *cluster) tool(op string) string { return "x_" + k.c.cfg.KubernetesFamily + "_" + op }

// member is the family member that serves the installation, by muster's
// server name: KubernetesMember executed over the installation.
func (k *cluster) member() (string, error) {
	var buf strings.Builder
	if err := k.c.member.Execute(&buf, struct{ Installation string }{k.installation}); err != nil {
		return "", fmt.Errorf("live: kubernetes member for %s: %w", k.installation, err)
	}
	return buf.String(), nil
}

// call runs one kubernetes tool for the installation and answers its text,
// the tool's refusal mapped to the verify's errors. A family's tool takes
// the member's server name in the instance argument: muster routes by it.
func (k *cluster) call(ctx context.Context, op string, args map[string]any) (string, error) {
	if k.c.cfg.KubernetesInstanceArg != "" {
		member, err := k.member()
		if err != nil {
			return "", err
		}
		args[k.c.cfg.KubernetesInstanceArg] = member
	}
	name := k.tool(op)
	res, err := k.s.s.Call(ctx, name, args)
	if toolNotFound(res, err) {
		// muster does not list the tool for this session yet (the fan-out
		// to the installation's servers runs after initialize): wait for it,
		// bounded, and call once more.
		if werr := k.waitForTool(ctx, name); werr != nil {
			return "", werr
		}
		res, err = k.s.s.Call(ctx, name, args)
	}
	if err != nil {
		return "", err
	}
	text := aggregator.TextOf(res)
	if res.IsError {
		return "", classify(text)
	}
	return text, nil
}

func isToolNotFound(err error) bool {
	return toolNotFoundText(err.Error())
}

// toolNotFoundText says whether text is muster's refusal of a tool it does
// not list for the session — "tool not found: x_kubernetes_get", "unknown
// tool" — never an object of the installation that does not exist.
func toolNotFoundText(text string) bool {
	return strings.Contains(text, "tool not found") || strings.Contains(text, "unknown tool")
}

// toolNotFound says whether a call's answer is muster's tool-not-found: as
// the call's error, or as the tool result's error text (muster answers it
// either way).
func toolNotFound(res *mcp.CallToolResult, err error) bool {
	if err != nil {
		return isToolNotFound(err)
	}
	return res != nil && res.IsError && toolNotFoundText(aggregator.TextOf(res))
}

// waitForTool waits, bounded, for muster to list name for this session.
func (k *cluster) waitForTool(ctx context.Context, name string) error {
	deadline := time.Now().Add(fanOutWait)
	for {
		names, err := k.s.s.Tools(ctx)
		if err != nil {
			return err
		}
		for _, n := range names {
			if n == name {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("muster lists no %s for %s: the person's session is not connected to the installations' kubernetes servers (the family %q of live.muster.kubernetesFamily)", name, k.person, k.c.cfg.KubernetesFamily)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

var (
	authServerRe = regexp.MustCompile(`server '([^']+)'`)
	urlRe        = regexp.MustCompile(`https?://\S+`)
	userRe       = regexp.MustCompile(`User "([^"]+)"`)
	// objectNotFoundRe is the apiserver's NotFound as mcp-kubernetes relays
	// it: the resource, the object's name in quotes, "not found"
	// (`helmreleases.helm.toolkit.fluxcd.io "kagent" not found`). muster's
	// "tool not found: x_kubernetes_get" and the apiserver's "the server
	// could not find the requested resource" are refusals of the read, not
	// an object that does not exist.
	objectNotFoundRe = regexp.MustCompile(`"[^"]+" not found`)
)

// classify maps a tool's refusal to the verify's errors: muster's
// auth_required for the installation, mcp-kubernetes's response_too_large
// for a read it will not answer whole, the apiserver's forbidden naming the
// person, an object that does not exist (the apiserver's NotFound with the
// object's name), anything else as it was said — muster's tool not found and
// the apiserver's unknown resource among them, which a check reads as not
// checked, never as an object missing from the installation.
func classify(text string) error {
	if tl := tooLarge(text); tl != nil {
		return tl
	}
	switch {
	case toolNotFoundText(text):
		return errors.New("muster lists no such tool for this session: " + strings.TrimSpace(text))
	case strings.HasPrefix(text, "auth_required") || strings.Contains(text, "requires authentication"):
		a := &verify.AuthRequired{Message: text}
		if m := authServerRe.FindStringSubmatch(text); m != nil {
			a.Server = m[1]
		}
		a.URL = urlRe.FindString(text)
		return a
	case strings.Contains(text, "is forbidden"):
		f := &verify.Forbidden{Reason: strings.TrimSpace(text)}
		if m := userRe.FindStringSubmatch(text); m != nil {
			f.Person = m[1]
		}
		return f
	case objectNotFoundRe.MatchString(text):
		return fmt.Errorf("%w: %s", verify.ErrNotFound, strings.TrimSpace(text))
	}
	return errors.New(strings.TrimSpace(text))
}

// responseTooLarge is the error code of mcp-kubernetes's refusal to answer a
// call whole: mcp-toolkit's responsecap middleware caps a tool's answer
// (128 KiB) and answers `{"error":"response_too_large","bytes":…,"limit":…,
// "message":…,"hint":…}` as the result's error in place of a truncated
// object or log.
const responseTooLarge = "response_too_large"

// tooLarge reads a response_too_large refusal; nil for any other text.
func tooLarge(text string) *verify.TooLarge {
	var doc struct {
		Error string `json:"error"`
		Bytes int    `json:"bytes"`
		Limit int    `json:"limit"`
	}
	if err := decode(text, &doc); err != nil || doc.Error != responseTooLarge {
		return nil
	}
	return &verify.TooLarge{Bytes: doc.Bytes, Limit: doc.Limit}
}

// outputOf is mcp-kubernetes's output argument for how much a check reads:
// slim for Readiness — the conditions and the revision, a workload's
// selector, a pod's phase, the keys of a Secret, with a HelmRelease's values
// and history, a workload's long environment and every object's managed
// fields and last-applied configuration dropped —, normal for Configuration,
// the values whole with only the bookkeeping dropped, and full for Manifest,
// the managed fields kept. mcp-kubernetes offers no selection of paths, and
// a whole object (full) is what its response cap refuses for a HelmRelease
// with history; it masks a Secret's values in every output.
func outputOf(shape verify.Shape) string {
	switch shape {
	case verify.Configuration:
		return "normal"
	case verify.Manifest:
		return "full"
	}
	return "slim"
}

// kindArgs are mcp-kubernetes's resourceType and apiGroup for a probe's
// resource (kind, or kind.group).
func kindArgs(resource string) (string, string) {
	kind, group, _ := strings.Cut(resource, ".")
	return strings.ToLower(kind), group
}

// Get reads one object, as much of it as the shape says.
func (k *cluster) Get(ctx context.Context, namespace, resource, name string, shape verify.Shape) (map[string]any, error) {
	kind, group := kindArgs(resource)
	args := map[string]any{"resourceType": kind, "name": name, "output": outputOf(shape)}
	if namespace != "" {
		args["namespace"] = namespace
	}
	if group != "" {
		args["apiGroup"] = group
	}
	text, err := k.call(ctx, opGet, args)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Resource map[string]any `json:"resource"`
	}
	if err := decode(text, &doc); err != nil || doc.Resource == nil {
		return nil, fmt.Errorf("%s answered something other than an object for %s %s/%s: %.200q", k.tool(opGet), resource, namespace, name, text)
	}
	return doc.Resource, nil
}

// List reads the objects a label selector matches, as much of each as the
// shape says.
func (k *cluster) List(ctx context.Context, namespace, resource, labelSelector string, shape verify.Shape) ([]map[string]any, error) {
	kind, group := kindArgs(resource)
	args := map[string]any{"resourceType": kind, "fullOutput": true, "output": outputOf(shape), "limit": 200}
	if namespace != "" {
		args["namespace"] = namespace
	}
	if group != "" {
		args["apiGroup"] = group
	}
	if labelSelector != "" {
		args["labelSelector"] = labelSelector
	}
	text, err := k.call(ctx, opList, args)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Items []map[string]any `json:"items"`
	}
	if err := decode(text, &doc); err != nil {
		return nil, fmt.Errorf("%s answered something other than a list for %s in %s: %.200q", k.tool(opList), resource, namespace, text)
	}
	return doc.Items, nil
}

// Logs reads a pod's log, the last tail lines (mcp-kubernetes takes 1 to 1000).
func (k *cluster) Logs(ctx context.Context, namespace, pod string, tail int) (string, error) {
	return k.call(ctx, opLogs, map[string]any{"namespace": namespace, "podName": pod, "tailLines": tail})
}

// Serves discovers whether the apiserver serves the resource of the group at
// the version: mcp-kubernetes lists the group's resources at their preferred
// version, so a resource served at another version only is not found.
func (k *cluster) Serves(ctx context.Context, group, version, resource string) (bool, error) {
	text, err := k.call(ctx, opAPIResources, map[string]any{"apiGroup": group, "limit": 0})
	if err != nil {
		return false, err
	}
	var doc struct {
		Items []struct {
			Name    string `json:"name"`
			Group   string `json:"group"`
			Version string `json:"version"`
		} `json:"items"`
	}
	if err := decode(text, &doc); err != nil {
		return false, fmt.Errorf("%s answered something other than a resource list for group %q: %.200q", k.tool(opAPIResources), group, text)
	}
	for _, r := range doc.Items {
		if r.Name == resource && r.Group == group && r.Version == version {
			return true, nil
		}
	}
	return false, nil
}

func decode(text string, v any) error {
	return json.Unmarshal([]byte(text), v)
}
