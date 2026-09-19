package e2e

// verify_installation on the live path: the person's ID token as the bearer,
// the installation read through the fake muster as the person, the three
// marks over the live dimensions, the forbidden and the not-connected
// results, waiting for the customer, the feed into list_installations.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// The objects of the live dimensions the tests change.
const (
	platformNamespace = "agent-platform"
	kagentNamespace   = "kagent"
	fluxNamespace     = "flux-giantswarm"
	musterConfigMap   = "muster-config"
	konfiguration     = "agent-platform-konfiguration"
	valuesFileKey     = "configmap-values.yaml"
	liveAction        = "enable-rowan-live"
)

// enableRowanLive renders rowan from inputs, puts the files into the
// registry's repositories, seeds the Action with the merged inputs per
// installation and a final result — an enablement that rolled out — and
// populates the fake installation from the same render.
func enableRowanLive(t *testing.T, st *stack, c *client.Client, inputs map[string]any) plan.Installation {
	t.Helper()
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr {
		t.Fatalf("dry run: %s", text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" {
		t.Fatalf("refused: %s", p.Refused)
	}
	files := map[string]map[string]string{}
	valuesFile := ""
	for _, f := range p.Files {
		if files[f.Repository] == nil {
			files[f.Repository] = map[string]string{}
		}
		files[f.Repository][f.Path] = f.Content
		if strings.HasSuffix(f.Path, "/apps/"+installations.AgentPlatform+"/"+valuesFileKey+".patch") {
			valuesFile = f.Content
		}
	}
	for repo, fs := range files {
		st.ghs.addFiles(repo, fs)
	}
	def, _ := installations.FindCapability(installations.AgentPlatform)
	in, err := def.Parse(p.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	res, err := def.Render(p.Inputs, in.SuppliedMarkers())
	if err != nil {
		t.Fatal(err)
	}
	st.inst.populate(t, rowan, res, valuesFile)
	now := time.Date(2026, 9, 19, 8, 0, 0, 0, time.UTC)
	seeded := actions.Action{Name: liveAction, Namespace: actionsNamespace, CreatedAt: now,
		Spec: actions.Spec{Actor: actions.Actor{Login: alice}, Capability: installations.AgentPlatform, Installations: []string{rowan}, Kind: actions.KindEnable,
			Inputs: inputs, InputsByInstallation: map[string]map[string]any{rowan: p.Inputs}},
		Status: actions.Status{State: actions.StateEnabled, Result: &actions.Result{State: actions.StateEnabled, Message: "rolled out", At: &now}}}
	if _, err := st.dyn.Resource(actions.GVR).Namespace(actionsNamespace).Create(context.Background(), actions.Unstructured(seeded), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return p
}

func verifyLive(t *testing.T, c *client.Client, name string) verify.Result {
	t.Helper()
	text, isErr := call(t, c, tools.ToolVerifyInstallation, map[string]any{tools.ArgInstallation: name})
	if isErr {
		t.Fatalf("%s %s: %s", tools.ToolVerifyInstallation, name, text)
	}
	var out verify.Result
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	return out
}

// liveDimensions are the result's live dimensions by id.
func liveDimensions(res verify.Result) map[string]verify.Dimension {
	out := map[string]verify.Dimension{}
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Kind == definitions.KindLive {
				out[d.ID] = d
			}
		}
	}
	return out
}

// rowanState is rowan's agent-platform state as list_installations answers alice.
func rowanState(t *testing.T, c *client.Client) installations.State {
	t.Helper()
	text, isErr := call(t, c, tools.ToolListInstallations, map[string]any{tools.ArgInstallations: []string{rowan}})
	if isErr {
		t.Fatalf("list_installations: %s", text)
	}
	var out tools.ListInstallationsResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	for _, r := range out.Installations {
		if r.Name == rowan {
			for _, cs := range r.Capabilities {
				if cs.Name == installations.AgentPlatform {
					return cs.State
				}
			}
		}
	}
	t.Fatalf("rowan is not in the answer: %s", text)
	return ""
}

func recordedProbes(t *testing.T, st *stack) map[string]actions.Probe {
	t.Helper()
	a, err := actions.New(st.dyn, actionsNamespace).Get(context.Background(), liveAction)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]actions.Probe{}
	for _, p := range a.Status.Probes {
		out[p.ID] = p
	}
	return out
}

