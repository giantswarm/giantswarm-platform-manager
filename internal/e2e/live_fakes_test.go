package e2e

// The fakes of the live path: the platform's identity provider (a Dex that
// signs ID tokens and publishes its key set), muster's aggregator with the
// installations' kubernetes tools behind call_tool, and the installation
// itself — its objects built from the definition's own probes, so a render
// and its installation agree unless a test changes one.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster/mustertest"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The keys of the fake installation's objects.
const (
	statusKey     = "status"
	conditionsKey = "conditions"
	metadataKey   = "metadata"
	typeKey       = "type"
)

// The persons of the live path, as the identity provider names them, and
// the two audiences the live surface trusts: the platform's own client and
// the one the live registration requires of muster.
const (
	liveAudience         = "agent-platform"
	liveRequiredAudience = "dex-k8s-authenticator"
	liveAdmin            = "admin@example.test"
	liveViewer           = "viewer@example.test"
	// liveStranger has an account at the hub but none on the installation:
	// muster answers auth_required for its kubernetes server.
	liveStranger = "stranger@example.test"
	// kubernetesFamily is the muster family the fake installation's tools
	// are under, and instanceArg the argument that selects it.
	kubernetesFamily = "kubernetes"
	instanceArg      = "management_cluster"
)

// fakeDex is the platform identity provider: it signs ID tokens for the
// persons and publishes its key set over TLS under its own CA.
type fakeDex struct {
	*httptest.Server
	key    *rsa.PrivateKey
	kid    string
	issuer string
	caFile string
}

