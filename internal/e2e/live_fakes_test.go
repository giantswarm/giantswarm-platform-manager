package e2e

// The fakes of the live tools: the platform's identity provider (a Dex that
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
	"slices"
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

// The persons of the live tools, as the identity provider names them, and
// the two audiences the live tools trust: the platform's own client and the
// one the registration requires of muster.
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
	// memberTemplate names the member that serves an installation, as the
	// platform's agent-platform-mcps chart names the servers.
	memberTemplate = "{{ .Installation }}-mcp-kubernetes"
)

// The kubernetes tools' operations behind the family.
const (
	opGet          = "get"
	opList         = "list"
	opLogs         = "logs"
	opAPIResources = "api_resources"
)

// The output formats of mcp-kubernetes's get and list a read asks for.
const (
	outputSlim   = "slim"
	outputNormal = "normal"
	outputFull   = "full"
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

// fakeInstallation is one installation's objects, pod logs and the APIs its
// apiserver serves.
type fakeInstallation struct {
	mu      sync.Mutex
	objects map[string]map[string]any // kind|namespace|name, kind lowercased
	logs    map[string]string         // namespace/pod
	apis    []apiResource             // what discovery lists
}

// apiResource is one API resource discovery lists, as mcp-kubernetes's
// api_resources answers it.
type apiResource struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Namespaced bool     `json:"namespaced"`
	Verbs      []string `json:"verbs"`
}

func newFakeInstallation() *fakeInstallation {
	return &fakeInstallation{objects: map[string]map[string]any{}, logs: map[string]string{}}
}

// serveAPI makes discovery list the resource of the group at the version.
func (f *fakeInstallation) serveAPI(group, version, resource, kind string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apis = append(f.apis, apiResource{Name: resource, Kind: kind, Group: group, Version: version, Namespaced: true, Verbs: []string{opGet}})
}

// unserveAPI takes the resource out of discovery: the apiserver no longer
// serves it (or does not yet).
func (f *fakeInstallation) unserveAPI(group, resource string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apis = slices.DeleteFunc(f.apis, func(r apiResource) bool { return r.Group == group && r.Name == resource })
}

