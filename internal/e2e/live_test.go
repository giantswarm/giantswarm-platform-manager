package e2e

// verify_installation on the one registration: the person's App user token
// as the bearer and their ID token in X-Muster-Id-Token, the installation
// read through the fake muster as the person, the three
// marks over the live dimensions, the forbidden and the not-connected
// results, waiting for the customer, the feed into list_installations.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/render"
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
	res, err := def.Render(p.Inputs, in.SuppliedMarkers(), render.ModeCompare)
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

// The live tools read as the person the forwarded identity names and no
// one else: a call with the App user token alone, or with an ID token for
// another audience or an expired one in X-Muster-Id-Token, is refused by the
// tool, naming the header and muster's auth.forwardIdentity, before anything
// reaches muster — while the GitHub tools answer on the same session, with
// the App token alone. An ID token for any of the trusted audiences — the
// platform's client, the audience the registration requires — is taken.
func TestLiveToolsRefuseAnIdentityTheyCannotVerify(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	withIdentity := func(idToken string) *client.Client {
		return st.client(t, aliceToken, idToken)
	}
	for name, c := range map[string]struct {
		session *client.Client
		want    string
	}{
		"no identity":    {st.mcpClient(t, aliceToken), "the call carried no " + identity.ForwardedIdentityHeader + " header"},
		"a GitHub token": {withIdentity(aliceToken), "the " + identity.ForwardedIdentityHeader + " header was not accepted"},
		// The token check's own words: the token's audiences and the
		// trusted list, so a hub's operator sees which client the person
		// signed in with.
		"other audience": {withIdentity(st.dex.token(t, liveAdmin, []string{"other-client"}, time.Hour)), "audience mismatch: token audiences [other-client] not in trusted [" + liveAudience + " " + liveRequiredAudience + "]"},
		"expired":        {withIdentity(st.dex.token(t, liveAdmin, []string{liveAudience}, -time.Hour)), "the " + identity.ForwardedIdentityHeader + " header was not accepted"},
	} {
		for tool, args := range map[string]map[string]any{
			tools.ToolVerifyInstallation: {tools.ArgInstallation: rowan},
			tools.ToolWatchAction:        {tools.ArgAction: liveAction},
		} {
			text, isErr := call(t, c.session, tool, args)
			if !isErr || !strings.Contains(text, c.want) || !strings.Contains(text, "auth.forwardIdentity") {
				t.Errorf("%s, %s: %v %s", name, tool, isErr, text)
			}
		}
		if text, isErr := call(t, c.session, tools.ToolListInstallations, map[string]any{tools.ArgInstallations: []string{rowan}}); isErr {
			t.Errorf("%s: list_installations with the App token: %s", name, text)
		}
	}
	for name, aud := range map[string]string{"the platform client": liveAudience, "the required audience": liveRequiredAudience} {
		info := getInfo(t, withIdentity(st.dex.token(t, liveAdmin, []string{aud}, time.Hour)))
		if id := info.Live.Identity; !id.Forwarded || id.Refused != "" || id.Person == nil || id.Person.Email != liveAdmin || info.Caller == nil || info.Caller.Login != alice {
			t.Errorf("a token for %s: caller %+v identity %+v", name, info.Caller, id)
		}
	}
	if len(st.muster.seen()) != 0 {
		t.Errorf("nothing reached muster: %+v", st.muster.seen())
	}
}