// The live path takes the ID token muster forwards and nothing else: no
// bearer, a GitHub token, a token for another audience and an expired one
// are refused with the challenge, before any tool runs.
func TestLivePathRefusesTokensItCannotVerify(t *testing.T) {
	st := newStack(t)
	post := func(bearer string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, st.srv.URL+livePath, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	for name, bearer := range map[string]string{
		"no bearer":      "",
		"a GitHub token": aliceToken,
		"other audience": st.dex.token(t, liveAdmin, []string{"other-client"}, time.Hour),
		"expired":        st.dex.token(t, liveAdmin, []string{liveAudience}, -time.Hour),
	} {
		resp := post(bearer)
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), `realm="giantswarm-platform-manager-live"`) {
			t.Errorf("%s: %d %q", name, resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	}
	if resp := post(st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)); resp.StatusCode != http.StatusOK {
		t.Errorf("the forwarded token: %d", resp.StatusCode)
	}
	if len(st.muster.seen()) != 0 {
		t.Errorf("nothing reached muster: %+v", st.muster.seen())
	}
}

// The installation runs as rendered: every live dimension as defined, every
// read at muster as the person with the installation selected, the
// repository dimensions left to verify_capability, the result recorded on
// the action.
func TestVerifyInstallationAsDefined(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())
	admin := st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)

	res := verifyLive(t, st.liveClient(t, admin), rowan)
	if res.Caller != liveAdmin || res.Inputs.Source != "action "+liveAction || res.State != installations.StateEnabled || res.Refused != "" {
		t.Errorf("caller %q inputs %q state %q refused %q", res.Caller, res.Inputs.Source, res.State, res.Refused)
	}
	dims := liveDimensions(res)
	if len(dims) == 0 {
		t.Fatal("no live dimensions")
	}
	for id, d := range dims {
		// rowan pins no login connector: the render carries no connectorId
		// to hold the live one against, and the verify says so.
		if id == "live-muster-connector-and-client-id" {
			if d.Mark != verify.NotChecked || !strings.Contains(d.Reason, "the render carries no value at muster.muster.oauth.server.dex.connectorId") {
				t.Errorf("%s: %s (%s)", id, d.Mark, d.Reason)
			}
			continue
		}
		if d.Mark != verify.AsDefined {
			t.Errorf("%s: %s (%s) %+v", id, d.Mark, d.Reason, d.Live)
		}
	}
	if d := dims["live-drift"]; d.Live == nil || len(d.Live.Checks) != 1 || d.Live.Checks[0].Resource != "HelmRelease" || len(d.Differences) != 0 {
		t.Errorf("live-drift: %+v", d)
	}
	if d := dims["live-oauth2-proxy-audience"]; d.Live == nil || !strings.Contains(d.Live.Checks[0].Message, "pod log") {
		t.Errorf("live-oauth2-proxy-audience read the logs: %+v", d)
	}
	if d := dims["live-kagent-login-redirect"]; d.Live == nil || d.Live.Checks[0].Message != "302" {
		t.Errorf("the HTTP probe ran direct: %+v", d)
	}
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Kind != definitions.KindLive && (d.Mark != verify.NotChecked || d.Reason != verify.ReasonRepositorySide) {
				t.Errorf("%s/%s is verify_capability's: %+v", f.ID, d.ID, d)
			}
		}
	}
	calls := st.muster.seen()
	if len(calls) == 0 {
		t.Fatal("no read reached muster")
	}
	for _, c := range calls {
		if c.Person != liveAdmin || c.Args[instanceArg] != rowan {
			t.Errorf("a read not as the person on the installation: %+v", c)
		}
	}
	probes := recordedProbes(t, st)
	if p := probes["live-helmreleases-ready"]; p.Result != string(verify.AsDefined) || p.Installation != rowan {
		t.Errorf("recorded: %+v (%d entries)", p, len(probes))
	}
	if s := rowanState(t, st.mcpClient(t, aliceToken)); s != installations.StateEnabled {
		t.Errorf("state %q", s)
	}
}