// apiResources is what discovery lists, the group's only when one is asked.
func (f *fakeInstallation) apiResources(group string) []apiResource {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []apiResource{}
	for _, r := range f.apis {
		if group == "" || r.Group == group {
			out = append(out, r)
		}
	}
	return out
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

// editLogs changes the pods' logs in place.
func (f *fakeInstallation) editLogs(change func(logs map[string]string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.logs)
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
	return map[string]any{statusKey: map[string]any{conditionsKey: []any{map[string]any{typeKey: condition, statusKey: statusTrue, message: "ok"}}}}
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
				bulky(obj)
				f.put("ConfigMap", p.Namespace, "agent-platform-konfiguration", map[string]any{"data": map[string]any{"configmap-values.yaml": valuesFile}})
			case "kagent":
				obj["spec"] = map[string]any{"values": map[string]any{"providers": at("kagent.providers")}}
			}
			f.put(kind, p.Namespace, p.Name, obj)
		case render.Condition:
			f.put(kind, p.Namespace, p.Name, conditionTrue(p.Expect.Condition))
		case render.APIServed:
			resource, group, _ := strings.Cut(p.Resource, ".")
			f.serveAPI(group, p.Expect.Version, resource, "PodCertificateRequest")
		case render.SecretLoaded:
			// The Secret written before the container that reads it started:
			// the container holds its value.
			f.put(kind, p.Namespace, p.Name, map[string]any{metadataKey: map[string]any{managedFieldsKey: []any{dataWrite(dataWritten)}}, dataKey: map[string]any{render.DexSecretKey: "***"}})
			f.put("Pod", p.Namespace, podOf(p), map[string]any{metadataKey: map[string]any{"labels": labelsOf(p.Expect.Pods)}, statusKey: map[string]any{"phase": "Running",
				"containerStatuses": []any{map[string]any{nameKey: p.Expect.Container, "state": map[string]any{"running": map[string]any{startedAtKey: containerStarted}}}}}})
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
				f.logs[p.Namespace+"/"+pod] = busyLog()
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

// When the fake installation's Secrets that a container reads at start were
// written, and when those containers started: after the write.
const (
	dataWritten    = "2026-09-23T16:21:23Z"
	containerStarted = "2026-09-23T16:44:13Z"
	managedFieldsKey = "managedFields"
	dataKey          = "data"
	startedAtKey     = "startedAt"
)

// dataWrite is a managed-fields entry of the manager that wrote a Secret's
// data at the time.
func dataWrite(at string) map[string]any {
	return map[string]any{"manager": "kustomize-controller", "operation": "Apply", "time": at,
		"fieldsV1": map[string]any{"f:data": map[string]any{".": map[string]any{}, "f:secret": map[string]any{}}}}
}

// podOf is the one pod a SecretLoaded probe's selector matches.
func podOf(p render.Probe) string { return p.Expect.Container + "-0" }

// labelsOf are the labels a label selector of k=v terms asks for.
func labelsOf(selector string) map[string]any {
	labels := map[string]any{}
	for _, kv := range strings.Split(selector, ",") {
		k, v, _ := strings.Cut(kv, "=")
		labels[k] = v
	}
	return labels
}

// responseLimit is mcp-kubernetes's cap on a tool's answer (mcp-toolkit's
// responsecap middleware, mounted with its default): a larger answer is
// refused whole, muster relays the refusal.
const responseLimit = 128 * 1024

// lastAppliedAnnotation duplicates the manifest on every object kubectl applied.
const lastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// capped is a tool's result as mcp-kubernetes's response cap relays it: a
// text over the limit becomes the typed response_too_large refusal and the
// result an error, as the middleware phrases it.
func capped(res *mcp.CallToolResult) *mcp.CallToolResult {
	for i, c := range res.Content {
		t, ok := mcp.AsTextContent(c)
		if !ok || len(t.Text) <= responseLimit {
			continue
		}
		payload, err := json.Marshal(map[string]any{"error": "response_too_large", "bytes": len(t.Text), "limit": responseLimit,
			"message": fmt.Sprintf("response is %d bytes, exceeds %d byte limit", len(t.Text), responseLimit),
			"hint":    "narrow the query: tighten filters, reduce the time range, or request fewer items"})
		if err != nil {
			return mcp.NewToolResultError(err.Error())
		}
		res.Content[i] = mcp.NewTextContent(string(payload))
		res.IsError = true
	}
	return res
}

// shaped is an object as mcp-kubernetes answers it for the output asked:
// full and wide as the apiserver holds it; normal without the bookkeeping
// (the managed fields, the last-applied configuration); slim — the tool's
// default — without a HelmRelease's values and history either. A copy: the
// installation's object stays whole.
func shaped(obj map[string]any, output string) map[string]any {
	out := deepCopy(obj)
	if output == outputFull || output == "wide" {
		return out
	}
	if meta, ok := out[metadataKey].(map[string]any); ok {
		delete(meta, "managedFields")
		if annotations, ok := meta["annotations"].(map[string]any); ok {
			delete(annotations, lastAppliedAnnotation)
		}
	}
	if output == outputNormal {
		return out
	}
	if kind, _ := out["kind"].(string); kind == helmReleaseKind {
		if spec, ok := out["spec"].(map[string]any); ok {
			delete(spec, "values")
		}
		if status, ok := out[statusKey].(map[string]any); ok {
			delete(status, "history")
		}
	}
	return out
}

func deepCopy(obj map[string]any) map[string]any {
	b, err := json.Marshal(obj)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// lastLines is a log's last n lines, as the apiserver answers tailLines.
func lastLines(log string, n int) string {
	lines := strings.Split(strings.TrimSuffix(log, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}

// busyLog is the log of an oauth2-proxy that serves traffic: a thousand
// request lines, then the lines its start writes — whole, over the response
// cap; the last two hundred lines, well within it.
func busyLog() string {
	var b strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&b, "10.244.%d.%d - alice@example.test [22/Sep/2026:05:%02d:%02d +0000] kagent.example.test GET - \"/api/agents?namespace=kagent&page=%d\" HTTP/1.1 \"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36\" 200 1532 0.012\n",
			i/256, i%256, i/60%60, i%60, i)
	}
	b.WriteString("level=info msg=\"OAuthProxy configured\"\nlevel=info msg=\"listening on :4180\"\n")
	return b.String()
}

// bulky makes a HelmRelease as large as the platform's meta chart release is
// on the apiserver: the managed fields and the last-applied configuration of
// the size the apiserver keeps for it, and Flux's history — over the response
// cap whole, within it without the bookkeeping.
func bulky(obj map[string]any) {
	fields := strings.Repeat(`{"f:spec":{"f:values":{"f:kagent":{"f:providers":{}}}}},`, 1500)
	obj[metadataKey] = map[string]any{
		"annotations":   map[string]any{lastAppliedAnnotation: `{"apiVersion":"helm.toolkit.fluxcd.io/v2","kind":"HelmRelease","spec":` + fields + `}`},
		"managedFields": []any{map[string]any{"manager": "helm-controller", "operation": "Apply", "fieldsType": "FieldsV1", "fieldsV1": fields}},
	}
	var history []any
	for i := range 10 {
		history = append(history, map[string]any{"chartName": platformRelease, "chartVersion": fmt.Sprintf("4.4%d.0", i), "digest": strings.Repeat("ab", 32), nameKey: platformRelease, "namespace": platformNamespace, statusKey: "superseded", "version": i + 1})
	}
	obj[statusKey].(map[string]any)["history"] = history
}

// musterCall is one kubernetes tool call the fake muster saw, by person.
type musterCall struct {
	Person string
	Tool   string
	Args   map[string]any
}

// fakeMuster is muster's aggregator as the manager's loop-back reaches it:
// an MCP endpoint whose bearer names the person and the installation's
// kubernetes tools under a family behind call_tool — nothing of the
// manager's own: the loop-back reads installations, never GitHub. The admin
// reads everything, the viewer is forbidden Secrets, the stranger is not
// connected to the installation.
type fakeMuster struct {
	*httptest.Server
	// insts are the installations behind the family, by the instance argument.
	insts map[string]*fakeInstallation

	mu    sync.Mutex
	calls []musterCall
}

type personKey struct{}

// authRequiredText is muster's answer for a server the session is not
// connected to, as it phrases it.
const authRequiredText = "auth_required: server '%s' requires authentication before its tools can be called (this session is not authenticated to it).\n\nAuthentication Required\n\nServer: %s\n\nPlease sign in to connect to this server:\n\nhttps://muster.example.test/oauth/proxy/start?state=fixture"

func newFakeMuster(t *testing.T, inst *fakeInstallation, installation string) *fakeMuster {
	t.Helper()
	m := &fakeMuster{insts: map[string]*fakeInstallation{installation: inst}}
	aggregated := map[string]mustertest.Tool{}
	for _, op := range []string{opGet, opList, opLogs, opAPIResources} {
		aggregated["x_"+kubernetesFamily+"_"+op] = m.kubernetes(op)
	}
	bridge := mustertest.Bridge(aggregated)
	m.Server = httptest.NewServer(mcpserver.NewStreamableHTTPServer(bridge, mcpserver.WithEndpointPath("/mcp"),
		mcpserver.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, personKey{}, personOf(bearer(r)))
		})))
	t.Cleanup(m.Close)
	return m
}