// getInfo is get_info's answer on c.
func getInfo(t *testing.T, c *client.Client) tools.Info {
	t.Helper()
	text, isErr := call(t, c, tools.ToolGetInfo, nil)
	if isErr {
		t.Fatal(text)
	}
	var info tools.Info
	if err := json.Unmarshal([]byte(text), &info); err != nil {
		t.Fatal(err)
	}
	return info
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
		// rowan runs the 3 line and registers no server of its own: the API
		// Agent Substrate needs on the 4 line and the registered servers are
		// not probed, and the dimensions say so.
		if id == "live-pod-certificate-request" || id == "live-registered-mcp-servers" {
			if d.Mark != verify.NotChecked || d.Reason != verify.ReasonNoProbe {
				t.Errorf("%s: %s (%s)", id, d.Mark, d.Reason)
			}
			continue
		}
		if d.Mark != verify.AsDefined {
			t.Errorf("%s: %s (%s) %+v", id, d.Mark, d.Reason, d.Live)
		}
	}
	if d := dims[liveDrift]; d.Live == nil || len(d.Live.Checks) != 1 || d.Live.Checks[0].Resource != helmReleaseKind || len(d.Differences) != 0 {
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
	// Every read runs as the person and names the installation's member by
	// muster's server name, the way muster routes a family's tool.
	for _, c := range calls {
		if c.Person != liveAdmin || c.Args[instanceArg] != rowan+"-mcp-kubernetes" {
			t.Errorf("a read not as the person on the installation's member: %+v", c)
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
	if other := liveDimensions(res)[liveDrift]; other.Mark != verify.AsDefined {
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

// The live values carry a global.domain off the installation's base domain:
// a leaf a fact derives, nobody's choice — drift, named without an input,
// and the state moves.
func TestVerifyInstallationFactOffTheRecordIsDrift(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())
	st.inst.edit("ConfigMap", fluxNamespace, konfiguration, func(obj map[string]any) {
		data := obj["data"].(map[string]any)
		values, _ := data[valuesFileKey].(string)
		if !strings.Contains(values, "  domain: rowan.acme.test\n") {
			t.Fatalf("the live values carry no global.domain of the record:\n%s", values)
		}
		data[valuesFileKey] = strings.Replace(values, "  domain: rowan.acme.test\n", "  domain: rowan.elsewhere.test\n", 1)
	})

	res := verifyLive(t, st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)), rowan)
	d := liveDimensions(res)[liveDrift]
	if d.Mark != verify.Drifted || len(d.Differences) != 1 || d.Differences[0].Input != "" || d.Differences[0].Path != "global.domain" || d.Differences[0].Rendered != "rowan.acme.test" {
		t.Fatalf("live-drift: %+v", d)
	}
	if res.Summary[verify.Drifted] == 0 || res.Summary[verify.DiffersByInput] != 0 || res.State != installations.StateDrifted {
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
	enableRowanLive(t, st, alice, minimalInputs(nil)) // the model key is the installation's own on every installation
	st.inst.edit("ModelConfig", kagentNamespace, "default-model-config", func(obj map[string]any) {
		obj[statusKey] = map[string]any{conditionsKey: []any{map[string]any{typeKey: accepted, statusKey: statusFalse, message: modelKeyMissing}}}
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

// An installation no action has rendered, verified without inputs: every
// live dimension is not checked and says there is nothing to compare
// against; nothing is read.
func TestVerifyInstallationWithoutInputsOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)

	res := verifyLive(t, st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)), alder)
	if res.Inputs.Source != verify.SourceNone {
		t.Errorf("inputs %q", res.Inputs.Source)
	}
	for id, d := range liveDimensions(res) {
		if d.Mark != verify.NotChecked || d.Reason != verify.ReasonNoRender {
			t.Errorf("%s: %+v", id, d)
		}
	}
	if len(st.muster.seen()) != 0 {
		t.Errorf("reads without inputs: %+v", st.muster.seen())
	}
}

// get_info reports the live tools on the one registration and the identity
// forwarding they read with: the header, muster's setting, and whether this
// call carried it — here it does not, and nothing is refused.
func TestGetInfoReportsTheLiveTools(t *testing.T) {
	st := newStack(t)
	info := getInfo(t, st.mcpClient(t, aliceToken))
	if !info.Live.Configured || info.Live.Tool != tools.ToolVerifyInstallation || info.Live.Issuer != st.dex.issuer || info.Live.KubernetesFamily != kubernetesFamily ||
		!reflect.DeepEqual(info.Live.Audiences, []string{liveAudience, liveRequiredAudience}) || !reflect.DeepEqual(info.Live.Tools, []string{tools.ToolVerifyInstallation, tools.ToolWatchAction}) ||
		info.Live.Identity.Header != identity.ForwardedIdentityHeader || info.Live.Identity.Muster != "auth.forwardIdentity" || info.Live.Identity.Forwarded || info.Live.Identity.Refused != "" {
		t.Errorf("live: %+v", info.Live)
	}
	c := st.mcpClient(t, aliceToken)
	listed, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, tool := range listed.Tools {
		served[tool.Name] = true
	}
	for _, want := range append([]string{tools.ToolGetInfo, tools.ToolVerifyCapability}, info.Live.Tools...) {
		if !served[want] {
			t.Errorf("%s is not among the tools of the one registration", want)
		}
	}
}

// On the 4 line the runtime feature reads the API Agent Substrate needs
// through discovery: served, the dimension is as defined; while the record
// says the cluster has the gates and the apiserver does not serve
// certificates.k8s.io/v1beta1 podcertificaterequests yet — the control plane
// rolls after the gates are set — the check reads rolling and the feature is
// marked, never as a fault of a different kind. rowan's record is typed onto
// the 4 line with the fact, the way a person checks a plan before the merge.
func TestVerifyInstallationReadsRollingUntilPodCertificateRequestIsServed(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	inputs := kagentEnabled()
	inputs[argInstallation] = map[string]any{chartLineFact: "4", "podCertificateRequest": true}
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), inputs)
	admin := st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)
	const dimension = "live-pod-certificate-request"

	res := verifyLive(t, st.liveClient(t, admin), rowan)
	d := liveDimensions(res)[dimension]
	if d.Mark != verify.AsDefined || d.Live == nil || len(d.Live.Checks) != 1 || d.Live.Checks[0].Kind != string(render.APIServed) || d.Live.Checks[0].Message != "served: certificates.k8s.io/v1beta1 podcertificaterequests" {
		t.Fatalf("served: %+v", d)
	}
	var discovered bool
	for _, c := range st.muster.seen() {
		discovered = discovered || (c.Tool == opAPIResources && c.Args["apiGroup"] == "certificates.k8s.io" && c.Args[instanceArg] == rowan+"-mcp-kubernetes" && c.Person == liveAdmin)
	}
	if !discovered {
		t.Errorf("discovery ran through muster as the person: %+v", st.muster.seen())
	}

	st.inst.unserveAPI("certificates.k8s.io", "podcertificaterequests")
	res = verifyLive(t, st.liveClient(t, admin), rowan)
	d = liveDimensions(res)[dimension]
	if d.Mark != verify.Drifted || d.Live == nil || len(d.Live.Checks) != 1 || !strings.HasPrefix(d.Live.Checks[0].Message, verify.Rolling) || !strings.Contains(d.Live.Checks[0].Message, "certificates.k8s.io/v1beta1 podcertificaterequests is not served yet") || !strings.Contains(d.Live.Checks[0].Note, "PodCertificateRequest, ClusterTrustBundle, ClusterTrustBundleProjection") {
		t.Fatalf("not served: %+v", d)
	}
	var runtime verify.Feature
	for _, f := range res.Features {
		if f.ID == "runtime" {
			runtime = f
		}
	}
	if runtime.Mark != verify.Drifted || res.State != installations.StateDrifted {
		t.Errorf("the runtime feature is marked: %s, state %s", runtime.Mark, res.State)
	}
	if p := recordedProbes(t, st)[dimension]; p.Result != string(verify.Drifted) {
		t.Errorf("recorded: %+v", p)
	}
}