// A live value edited by hand is drift: the dimension names the object and
// the path, the state is drifted, and list_installations says so from the
// record — until a verify finds it as defined again.
func TestVerifyInstallationDriftedNamesTheObject(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	alice := st.mcpClient(t, aliceToken)
	enableRowanLive(t, st, alice, kagentEnabled())
	st.inst.edit("ConfigMap", platformNamespace, musterConfigMap, func(obj map[string]any) {
		data := obj["data"].(map[string]any)
		data["config.yaml"] = strings.Replace(data["config.yaml"].(string), "trustedAudiences:\n", "trustedAudiences:\n    - hand-edited\n", 1)
	})
	admin := st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour))

	res := verifyLive(t, admin, rowan)
	if res.State != installations.StateDrifted || res.Summary[verify.Drifted] != 1 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	d := liveDimensions(res)["live-muster-trusted-audiences"]
	if d.Mark != verify.Drifted || len(d.Differences) == 0 || d.Differences[0].Object != "ConfigMap "+platformNamespace+"/"+musterConfigMap ||
		!strings.HasPrefix(d.Differences[0].Path, "muster.muster.oauth.server.trustedAudiences") || d.Differences[0].Input != "" {
		t.Errorf("live-muster-trusted-audiences: %+v", d)
	}
	if other := liveDimensions(res)["live-drift"]; other.Mark != verify.AsDefined {
		t.Errorf("the other live dimensions stand: %+v", other)
	}
	if s := rowanState(t, alice); s != installations.StateDrifted {
		t.Errorf("list_installations after the verify: %q", s)
	}
	if p := recordedProbes(t, st)["live-muster-trusted-audiences"]; p.Result != string(verify.Drifted) || !strings.Contains(p.Message, musterConfigMap) {
		t.Errorf("recorded: %+v", p)
	}

	st.inst.edit("ConfigMap", platformNamespace, musterConfigMap, func(obj map[string]any) {
		data := obj["data"].(map[string]any)
		data["config.yaml"] = strings.Replace(data["config.yaml"].(string), "    - hand-edited\n", "", 1)
	})
	if res := verifyLive(t, admin, rowan); res.State != installations.StateEnabled {
		t.Errorf("back as defined: %q %v", res.State, res.Summary)
	}
	if s := rowanState(t, alice); s != installations.StateEnabled {
		t.Errorf("list_installations back to the action's result: %q", s)
	}
}

// The live values express another value of an input than the one on record:
// the difference names the input, the dimension differs by input, nothing
// drifted.
func TestVerifyInstallationDiffersByInput(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())
	st.inst.edit("ConfigMap", fluxNamespace, konfiguration, func(obj map[string]any) {
		data := obj["data"].(map[string]any)
		data[valuesFileKey] = strings.Replace(data[valuesFileKey].(string), "  kagent:\n    enabled: true\n", "  kagent:\n    enabled: false\n", 1)
	})

	res := verifyLive(t, st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)), rowan)
	d := liveDimensions(res)["live-drift"]
	if d.Mark != verify.DiffersByInput || len(d.Differences) != 1 || d.Differences[0].Input != kagentKey+"."+enabledKey || d.Differences[0].Path != "components.kagent.enabled" {
		t.Fatalf("live-drift: %+v", d)
	}
	if res.Summary[verify.Drifted] != 0 || res.State != installations.StateEnabled {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
}

// An object the person may not read is not checked, forbidden for them by
// name — the installation is not blamed, the other reads stand.
func TestVerifyInstallationForbiddenForTheViewer(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())

	res := verifyLive(t, st.liveClient(t, st.dex.token(t, liveViewer, []string{liveAudience}, time.Hour)), rowan)
	dims := liveDimensions(res)
	d := dims["live-oauth2-proxy-secret-and-flux-sa"]
	if d.Mark != verify.NotChecked || !strings.Contains(d.Reason, "forbidden for oidc:"+liveViewer) || d.Live == nil {
		t.Fatalf("the Secret read: %+v", d)
	}
	seen := map[string]verify.Mark{}
	for _, c := range d.Live.Checks {
		seen[c.Resource] = c.Mark
	}
	if seen["Secret"] != verify.NotChecked || seen["ServiceAccount"] != verify.AsDefined {
		t.Errorf("checks: %+v", d.Live.Checks)
	}
	if res.Summary[verify.Drifted] != 0 || dims["live-helmreleases-ready"].Mark != verify.AsDefined || res.State != installations.StateEnabled {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	if res.Caller != liveViewer {
		t.Errorf("caller %q", res.Caller)
	}
}