func newFakeDex(t *testing.T) *fakeDex {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDex{key: key, kid: "k1"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dex/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: d.kid, Algorithm: string(jose.RS256), Use: "sig"}}})
	})
	mux.HandleFunc("GET /dex/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"issuer": d.issuer, "jwks_uri": d.issuer + "/keys", "authorization_endpoint": d.issuer + "/auth", "token_endpoint": d.issuer + "/token", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	d.Server = httptest.NewTLSServer(mux)
	t.Cleanup(d.Close)
	d.issuer = d.URL + "/dex"
	d.caFile = filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(d.caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

// token is an ID token for email, the shape muster forwards: sub, email, the
// audiences, one hour to live unless told otherwise.
func (d *fakeDex) token(t *testing.T, email string, aud []string, ttl time.Duration) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: d.key}, (&jose.SignerOptions{}).WithHeader("kid", d.kid).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := jwt.Claims{Issuer: d.issuer, Subject: "sub-" + strings.SplitN(email, "@", 2)[0], Audience: aud, IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(ttl))}
	raw, err := jwt.Signed(signer).Claims(claims).Claims(map[string]any{"email": email, "email_verified": true}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// personOf is the email of a bearer the fake Dex issued — the fake muster
// trusts it, the manager under test validated it.
func personOf(bearer string) string {
	parts := strings.Split(bearer, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Email
}

// fakeInstallation is one installation's objects and pod logs.
type fakeInstallation struct {
	mu      sync.Mutex
	objects map[string]map[string]any // kind|namespace|name, kind lowercased
	logs    map[string]string         // namespace/pod
}

func newFakeInstallation() *fakeInstallation {
	return &fakeInstallation{objects: map[string]map[string]any{}, logs: map[string]string{}}
}

func objectKey(kind, namespace, name string) string {
	return strings.ToLower(kind) + "|" + namespace + "|" + name
}

// put stores an object by kind, namespace and name, merging conditions onto
// an object already there (several probes name one object).
func (f *fakeInstallation) put(kind, namespace, name string, obj map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := objectKey(kind, namespace, name)
	meta := map[string]any{nameKey: name}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	if existing, ok := f.objects[key]; ok {
		if m, ok := existing[metadataKey].(map[string]any); ok {
			for k, v := range m {
				meta[k] = v
			}
		}
		existing = mergeMaps(existing, obj)
		existing[metadataKey] = meta
		f.objects[key] = existing
		return
	}
	obj[metadataKey] = mergeMaps(meta, obj[metadataKey])
	obj["kind"] = kind
	f.objects[key] = obj
}

// mergeMaps merges over into base (conditions appended, maps merged).
func mergeMaps(base map[string]any, over any) map[string]any {
	om, ok := over.(map[string]any)
	if !ok {
		return base
	}
	if base == nil {
		base = map[string]any{}
	}
	for k, v := range om {
		if k == conditionsKey {
			bl, _ := base[k].([]any)
			ol, _ := v.([]any)
			base[k] = append(bl, ol...)
			continue
		}
		if vm, ok := v.(map[string]any); ok {
			bm, _ := base[k].(map[string]any)
			base[k] = mergeMaps(bm, vm)
			continue
		}
		base[k] = v
	}
	return base
}

// edit changes one object in place.
func (f *fakeInstallation) edit(kind, namespace, name string, change func(obj map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if obj, ok := f.objects[objectKey(kind, namespace, name)]; ok {
		change(obj)
	}
}

func (f *fakeInstallation) get(kind, namespace, name string) (map[string]any, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.objects[objectKey(kind, namespace, name)]
	return obj, ok
}

func (f *fakeInstallation) list(kind, namespace, selector string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for key, obj := range f.objects {
		if !strings.HasPrefix(key, strings.ToLower(kind)+"|"+namespace+"|") {
			continue
		}
		if selector != "" && !matches(obj, selector) {
			continue
		}
		out = append(out, obj)
	}
	sort.Slice(out, func(i, j int) bool { return fmt.Sprint(out[i][metadataKey]) < fmt.Sprint(out[j][metadataKey]) })
	return out
}

func matches(obj map[string]any, selector string) bool {
	labels, _ := obj[metadataKey].(map[string]any)["labels"].(map[string]any)
	for _, kv := range strings.Split(selector, ",") {
		k, v, _ := strings.Cut(kv, "=")
		if fmt.Sprint(labels[k]) != v {
			return false
		}
	}
	return true
}

func conditionTrue(condition string) map[string]any {
	return map[string]any{statusKey: map[string]any{conditionsKey: []any{map[string]any{typeKey: condition, statusKey: "True", message: "ok"}}}}
}

// populate fills the installation as the definition rendered it: an object
// per probe, healthy, and the values the drift probes read equal to the
// rendered values file.
func (f *fakeInstallation) populate(t *testing.T, installation string, res *render.Result, valuesFile string) {
	t.Helper()
	var values map[string]any
	if err := yaml.Unmarshal([]byte(valuesFile), &values); err != nil {
		t.Fatal(err)
	}
	at := func(path string) any {
		var v any = values
		for _, k := range strings.Split(path, ".") {
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		}
		return v
	}
	for _, p := range res.Probes {
		kind, _, _ := strings.Cut(p.Resource, ".")
		switch p.Kind {
		case render.HelmReleaseReady:
			obj := conditionTrue("Ready")
			switch p.Name {
			case "agent-platform":
				obj["spec"] = map[string]any{"valuesFrom": []any{map[string]any{"kind": "ConfigMap", "name": "agent-platform-konfiguration", "valuesKey": "configmap-values.yaml"}},
					"values": map[string]any{"gitops": map[string]any{"namespace": p.Namespace}}}
				f.put("ConfigMap", p.Namespace, "agent-platform-konfiguration", map[string]any{"data": map[string]any{"configmap-values.yaml": valuesFile}})
			case "kagent":
				obj["spec"] = map[string]any{"values": map[string]any{"providers": at("kagent.providers")}}
			}
			f.put(kind, p.Namespace, p.Name, obj)
		case render.Condition:
			f.put(kind, p.Namespace, p.Name, conditionTrue(p.Expect.Condition))
		case render.ResourcePresent:
			obj := map[string]any{}
			if len(p.Expect.Keys) > 0 {
				data := map[string]any{}
				for _, k := range p.Expect.Keys {
					data[k] = "***"
				}
				obj["data"] = data
			}
			f.put(kind, p.Namespace, p.Name, obj)
		case render.LogAbsent, render.Drift:
			if kind != "Deployment" && kind != "ConfigMap" {
				continue
			}
			if kind == "Deployment" {
				pod := p.Name + "-0"
				f.put("Deployment", p.Namespace, p.Name, map[string]any{"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": p.Name}},
					"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": p.Name,
						"args": []any{"--provider=oidc", "--oidc-extra-audience=" + fmt.Sprint(at("kagent.oauth2-proxy.extraArgs.oidc-extra-audience"))}}}}}}})
				f.put("Pod", p.Namespace, pod, map[string]any{metadataKey: map[string]any{"labels": map[string]any{"app": p.Name}}, statusKey: map[string]any{"phase": "Running"}})
				f.logs[p.Namespace+"/"+pod] = "level=info msg=\"OAuthProxy configured\"\nlevel=info msg=\"listening on :4180\"\n"
				continue
			}
			server := at("muster.muster.oauth.server")
			config, err := yaml.Marshal(map[string]any{"aggregator": map[string]any{"oauth": map[string]any{"server": server}}})
			if err != nil {
				t.Fatal(err)
			}
			f.put("ConfigMap", p.Namespace, p.Name, map[string]any{"data": map[string]any{"config.yaml": string(config)}})
		}
	}
	_ = installation
}