// mcp-kubernetes caps a tool's answer at 128 KiB and refuses a larger one
// whole, and muster relays the refusal: the meta chart's HelmRelease as the
// apiserver holds it and a busy pod's last thousand lines are past the cap.
// A check asks for what it reads — the slim output for a readiness check,
// the normal output for a drift probe's values, the last two hundred lines
// of a log — and the fixture, as bulky as the platform's objects, is read
// within the cap; a read the cap still refuses is not checked with the
// refusal in plain words while the checks that asked for less stand.
func TestVerifyInstallationReadsWithinTheResponseCap(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())
	admin := st.liveClient(t, st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour))
	hr, _ := st.inst.get(helmReleaseKind, fluxNamespace, platformRelease)
	if whole, _ := json.Marshal(hr); len(whole) <= responseLimit {
		t.Fatalf("the fixture's HelmRelease is %d bytes whole, within the cap", len(whole))
	}
	st.inst.editLogs(func(logs map[string]string) {
		for key, log := range logs {
			if strings.HasPrefix(key, kagentNamespace+"/") && len(log) <= responseLimit {
				t.Fatalf("the fixture's log %s is %d bytes whole, within the cap", key, len(log))
			}
		}
	})

	res := verifyLive(t, admin, rowan)
	dims := liveDimensions(res)
	for _, id := range []string{"live-helmreleases-ready", liveDrift, "live-oauth2-proxy-audience"} {
		if d := dims[id]; d.Mark != verify.AsDefined {
			t.Errorf("%s: %s (%s)", id, d.Mark, d.Reason)
		}
	}
	if d := dims["live-oauth2-proxy-audience"]; d.Live == nil || !strings.Contains(d.Live.Checks[0].Note, "the last 200 lines of each pod's log are read") || !strings.Contains(d.Live.Checks[0].Message, "the last 200 lines") {
		t.Errorf("the absence check says how much it reads: %+v", d.Live)
	}
	// Every read asked for what its check reads: the meta chart's HelmRelease
	// slim for its readiness and normal for its values, the ConfigMaps a
	// drift probe compares normal, the Dex client Secrets whole for when
	// their data changed, the other Secrets and the pods slim, a log's last
	// LogTail lines; nothing else whole.
	outputs := map[string]map[string]bool{}
	for _, c := range st.muster.seen() {
		switch c.Tool {
		case opGet, opList:
			output, _ := c.Args["output"].(string)
			key := fmt.Sprint(c.Args["resourceType"])
			if c.Tool == opGet {
				key += "/" + fmt.Sprint(c.Args[nameKey])
			}
			if outputs[key] == nil {
				outputs[key] = map[string]bool{}
			}
			outputs[key][output] = true
		case opLogs:
			if tail, _ := c.Args["tailLines"].(float64); int(tail) != verify.LogTail {
				t.Errorf("a log read of %v lines: %+v", tail, c)
			}
		}
	}
	for key, seen := range outputs {
		kind, _, _ := strings.Cut(key, "/")
		var want map[string]bool
		switch {
		case key == "helmrelease/"+platformRelease || key == "helmrelease/kagent":
			want = map[string]bool{outputSlim: true, outputNormal: true}
		case kind == "configmap":
			want = map[string]bool{outputNormal: true}
		case kind == "deployment" && key == "deployment/kagent-oauth2-proxy":
			want = map[string]bool{outputSlim: true, outputNormal: true}
		case strings.HasPrefix(key, "secret/"+render.DexClientSecretName("")):
			want = map[string]bool{outputFull: true}
		default:
			want = map[string]bool{outputSlim: true}
		}
		if !reflect.DeepEqual(seen, want) {
			t.Errorf("%s was read with %v, want %v", key, seen, want)
		}
	}

	// The values of the meta chart's HelmRelease past the cap even without
	// the bookkeeping: the drift probe, which compares them, is not checked
	// with the refusal in plain words; the readiness check, which asks for
	// the object without them, stands. A log whose last two hundred lines
	// are past the cap: the absence check says so the same way.
	st.inst.edit(helmReleaseKind, fluxNamespace, platformRelease, func(obj map[string]any) {
		values := obj["spec"].(map[string]any)["values"].(map[string]any)
		values["padding"] = strings.Repeat("v", responseLimit)
	})
	st.inst.editLogs(func(logs map[string]string) {
		for key := range logs {
			if strings.HasPrefix(key, kagentNamespace+"/") {
				logs[key] = strings.Repeat(strings.Repeat("x", 1024)+"\n", verify.LogTail)
			}
		}
	})
	res = verifyLive(t, admin, rowan)
	dims = liveDimensions(res)
	for _, id := range []string{liveDrift, "live-oauth2-proxy-audience"} {
		d := dims[id]
		if d.Mark != verify.NotChecked || !strings.Contains(d.Reason, "larger than mcp-kubernetes answers") || !strings.Contains(d.Reason, "the limit is 128 KiB") || strings.Contains(d.Reason, "response_too_large") {
			t.Errorf("%s: %s (%s)", id, d.Mark, d.Reason)
		}
	}
	if d := dims["live-helmreleases-ready"]; d.Mark != verify.AsDefined {
		t.Errorf("live-helmreleases-ready without the values: %s (%s)", d.Mark, d.Reason)
	}
	if p := recordedProbes(t, st)[liveDrift]; p.Result != string(verify.NotChecked) || !strings.Contains(p.Message, "larger than mcp-kubernetes answers") {
		t.Errorf("recorded: %+v", p)
	}
}