// A person not connected to the installation in muster gets muster's own
// answer, relayed inside the feature: the server, the sign-in; the probes
// that carry no identity still run.
func TestVerifyInstallationRelaysAuthRequired(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())

	res := verifyLive(t, st.liveClient(t, st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour)), rowan)
	dims := liveDimensions(res)
	d := dims["live-helmreleases-ready"]
	if d.Mark != verify.NotChecked || d.Live == nil || d.Live.AuthRequired == nil || d.Live.AuthRequired.Server != rowan+"-mcp-kubernetes" || !strings.HasPrefix(d.Live.AuthRequired.URL, "https://muster.example.test/oauth/proxy/start") {
		t.Fatalf("live-helmreleases-ready: %+v", d)
	}
	if !strings.Contains(d.Reason, "not connected to the installation in muster") {
		t.Errorf("reason %q", d.Reason)
	}
	if h := dims["live-kagent-api-protected"]; h.Mark != verify.AsDefined {
		t.Errorf("the anonymous probe still ran: %+v", h)
	}
	if res.Summary[verify.Drifted] != 0 || res.State != installations.StateEnabled {
		t.Errorf("state %q summary %v: not connected is not drift", res.State, res.Summary)
	}
}

// The model key is the customer's to provide: while the default ModelConfig
// is not Accepted and nothing else is off, the state is waiting for the
// customer, on the record too.
func TestVerifyInstallationWaitingForTheCustomer(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	alice := st.mcpClient(t, aliceToken)
	enableRowanLive(t, st, alice, minimalInputs(map[string]any{kagentKey: map[string]any{enabledKey: true, "modelKeySecret": "customer-provided"}}))
	st.inst.edit("ModelConfig", kagentNamespace, "default-model-config", func(obj map[string]any) {
		obj[statusKey] = map[string]any{conditionsKey: []any{map[string]any{typeKey: "Accepted", statusKey: "False", message: "secret kagent-anthropic-key not found"}}}
	})

	res := verifyLive(t, st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)), rowan)
	d := liveDimensions(res)["live-model-configs"]
	if d.Mark != verify.Drifted || d.Live == nil || !strings.Contains(d.Live.Checks[0].Message, "Accepted=False") || !strings.Contains(d.Live.Checks[0].Note, "waiting for the customer") {
		t.Fatalf("live-model-configs: %+v", d)
	}
	if res.State != installations.StateWaitingForCustomer || res.Summary[verify.Drifted] != 1 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	if s := rowanState(t, alice); s != installations.StateWaitingForCustomer {
		t.Errorf("list_installations: %q", s)
	}
}

// An installation no action has rendered: every live dimension is not
// checked and says there are no inputs on record; nothing is read.
func TestVerifyInstallationWithoutInputsOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)

	res := verifyLive(t, st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)), alder)
	if res.Inputs.Source != tools.InputsNone {
		t.Errorf("inputs %q", res.Inputs.Source)
	}
	for id, d := range liveDimensions(res) {
		if d.Mark != verify.NotChecked || d.Reason != verify.ReasonNoInputs {
			t.Errorf("%s: %+v", id, d)
		}
	}
	if len(st.muster.seen()) != 0 {
		t.Errorf("reads without inputs: %+v", st.muster.seen())
	}
}

// get_info on the App-pinned path reports the live surface.
func TestGetInfoReportsTheLiveSurface(t *testing.T) {
	st := newStack(t)
	text, isErr := call(t, st.mcpClient(t, aliceToken), tools.ToolGetInfo, nil)
	if isErr {
		t.Fatal(text)
	}
	var info tools.Info
	if err := json.Unmarshal([]byte(text), &info); err != nil {
		t.Fatal(err)
	}
	if !info.Live.Configured || info.Live.ToolPrefix != tools.LiveToolPrefix || info.Live.Tool != tools.ToolVerifyInstallation || info.Live.Issuer != st.dex.issuer || info.Live.KubernetesFamily != kubernetesFamily {
		t.Errorf("live: %+v", info.Live)
	}
}
