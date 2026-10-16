package agentplatform

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The confidential-client shape's registrations: a client registered anew,
// one registered by hand whose Secret is on record under mcpclients/, and a
// public one.
const (
	freshClient   = "production-gateway"
	adoptedClient = "staging-gateway"
	publicURI     = "https://agents.runtime.example/identities/oauth2/callback/00000000-0000-4000-8000-00000000000a"
	freshURI      = "https://gateway.runtime.example/oauth2/callback/00000000-0000-4000-8000-00000000000b"
	adoptedURI    = "https://gateway.runtime.example/oauth2/callback/00000000-0000-4000-8000-00000000000c"
)

func renderConfidential(t *testing.T, mutate func(installation map[string]any)) (*render.Result, error) {
	t.Helper()
	input, secrets := loadInput(t, shapeConfidentialClient)
	if mutate != nil {
		mutate(input["installation"].(map[string]any))
	}
	return Render(input, secrets, render.ModeCommit)
}

func treeFile(t *testing.T, r *render.Result, suffix string) string {
	t.Helper()
	for name, content := range r.Tree() {
		if strings.HasSuffix(filepathSlash(name), suffix) {
			return string(content)
		}
	}
	return ""
}

func filepathSlash(name string) string { return strings.ReplaceAll(name, "\\", "/") }

// A confidential registration renders its Dex client with the referenced
// Secret, the Secret with a generated value under extras/agent-platform/secrets
// listed by the Secrets' kustomization, and its id among muster's audiences —
// not the kagent UI's — and no public registration; the public client renders
// its redirect URI on muster's allowlist alone.
func TestRenderConfidentialClient(t *testing.T) {
	r, err := renderConfidential(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	dex := treeFile(t, r, "/apps/dex-app/configmap-values.yaml.patch")
	for _, want := range []string{
		"    - id: " + freshClient + "\n      name: " + freshClient + "\n      redirectURIs:\n        - " + freshURI + "\n      secretRef:\n        name: dex-client-" + freshClient + "\n        key: secret\n",
		"    - id: " + adoptedClient + "\n      name: " + adoptedClient + "\n      redirectURIs:\n        - " + adoptedURI + "\n      secretRef:\n        name: dex-client-" + adoptedClient + "\n        key: secret\n",
	} {
		if !strings.Contains(dex, want) {
			t.Errorf("the dex patch lacks\n%s\nin\n%s", want, dex)
		}
	}
	if strings.Contains(dex, publicURI) {
		t.Errorf("the public client is a Dex client:\n%s", dex)
	}

	secret := treeFile(t, r, "/extras/agent-platform/secrets/dex-client-"+freshClient+"-secret.yaml")
	if !strings.Contains(secret, "name: dex-client-"+freshClient+"\n") || !strings.Contains(secret, "namespace: "+dexNamespace+"\n") || !strings.Contains(secret, "secret: "+render.Placeholder("heron-"+freshClient+"-dex-client-secret")) {
		t.Errorf("the client's Secret:\n%s", secret)
	}
	if k := treeFile(t, r, "/extras/agent-platform/secrets/kustomization.yaml"); !strings.Contains(k, "dex-client-"+freshClient+"-secret.yaml") || strings.Contains(k, adoptedClient) {
		t.Errorf("the Secrets' kustomization:\n%s", k)
	}

	patch := treeFile(t, r, "/apps/agent-platform/configmap-values.yaml.patch")
	if !strings.Contains(patch, "trustedAudiences:\n          - dex-k8s-authenticator\n          - kagent\n          - "+freshClient+"\n          - "+adoptedClient+"\n") {
		t.Errorf("muster's audiences:\n%s", patch)
	}
	if !strings.Contains(patch, "trustedPublicRegistrationRedirectURIs:\n          - "+publicURI+"\n") || strings.Contains(patch, freshURI) || strings.Contains(patch, adoptedURI) {
		t.Errorf("muster's public registrations:\n%s", patch)
	}
	if !strings.Contains(patch, "oidc-extra-audience: dex-k8s-authenticator,kagent ") {
		t.Errorf("the kagent UI trusts a confidential client:\n%s", patch)
	}
}

// A client registered by hand, its Secret on record under mcpclients/, is
// adopted in place: the render writes the Secret in that file with the same
// generated name, so the value on record stands, and renders no second
// Secret under extras/agent-platform/secrets.
func TestRenderAdoptsAHandRegisteredClientsSecret(t *testing.T) {
	r, err := renderConfidential(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	adopted := treeFile(t, r, "/extras/agent-platform/mcpclients/dex-client-"+adoptedClient+".yaml")
	if !strings.Contains(adopted, "name: dex-client-"+adoptedClient+"\n") || !strings.Contains(adopted, "secret: "+render.Placeholder("heron-"+adoptedClient+"-dex-client-secret")) {
		t.Errorf("the adopted Secret:\n%s", adopted)
	}
	if s := treeFile(t, r, "/extras/agent-platform/secrets/dex-client-"+adoptedClient+"-secret.yaml"); s != "" {
		t.Errorf("the adopted client has a second Secret:\n%s", s)
	}
	if k := treeFile(t, r, "/extras/agent-platform/mcpclients/kustomization.yaml"); k != "" {
		t.Errorf("the registrations' kustomization is the installation's, rendered:\n%s", k)
	}
}

// verify holds the live Dex against every confidential client — Dex knows
// the client at each of its redirect URIs and holds its current secret — and
// muster's ConfigMap against the rendered audiences, the clients' among them.
func TestConfidentialClientProbes(t *testing.T) {
	r, err := renderConfidential(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ id, uri string }{{freshClient, freshURI}, {adoptedClient, adoptedURI}} {
		auth := slices.ContainsFunc(r.Probes, func(p render.Probe) bool {
			return p.ID == "live-dex-auth-per-client" && strings.Contains(p.URL, "client_id="+c.id+"&redirect_uri="+c.uri+"&")
		})
		loaded := slices.ContainsFunc(r.Probes, func(p render.Probe) bool {
			return p.ID == dexSecretsLoadedDimension && p.Name == "dex-client-"+c.id && p.Namespace == dexNamespace
		})
		if !auth || !loaded {
			t.Errorf("%s: auth probe %v, secret loaded probe %v", c.id, auth, loaded)
		}
	}
	if !slices.ContainsFunc(r.Probes, func(p render.Probe) bool {
		return p.ID == "live-muster-trusted-audiences" && len(p.Expect.Compare) == 1 && p.Expect.Compare[0].Rendered == "muster.muster.oauth.server.trustedAudiences"
	}) {
		t.Error("no probe holds muster's ConfigMap against the rendered audiences")
	}
	if slices.ContainsFunc(r.Probes, func(p render.Probe) bool { return strings.Contains(p.URL, publicURI) }) {
		t.Error("the public client is probed as a Dex client")
	}
}

// A confidential client under an id the platform renders a Dex client for
// itself is refused, naming the registration.
func TestConfidentialClientUnderAPlatformIDIsRefused(t *testing.T) {
	for _, id := range []string{"kagent", "muster", authenticatorClient, render.PortalDexClientID, "00000000-0000-4000-8000-000000000003"} {
		_, err := renderConfidential(t, func(inst map[string]any) {
			clients := inst["mcpClients"].([]any)
			clients[1].(map[string]any)["clientId"] = id
		})
		var refusal *render.Refusal
		if !errors.As(err, &refusal) || !strings.Contains(err.Error(), freshClient) || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: %v", id, err)
		}
	}
}