// serve adds an installation behind the family.
func (m *fakeMuster) serve(installation string, inst *fakeInstallation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.insts[installation] = inst
}

func (m *fakeMuster) installation(name string) (*fakeInstallation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.insts[name]
	return inst, ok
}

// members are the family's servers, sorted: what muster lists when the
// instance argument names none of them.
func (m *fakeMuster) members() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.insts))
	for name := range m.insts {
		names = append(names, name+"-mcp-kubernetes")
	}
	sort.Strings(names)
	return names
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
		// muster routes a family's tool by the member's server name in the
		// instance argument, never by the installation's name.
		server, _ := args[instanceArg].(string)
		mc := strings.TrimSuffix(server, "-mcp-kubernetes")
		inst, ok := m.installation(mc)
		if !ok || mc == server {
			return mcp.NewToolResultError(fmt.Sprintf("tool x_%s_%s is not available on server %q (available: %s)", kubernetesFamily, op, server, strings.Join(m.members(), ", ")))
		}
		if person == liveStranger {
			return mcp.NewToolResultError(fmt.Sprintf(authRequiredText, server, server))
		}
		if op == opAPIResources {
			group, _ := args["apiGroup"].(string)
			items := inst.apiResources(group)
			return document(map[string]any{"items": items, "totalItems": len(items), "totalCount": len(items), "hasMore": false})
		}
		kind, _ := args["resourceType"].(string)
		namespace, _ := args["namespace"].(string)
		if person == liveViewer && kind == "secret" {
			return mcp.NewToolResultError(fmt.Sprintf(`Failed to %s resource: secrets is forbidden: User "oidc:%s" cannot %s resource "secrets" in API group "" in the namespace "%s" (impersonating user=%s)`, op, person, op, namespace, person))
		}
		output, _ := args["output"].(string)
		switch op {
		case opGet:
			name, _ := args["name"].(string)
			obj, ok := inst.get(kind, namespace, name)
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Failed to get resource: %ss.%s %q not found", kind, args["apiGroup"], name))
			}
			return capped(document(map[string]any{"resource": shaped(obj, output), "_meta": map[string]any{"cluster": mc}}))
		case opList:
			selector, _ := args["labelSelector"].(string)
			items := []map[string]any{}
			for _, obj := range inst.list(kind, namespace, selector) {
				items = append(items, shaped(obj, output))
			}
			return capped(document(map[string]any{"kind": strings.ToUpper(kind[:1]) + kind[1:] + "List", "items": items, "totalItems": len(items)}))
		default:
			pod, _ := args["podName"].(string)
			tail := 100
			if v, ok := args["tailLines"].(float64); ok {
				if v < 1 || v > 1000 {
					return mcp.NewToolResultError("tailLines must be between 1 and 1000")
				}
				tail = int(v)
			}
			inst.mu.Lock()
			defer inst.mu.Unlock()
			log, ok := inst.logs[namespace+"/"+pod]
			if !ok {
				return mcp.NewToolResultError(fmt.Sprintf("Failed to get logs: pods %q not found", pod))
			}
			return capped(mcp.NewToolResultText(lastLines(log, tail)))
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