// A registered server's MCPServer object is read live: present and in no
// Failed state is as defined — a server waiting for a person's session reports
// Awaiting Session or Auth Required, which is no fault — and Failed is drift
// naming muster's last error.
func TestVerifyInstallationReadsRegisteredServers(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	registerOnRowan(st)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())
	admin := st.dex.token(t, liveAdmin, []string{liveAudience}, time.Hour)
	const dim, stateKey = "live-registered-mcp-servers", "state"

	res := verifyLive(t, st.liveClient(t, admin), rowan)
	d := liveDimensions(res)[dim]
	if d.Mark != verify.AsDefined || d.Live == nil || len(d.Live.Checks) != 1 || d.Live.Checks[0].Name != rowanRegisteredServer || d.Live.Checks[0].Message != "present, no state reported yet" {
		t.Fatalf("%s: %s (%s) %+v", dim, d.Mark, d.Reason, d.Live)
	}
	inst, ok := st.muster.installation(rowan)
	if !ok {
		t.Fatal("no fake installation for rowan")
	}
	inst.edit("MCPServer", "agent-platform", rowanRegisteredServer, func(obj map[string]any) {
		obj["status"] = map[string]any{stateKey: "Awaiting Session"}
	})
	if d = liveDimensions(verifyLive(t, st.liveClient(t, admin), rowan))[dim]; d.Mark != verify.AsDefined || d.Live.Checks[0].Message != "present, state Awaiting Session" {
		t.Errorf("awaiting a session: %s %+v", d.Mark, d.Live)
	}
	inst.edit("MCPServer", "agent-platform", rowanRegisteredServer, func(obj map[string]any) {
		obj["status"] = map[string]any{stateKey: "Failed", "lastError": "dial tcp: connection refused\nafter 3 attempts"}
	})
	if d = liveDimensions(verifyLive(t, st.liveClient(t, admin), rowan))[dim]; d.Mark != verify.Drifted || d.Live.Checks[0].Message != "state Failed: dial tcp: connection refused" {
		t.Errorf("failed: %s %+v", d.Mark, d.Live)
	}
}