// musterCall is one kubernetes tool call the fake muster saw, by person.
type musterCall struct {
	Person string
	Tool   string
	Args   map[string]any
}

// fakeMuster is muster's aggregator as the manager's loop-back reaches it:
// an MCP endpoint whose bearer names the person, the installation's
// kubernetes tools under a family behind call_tool. The admin reads
// everything, the viewer is forbidden Secrets, the stranger is not connected
// to the installation.
type fakeMuster struct {
	*httptest.Server
	inst         *fakeInstallation
	installation string

	mu    sync.Mutex
	calls []musterCall
}

type personKey struct{}

// authRequiredText is muster's answer for a server the session is not
// connected to, as it phrases it.
const authRequiredText = "auth_required: server '%s' requires authentication before its tools can be called (this session is not authenticated to it).\n\nAuthentication Required\n\nServer: %s\n\nPlease sign in to connect to this server:\n\nhttps://muster.example.test/oauth/proxy/start?state=fixture"

func newFakeMuster(t *testing.T, inst *fakeInstallation, installation string) *fakeMuster {
	t.Helper()
	m := &fakeMuster{inst: inst, installation: installation}
	tools := map[string]mustertest.Tool{}
	for _, op := range []string{"get", "list", "logs"} {
		tools["x_"+kubernetesFamily+"_"+op] = m.kubernetes(op)
	}
	bridge := mustertest.Bridge(tools)
	m.Server = httptest.NewServer(mcpserver.NewStreamableHTTPServer(bridge, mcpserver.WithEndpointPath("/mcp"),
		mcpserver.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, personKey{}, personOf(bearer(r)))
		})))
	t.Cleanup(m.Close)
	return m
}

// seen are the calls by person, oldest first.
func (m *fakeMuster) seen() []musterCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]musterCall{}, m.calls...)
}

func (m *fakeMuster) kubernetes(op string) mustertest.Tool {
	return func(ctx context.Context, args map[string]any) *mcp.CallToolResult {
		person, _ := ctx.Value(personKey{}).(string)
		m.mu.Lock()
		m.calls = append(m.calls, musterCall{Person: person, Tool: op, Args: args})
		m.mu.Unlock()
		if person == "" {
			return mcp.NewToolResultError("no bearer: the loop-back carried no token")
		}
		if mc, _ := args[instanceArg].(string); mc != m.installation {
			return mcp.NewToolResultError(fmt.Sprintf("no member of family %s serves %s=%q", kubernetesFamily, instanceArg, mc))
		}
		server := m.installation + "-mcp-kubernetes"
		if person == liveStranger {
			return mcp.NewToolResultError(fmt.Sprintf(authRequiredText, server, server))
		}
		kind, _ := args["resourceType"].(string)
		namespace, _ := args["namespace"].(string)
		if person == liveViewer && kind == "secret" {
			return mcp.NewToolResultError(fmt.Sprintf(`Failed to %s resource: secrets is forbidden: User "oidc:%s" cannot %s resource "secrets" in API group "" in the namespace "%s" (impersonating user=%s)`, op, person, op, namespace, person))
		}
		switch op {
		case "get":
			name, _ := args["name"].(string)
			obj, ok := m.inst.get(kind, namespace, name)
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Failed to get resource: %ss.%s %q not found", kind, args["apiGroup"], name))
			}
			return document(map[string]any{"resource": obj, "_meta": map[string]any{"cluster": m.installation}})
		case "list":
			selector, _ := args["labelSelector"].(string)
			items := m.inst.list(kind, namespace, selector)
			return document(map[string]any{"kind": strings.ToUpper(kind[:1]) + kind[1:] + "List", "items": items, "totalItems": len(items)})
		default:
			pod, _ := args["podName"].(string)
			m.inst.mu.Lock()
			defer m.inst.mu.Unlock()
			log, ok := m.inst.logs[namespace+"/"+pod]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Failed to get logs: pods %q not found", pod))
			}
			return mcp.NewToolResultText(log)
		}
	}
}

func document(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error())
	}
	return mcp.NewToolResultText(string(b))
}
