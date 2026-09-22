package e2e

// verify_capability over the invented registry: the three marks and the
// roll-up, the probes, and the live dimensions reported as not checked.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"gopkg.in/yaml.v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	argPrivate   = "private"
	privateInput = "installation." + argPrivate
)

func verifyRowan(t *testing.T, c *client.Client, name string) verify.Result {
	t.Helper()
	return verifyWith(t, c, name, nil)
}

// verifyWith is verify_capability of name with the person's typed inputs.
func verifyWith(t *testing.T, c *client.Client, name string, inputs map[string]any) verify.Result {
	t.Helper()
	args := map[string]any{}
	if inputs != nil {
		args[tools.ArgInputs] = inputs
	}
	return verifyArgs(t, c, name, args)
}

// verifyArgs is verify_capability of name with the tool's other arguments.
func verifyArgs(t *testing.T, c *client.Client, name string, args map[string]any) verify.Result {
	t.Helper()
	args[tools.ArgInstallation] = name
	text, isErr := call(t, c, tools.ToolVerifyCapability, args)
	if isErr {
		t.Fatalf("verify_capability %s: %s", name, text)
	}
	var out verify.Result
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	return out
}

func feature(t *testing.T, res verify.Result, id string) verify.Feature {
	t.Helper()
	for _, f := range res.Features {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("feature %s is not in the result", id)
	return verify.Feature{}
}

// anyDimension is the dimension id of whichever feature of res carries it.
func anyDimension(t *testing.T, res verify.Result, id string) verify.Dimension {
	t.Helper()
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.ID == id {
				return d
			}
		}
	}
	t.Fatalf("dimension %s is not in the result", id)
	return verify.Dimension{}
}

func dimension(t *testing.T, f verify.Feature, id string) verify.Dimension {
	t.Helper()
	for _, d := range f.Dimensions {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("dimension %s is not in feature %s", id, f.ID)
	return verify.Dimension{}
}

// enableRowan puts the files rendered from inRepos into the registry's
// repositories and seeds the Action that holds onRecord as its inputs — the
// same inputs for an installation as defined.
func enableRowan(t *testing.T, st *stack, c *client.Client, onRecord, inRepos map[string]any) {
	t.Helper()
	putOnRecord(t, st, c, rowan, inRepos)
	seeded := actions.Action{Name: "enable-rowan-1", Namespace: actionsNamespace, CreatedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		Spec:   actions.Spec{Actor: actions.Actor{Login: alice}, Capability: installations.AgentPlatform, Installations: []string{rowan}, Kind: "enable", Inputs: onRecord},
		Status: actions.Status{State: string(installations.StateEnabled)}}
	if _, err := st.dyn.Resource(actions.GVR).Namespace(actionsNamespace).Create(context.Background(), actions.Unstructured(seeded), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

// putOnRecord renders name from the typed inputs and puts every file into
// the registry's repositories, as an enablement by hand leaves them: no
// action records the inputs.
func putOnRecord(t *testing.T, st *stack, c *client.Client, name string, typed map[string]any) {
	t.Helper()
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: name, tools.ArgInputs: typed})
	if isErr {
		t.Fatalf("dry run: %s", text)
	}
	p := findPlan(t, out, name)
	if p.Refused != "" {
		t.Fatalf("refused: %s", p.Refused)
	}
	files := map[string]map[string]string{}
	for _, f := range p.Files {
		if files[f.Repository] == nil {
			files[f.Repository] = map[string]string{}
		}
		files[f.Repository][f.Path] = f.Content
	}
	for repo, fs := range files {
		st.ghs.addFiles(repo, fs)
	}
}

func kagentEnabled() map[string]any {
	return minimalInputs(nil)
}

// The repositories hold exactly the render from the inputs on record and
// every probe answers as expected: no feature drifted or differs, the file
// dimensions are as defined, the live dimensions are not checked and say
// they need the person's authority, the probes ran.
func TestVerifyCapabilityAsDefined(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())

	res := verifyRowan(t, c, rowan)
	if res.Caller != alice || res.Inputs.Source != verify.SourceRecord || res.State != installations.StateEnabled {
		t.Errorf("caller %q inputs %q state %q", res.Caller, res.Inputs.Source, res.State)
	}
	if res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 || res.Summary[verify.AsDefined] == 0 {
		t.Errorf("summary %v", res.Summary)
	}
	runtime := feature(t, res, "runtime")
	if runtime.Mark != verify.AsDefined {
		t.Errorf("runtime %q: %+v", runtime.Mark, runtime.Marks)
	}
	if d := dimension(t, runtime, "kagent-enabled"); d.Mark != verify.AsDefined || len(d.Files) == 0 {
		t.Errorf("kagent-enabled: %+v", d)
	}
	// The plan's view rides along: every file unchanged, nothing to open,
	// the content only when asked for.
	if len(res.Files) == 0 || res.Diff[plan.ChangeUnchanged] != len(res.Files) || len(res.PullRequests) != 0 || res.CommitRefused != "" || len(res.Probes) == 0 {
		t.Errorf("plan view: %d files, diff %v, %d pull requests, commit refused %q, %d probes", len(res.Files), res.Diff, len(res.PullRequests), res.CommitRefused, len(res.Probes))
	}
	if res.Files[0].Content != "" || res.Files[0].Current != "" {
		t.Errorf("content without asking: %q on record %q", res.Files[0].Content, res.Files[0].Current)
	}
	text, isErr := call(t, c, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgContent: true})
	if isErr || !strings.Contains(text, `"content": "`) {
		t.Errorf("content asked for: isErr %v, %.200s", isErr, text)
	}
	if d := dimension(t, runtime, "live-helmreleases-ready"); d.Mark != verify.NotChecked || d.Reason != verify.ReasonAuthority {
		t.Errorf("a live dimension is not checked and says why: %+v", d)
	}
	identity := feature(t, res, "identity")
	if identity.Mark != verify.AsDefined {
		t.Errorf("identity %q: %+v", identity.Mark, identity.Marks)
	}
	if d := dimension(t, identity, "dex-auth-request"); d.Kind != definitions.KindProbe || d.Mark != verify.AsDefined || len(d.Probe.Requests) == 0 || d.Probe.Requests[0].Status != http.StatusFound || d.Probe.Requests[0].Client == "" {
		t.Errorf("dex-auth-request: %+v", d)
	}
	if d := dimension(t, feature(t, res, "tool-access"), "muster-protected-resource-metadata"); d.Mark != verify.AsDefined || d.Probe.Requests[0].Status != http.StatusOK {
		t.Errorf("muster-protected-resource-metadata: %+v", d)
	}
}

// A hand edit at a path no input drives is drift: the dimension names the
// file and the path, its feature is drifted, the state is drifted.
func TestVerifyCapabilityDrifted(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	st.ghs.mu.Lock()
	content := st.ghs.files[acmeConfigs][marker]
	st.ghs.mu.Unlock()
	st.ghs.addFiles(acmeConfigs, map[string]string{marker: content + "\nhandEdited: true\n"})

	res := verifyRowan(t, c, rowan)
	if res.State != installations.StateDrifted || res.Summary[verify.Drifted] != 1 || res.Summary[verify.DiffersByInput] != 0 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	runtime := feature(t, res, "runtime")
	if runtime.Mark != verify.Drifted {
		t.Errorf("runtime %q", runtime.Mark)
	}
	d := dimension(t, runtime, "patch-top-level-keys")
	if d.Mark != verify.Drifted || len(d.Differences) != 1 || d.Differences[0].File != acmeConfigs+":"+marker || d.Differences[0].Path != "handEdited" || d.Differences[0].Input != "" {
		t.Errorf("patch-top-level-keys: %+v", d)
	}
	if identity := feature(t, res, "identity"); identity.Mark != verify.AsDefined {
		t.Errorf("identity %q", identity.Mark)
	}
}

// The installation's patch carries a key the template owns, one the
// definition's removals name: the difference is a planned change with the
// removal's reason, the dimension and its feature read planned, the summary
// counts it, and the installation stays enabled.
func TestVerifyCapabilityPlannedChange(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	st.ghs.mu.Lock()
	content := st.ghs.files[acmeConfigs][marker]
	st.ghs.mu.Unlock()
	st.ghs.addFiles(acmeConfigs, map[string]string{marker: content + "\ngitops:\n  forbidInlineSecrets: true\n"})
	removals, err := definitions.Removals(installations.AgentPlatform)
	if err != nil {
		t.Fatal(err)
	}
	reason := ""
	for _, r := range removals {
		if r.Key == "configmap:gitops.forbidInlineSecrets" && r.Kind == "template" {
			reason = r.Reason
		}
	}
	if reason == "" {
		t.Fatal("removals.yaml no longer names configmap:gitops.forbidInlineSecrets as the template's")
	}

	res := verifyRowan(t, c, rowan)
	if res.State != installations.StateEnabled || res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 || res.Summary[verify.Planned] != 1 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	secrets := feature(t, res, "secrets")
	if secrets.Mark != verify.Planned {
		t.Errorf("secrets %q: %+v", secrets.Mark, secrets.Marks)
	}
	d := dimension(t, secrets, "forbid-inline-secrets")
	if d.Mark != verify.Planned || len(d.Differences) != 1 || d.Differences[0].Path != "gitops.forbidInlineSecrets" || d.Differences[0].Planned != reason || d.Differences[0].Input != "" {
		t.Errorf("forbid-inline-secrets: %+v", d)
	}
	if runtime := feature(t, res, "runtime"); runtime.Mark != verify.AsDefined {
		t.Errorf("runtime %q", runtime.Mark)
	}
}

// An installation enabled by hand before a migration lacks what the
// migration adds — here the kagent client's referenced Secret and its
// kustomization entry: the definition renders them, the record has no leaf
// there, and each difference is a planned addition with the migration's
// reason; the dimensions and the feature read planned, the summary counts
// them, and the installation stays enabled.
func TestVerifyCapabilityPlannedAddition(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	const kagentClientFile = "dex-client-kagent-secret.yaml"
	st.ghs.mu.Lock()
	kustomization := ""
	for p, content := range st.ghs.files[acmeMCs] {
		switch {
		case strings.HasSuffix(p, "/extras/agent-platform/secrets/"+kagentClientFile):
			delete(st.ghs.files[acmeMCs], p)
		case strings.HasSuffix(p, "/extras/agent-platform/secrets/kustomization.yaml"):
			kustomization = p
			st.ghs.files[acmeMCs][p] = strings.ReplaceAll(content, "  - "+kagentClientFile+"\n", "")
		}
	}
	st.ghs.mu.Unlock()
	if kustomization == "" {
		t.Fatal("the secrets kustomization is not in the repository")
	}

	res := verifyRowan(t, c, rowan)
	if res.State != installations.StateEnabled || res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 || res.Summary[verify.Planned] != 2 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	secrets := feature(t, res, "secrets")
	if secrets.Mark != verify.Planned {
		t.Errorf("secrets %q: %+v", secrets.Mark, secrets.Marks)
	}
	for _, id := range []string{"secrets-kustomization-list", "other-secret-shapes"} {
		d := dimension(t, secrets, id)
		if d.Mark != verify.Planned || len(d.Differences) == 0 {
			t.Errorf("%s: %+v", id, d)
		}
		for _, diff := range d.Differences {
			if !strings.HasSuffix(diff.Planned, "· M5") || diff.Current != "" || diff.Rendered == "" {
				t.Errorf("%s: %+v", id, diff)
			}
		}
	}
	if identity := feature(t, res, "identity"); identity.Mark != verify.AsDefined {
		t.Errorf("identity %q", identity.Mark)
	}
}

// The hub's kagent UI accepts the portal's id alone on record; the render
// adds the authenticator's, kagent's and backstage's to the one scalar,
// each an entry the migrations name: the leaf is a planned change with the
// migrations' reasons joined, not a difference by input, and the dimension
// reads planned.
func TestVerifyCapabilityPlannedJoinedAudience(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	st.ghs.mu.Lock()
	content := st.ghs.files[hubConfigs][marker]
	st.ghs.mu.Unlock()
	onRecord := "oidc-extra-audience: dex-k8s-authenticator,kagent," + hubPortalClientID + ",backstage," + hubExtraAudienceID + "\n"
	if !strings.Contains(content, onRecord) {
		t.Fatalf("the hub's patch on record:\n%s", content)
	}
	st.ghs.addFiles(hubConfigs, map[string]string{marker: strings.Replace(content, onRecord, "oidc-extra-audience: "+hubPortalClientID+"\n", 1)})

	res := verifyWith(t, c, hub, nil)
	d := dimension(t, feature(t, res, "identity"), "oauth2-proxy-extra-audience")
	if d.Mark != verify.Planned || len(d.Differences) != 1 {
		t.Fatalf("oauth2-proxy-extra-audience %q: %+v", d.Mark, d.Differences)
	}
	diff := d.Differences[0]
	if diff.Path != plan.ListExtraAudience || diff.Current != hubPortalClientID || diff.Rendered != "dex-k8s-authenticator,kagent,"+hubPortalClientID+",backstage" {
		t.Errorf("oauth2-proxy-extra-audience: %+v", diff)
	}
	for _, m := range []string{"the audience kagent, its own client · M5", "the dex-k8s-authenticator client · M5", "the backstage client, so a portal session opens the kagent UI · M30"} {
		if !strings.Contains(diff.Planned, m) {
			t.Errorf("planned %q lacks %q", diff.Planned, m)
		}
	}
}

// The repositories express another value of a fact than the one on record:
// a fact is nobody's choice, so every difference is drift, none names an
// input, and the state moves.
func TestVerifyCapabilityFactOffTheRecordIsDrift(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	inRepos := kagentEnabled()
	inRepos["installation"] = map[string]any{argPrivate: true}
	enableRowan(t, st, c, kagentEnabled(), inRepos)

	res := verifyRowan(t, c, rowan)
	if res.Summary[verify.Drifted] == 0 || res.Summary[verify.DiffersByInput] != 0 || res.State != installations.StateDrifted || len(res.Inputs.Typed) != 0 {
		t.Fatalf("state %q summary %v typed %v", res.State, res.Summary, res.Inputs.Typed)
	}
	for _, diff := range allDifferences(res) {
		if diff.Input != "" || diff.Planned != "" {
			t.Errorf("%s#%s is %q / %q, want drift", diff.File, diff.Path, diff.Input, diff.Planned)
		}
	}
}

// A fact typed for the call is the person's input of that call: with the
// repositories as on record and installation.private typed, every
// difference names installation.private, nothing is drift, the state stands
// and the inputs object carries what was typed.
func TestVerifyCapabilityDiffersByInput(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())

	typed := map[string]any{argInstallation: map[string]any{argPrivate: true}}
	res := verifyWith(t, c, rowan, typed)
	if res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] == 0 || res.State != installations.StateEnabled || !strings.HasSuffix(res.Inputs.Source, verify.SourceTyped) {
		t.Fatalf("state %q summary %v inputs %q", res.State, res.Summary, res.Inputs.Source)
	}
	if !reflect.DeepEqual(res.Inputs.Typed, typed) {
		t.Errorf("typed %v, want %v", res.Inputs.Typed, typed)
	}
	diffs := allDifferences(res)
	if len(diffs) == 0 {
		t.Fatal("no difference: the private fact moves nothing")
	}
	for _, diff := range diffs {
		if diff.Input != privateInput {
			t.Errorf("%s#%s is %q, want %s", diff.File, diff.Path, diff.Input, privateInput)
		}
	}
}

// allDifferences are every difference of res, over its features and dimensions.
func allDifferences(res verify.Result) []verify.Difference {
	var out []verify.Difference
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			out = append(out, d.Differences...)
		}
	}
	return out
}

// A probe that answers wrong is drift of its feature, and only its: the
// roll-up lets the other features stand.
func TestVerifyCapabilityProbeDriftRollsUp(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	first := dimension(t, feature(t, verifyRowan(t, c, rowan), "tool-access"), "muster-protected-resource-metadata")
	u, err := url.Parse(first.Probe.Requests[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	st.probes.answer(u.Host+u.Path, http.StatusNotFound)

	res := verifyRowan(t, c, rowan)
	toolAccess := feature(t, res, "tool-access")
	if toolAccess.Mark != verify.Drifted || res.State != installations.StateDrifted {
		t.Errorf("tool-access %q state %q", toolAccess.Mark, res.State)
	}
	if d := dimension(t, toolAccess, "muster-protected-resource-metadata"); d.Mark != verify.Drifted || d.Probe.Requests[0].Status != http.StatusNotFound {
		t.Errorf("probe: %+v", d)
	}
	if identity := feature(t, res, "identity"); identity.Mark != verify.AsDefined {
		t.Errorf("identity %q", identity.Mark)
	}
}

// An installation no action has rendered is compared from its record: the
// inputs are the schema's defaults under the facts, the file dimensions are
// checked (every file absent, so off the render), the per-client probe runs.
func TestVerifyCapabilityFromTheRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)

	res := verifyRowan(t, c, alder)
	if res.Inputs.Source != verify.SourceRecord || res.State != installations.StateNotEnabled || res.Refused != "" {
		t.Errorf("inputs %q state %q refused %q", res.Inputs.Source, res.State, res.Refused)
	}
	if facts, _ := res.Inputs.Values["installation"].(map[string]any); facts["name"] != alder {
		t.Errorf("inputs %v", res.Inputs.Values)
	}
	identity := feature(t, res, "identity")
	if d := dimension(t, identity, "muster-dex-client-id"); d.Mark == verify.NotChecked || len(d.Files) == 0 {
		t.Errorf("file dimension: %+v", d)
	}
	if d := dimension(t, identity, "dex-auth-request"); d.Reason == verify.ReasonNoRender {
		t.Errorf("per-client probe: %+v", d)
	}
	if d := dimension(t, identity, "oauth2-proxy-gate"); d.Mark != verify.AsDefined {
		t.Errorf("anonymous probe: %+v", d)
	}
}

// birchByHand puts birch on the 4 chart line and enables it by hand with
// the serving choice: the render from the choice on record, no action.
func birchByHand(t *testing.T, st *stack, c *client.Client, serving bool) {
	t.Helper()
	st.ghs.addFile(acmeConfigs, installations.ConfigPatchPath(birch), "codename: birch\nbase: acme.test\nagentPlatform:\n  kagentApiV2: true\n")
	st.ghs.addFile(acmeMCs, installations.ClusterAppManifestPath(birch), clusterAppManifest(birch, "cluster-aws", "10.3.0", true))
	putOnRecord(t, st, c, birch, map[string]any{"modelServing": map[string]any{enabledKey: serving}})
}

// inputDifferences are the differences of res that input drives.
func inputDifferences(res verify.Result, input string) []verify.Difference {
	var out []verify.Difference
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			for _, diff := range d.Differences {
				if diff.Input == input {
					out = append(out, diff)
				}
			}
		}
	}
	return out
}

// An installation enabled by hand with the serving slice on, no action:
// the choice is read back from the configmap patch, the render carries it
// and nothing differs by input; a reconcile dry run without typed inputs
// changes no file. With the key absent the default stands and the serving
// keys are not rendered.
func TestVerifyCapabilityReadsBackTheServingChoice(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	birchByHand(t, st, c, true)

	res := verifyRowan(t, c, birch)
	if res.Inputs.Source != verify.Source(true, false) || res.Refused != "" || res.Inputs.ReadBack["modelServing.enabled"] != true {
		t.Fatalf("inputs %q refused %q read back %v", res.Inputs.Source, res.Refused, res.Inputs.ReadBack)
	}
	if serving, _ := res.Inputs.Values["modelServing"].(map[string]any); serving["enabled"] != true {
		t.Errorf("values %v", res.Inputs.Values)
	}
	if diffs := inputDifferences(res, "modelServing.enabled"); len(diffs) != 0 || res.Summary[verify.DiffersByInput] != 0 || res.Summary[verify.Drifted] != 0 {
		t.Errorf("the serving choice on record differs: %v %+v", res.Summary, diffs)
	}
	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: birch})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, out, birch); p.Refused != "" || p.Diff[plan.ChangeCreate] != 0 || p.Diff[plan.ChangeUpdate] != 0 {
		t.Errorf("the first reconcile flips the choice: refused %q diff %v", p.Refused, p.Diff)
	}

	birchByHand(t, st, c, false)
	res = verifyRowan(t, c, birch)
	if res.Inputs.Source != verify.SourceRecord || res.Refused != "" || len(res.Inputs.ReadBack) != 0 || res.Summary[verify.DiffersByInput] != 0 {
		t.Fatalf("inputs %q refused %q read back %v summary %v", res.Inputs.Source, res.Refused, res.Inputs.ReadBack, res.Summary)
	}
	if serving, _ := res.Inputs.Values["modelServing"].(map[string]any); serving["enabled"] != false {
		t.Errorf("values %v", res.Inputs.Values)
	}
	if patch := st.ghs.repos()[acmeConfigs][installations.Capabilities()[0].EnabledMarker(birch)]; strings.Contains(patch, "kserve") || strings.Contains(patch, "modelServing") {
		t.Errorf("the serving keys are rendered from the default:\n%s", patch)
	}
}

// onRecord is the one file on the record whose path ends with suffix.
func onRecord(t *testing.T, st *stack, suffix string) (repo, path, content string) {
	t.Helper()
	st.ghs.mu.Lock()
	defer st.ghs.mu.Unlock()
	var found []string
	for r, files := range st.ghs.files {
		for p, c := range files {
			if strings.HasSuffix(p, suffix) {
				repo, path, content = r, p, c
				found = append(found, r+":"+p)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d files on record end with %s: %v", len(found), suffix, found)
	}
	return repo, path, content
}

// differencesOf are the differences the result names in the file.
func differencesOf(res verify.Result, file string) []verify.Difference {
	var out []verify.Difference
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			for _, diff := range d.Differences {
				if diff.File == file {
					out = append(out, diff)
				}
			}
		}
	}
	return out
}

// The encrypted files an enablement put on record are the render's outside
// the values SOPS encrypted: no dimension drifts, the secrets are as defined.
func TestVerifyCapabilityKeepsTheEncryptedFilesOnRecord(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	repo, path, content := onRecord(t, st, "/secrets/kagent-oauth2-proxy-credentials.yaml")
	if !strings.Contains(content, "ENC[") || !strings.Contains(content, "sops:") {
		t.Fatalf("%s is not encrypted on record:\n%s", path, content)
	}
	res := verifyRowan(t, c, rowan)
	if res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 || res.State != installations.StateEnabled {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	d := dimension(t, feature(t, res, "secrets"), "oauth2-proxy-credentials-secret")
	if d.Mark != verify.AsDefined || !slices.Contains(d.Files, repo+":"+path) || len(d.Differences) != 0 {
		t.Errorf("oauth2-proxy-credentials-secret: %+v", d)
	}
}

// An encrypted file whose plaintext skeleton is off the render drifts at the
// paths that differ, the values redacted: no value of it is shown. The
// difference sits on its line of the record, and the record, asked for with
// the content, is shown redacted the same way: SOPS's block dropped, no
// ciphertext left.
func TestVerifyCapabilityRedactsAnEncryptedFile(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	repo, path, content := onRecord(t, st, "/secrets/kagent-oauth2-proxy-credentials.yaml")
	st.ghs.addFiles(repo, map[string]string{path: content + "handEdited: true\n"})

	res := verifyArgs(t, c, rowan, map[string]any{tools.ArgContent: true})
	d := dimension(t, feature(t, res, "secrets"), "oauth2-proxy-credentials-secret")
	if d.Mark != verify.Drifted || res.Summary[verify.Drifted] != 1 || len(d.Differences) != 1 {
		t.Fatalf("oauth2-proxy-credentials-secret: %+v summary %v", d, res.Summary)
	}
	if diff := d.Differences[0]; diff.File != repo+":"+path || diff.Path != "handEdited" || diff.Rendered != "" || diff.Current != verify.Redacted || diff.Input != "" || diff.Line != 0 || diff.CurrentLine == 0 {
		t.Errorf("the difference: %+v", diff)
	}
	i := slices.IndexFunc(res.Files, func(f plan.File) bool { return f.Repository == repo && f.Path == path })
	if i < 0 {
		t.Fatalf("%s:%s is not among the files", repo, path)
	}
	shown := res.Files[i]
	if shown.Content == "" || shown.Current == "" || strings.Contains(shown.Current, "ENC[") || strings.Contains(shown.Current, "sops:") || !strings.Contains(shown.Current, "handEdited: ") {
		t.Errorf("the record as shown:\n%s", shown.Current)
	}
	if lines := strings.Split(shown.Current, "\n"); !strings.HasPrefix(lines[d.Differences[0].CurrentLine-1], "handEdited: ") {
		t.Errorf("the difference's line %d of the record as shown is %q", d.Differences[0].CurrentLine, lines[d.Differences[0].CurrentLine-1])
	}
}

// kustomizationResources are the resources of a kustomization on record, and
// the kustomization written with other resources.
func kustomizationResources(t *testing.T, content string) ([]string, func(resources []string) string) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatal(err)
	}
	var resources []string
	list, _ := doc["resources"].([]any)
	for _, r := range list {
		resources = append(resources, r.(string))
	}
	if len(resources) < 2 {
		t.Fatalf("resources on record: %v", resources)
	}
	return resources, func(resources []string) string {
		doc["resources"] = resources
		out, err := yaml.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
}

// A kustomization whose resources another owner added to, in another order,
// is as defined: the plan keeps their entries and the order is nobody's. An
// entry of the render dropped is one difference, the entry.
func TestVerifyCapabilityKeepsTheKustomizationEntries(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	repo, path, content := onRecord(t, st, "/secrets/kustomization.yaml")
	rendered, write := kustomizationResources(t, content)
	reversed := slices.Clone(rendered)
	slices.Reverse(reversed)
	st.ghs.addFiles(repo, map[string]string{path: write(append([]string{"theirs.yaml"}, reversed...))})

	res := verifyRowan(t, c, rowan)
	d := dimension(t, feature(t, res, "secrets"), "secrets-kustomization-list")
	if d.Mark != verify.AsDefined || len(d.Differences) != 0 || res.Summary[verify.Drifted] != 0 {
		t.Errorf("secrets-kustomization-list: %+v summary %v", d, res.Summary)
	}
	st.ghs.addFiles(repo, map[string]string{path: write(append([]string{"theirs.yaml"}, rendered[1:]...))})
	res = verifyRowan(t, c, rowan)
	if diffs := differencesOf(res, repo+":"+path); res.Summary[verify.AsDefined] == 0 || len(diffs) != 1 || diffs[0].Path != "resources["+rendered[0]+"]" || diffs[0].Rendered != rendered[0] || diffs[0].Current != "" {
		t.Errorf("an entry of the render dropped: %+v", diffs)
	}
}

// A file of several documents in another order is as defined: the documents
// are compared by kind/namespace/name, not by position; one dropped is its
// leaves.
func TestVerifyCapabilityReadsDocumentsByObject(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	federated := minimalInputs(map[string]any{argInstallation: map[string]any{"federation": map[string]any{
		"brokerClientId": "broker", "hubs": []any{},
		"targets": []any{map[string]any{"installation": alder, "baseDomain": alder + ".example", argPrivate: true}}}}})
	enableRowan(t, st, c, federated, federated)
	repo, path, content := onRecord(t, st, "/remoteapps.yaml")
	docs := strings.Split(content, "---\n")
	if len(docs) < 3 {
		t.Fatalf("%s holds %d documents:\n%s", path, len(docs)-1, content)
	}
	slices.Reverse(docs[1:])
	st.ghs.addFiles(repo, map[string]string{path: strings.Join(docs, "---\n")})

	res := verifyWith(t, c, rowan, federated)
	if diffs := differencesOf(res, repo+":"+path); res.Summary[verify.Drifted] != 0 || len(diffs) != 0 {
		t.Errorf("reordered documents: %v %+v", res.Summary, diffs)
	}
	st.ghs.addFiles(repo, map[string]string{path: strings.Join(docs[:len(docs)-1], "---\n")})
	res = verifyWith(t, c, rowan, federated)
	if diffs := differencesOf(res, repo+":"+path); len(diffs) != 7 || diffs[0].Current != "" || !strings.HasPrefix(diffs[0].Path, "[RemoteApp/"+platformNamespace+"/") {
		t.Errorf("a document dropped: %v %+v", res.Summary, diffs)
	}
}

// The hub's portal on record, written by hand: its domain, organisation,
// chart line and plugins are read back from its tree and the comparison
// takes them as the record's inputs, refusing nothing and marking the tree
// drifted from the render; the tunnel reads back off, its file not on
// record; the title is the default.
func TestVerifyCapabilityReadsBackThePortal(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	text, isErr := call(t, c, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal})
	if isErr {
		t.Fatal(text)
	}
	var res verify.Result
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	back := res.Inputs.ReadBack
	if res.Inputs.Source != verify.Source(true, false) || res.Refused != "" || res.State != installations.StateDrifted {
		t.Fatalf("inputs %q refused %q state %q read back %v", res.Inputs.Source, res.Refused, res.State, back)
	}
	if back["portal.domain"] != "portal."+hub+".example.test" || back["portal.organization"] != "Example" || back["chart.line"] != chartLine ||
		back["plugins.github.enabled"] != false || back["plugins.grafana.enabled"] != false || back["plugins.flux.enabled"] != false || back["plugins.sentry.enabled"] != false || back["tunnel.enabled"] != false {
		t.Fatalf("read back %v", back)
	}
	if portal, _ := res.Inputs.Values["portal"].(map[string]any); portal["domain"] != "portal."+hub+".example.test" || portal["title"] != "Dev Portal" {
		t.Errorf("values %v", res.Inputs.Values)
	}
	// The federation follows the record: every installation the hub's portal lists but the hub, by name, with its facts.
	federation, _ := res.Inputs.Values["federation"].(map[string]any)
	entries, _ := federation["installations"].([]any)
	var names []string
	for _, e := range entries {
		names = append(names, e.(map[string]any)["name"].(string))
	}
	const oak = "oak"
	if !slices.Equal(names, []string{alder, birch, "larch", maple, oak, rowan, "willow"}) {
		t.Fatalf("federation.installations %v", names)
	}
	// The registry's facts over the record's: birch's base domain is the catalog's, not the portal entry's.
	if e := entries[1].(map[string]any); e["baseDomain"] != birch+".acme.test" || e["agentPlatform"] != true || e["pipeline"] != "stable" || e["private"] != true || !slices.Equal(e["providers"].([]any), []any{"capa"}) {
		t.Errorf("%s: %v", birch, e)
	}
	if e := entries[0].(map[string]any); e["agentPlatform"] != false || e["private"] != false {
		t.Errorf("%s runs no platform and is public: %v", alder, e)
	}

}

// A federated portal signing people in at one of the installations it
// federates compares: the sign-in installation read back from gs.authProvider
// is among the set the record derives, so nothing is refused. A portal
// whose record lists an installation the registry does not know is refused
// naming that installation.
func TestVerifyCapabilityFederationFollowsTheRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	signIn := strings.Replace(portalConfig(hub, alder, birch), "        gs:\n", "        gs:\n          authProvider: oidc-"+birch+"\n", 1)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), signIn)
	text, isErr := call(t, c, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal})
	if isErr {
		t.Fatal(text)
	}
	var res verify.Result
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	federation, _ := res.Inputs.Values["federation"].(map[string]any)
	if res.Refused != "" || res.Inputs.ReadBack["federation.signInInstallation"] != birch || federation["signInInstallation"] != birch || len(federation["installations"].([]any)) != 2 {
		t.Fatalf("refused %q, read back %v, federation %v", res.Refused, res.Inputs.ReadBack, federation)
	}
	// The hub's portal is a registry source, so every name it lists is known; a customer's portal listing a name the registry lacks is refused by that name.
	st.ghs.addFile(umbrellaMCs, installations.PortalConfigPath(maple), portalConfig(maple, "spruce"))
	if text, isErr := call(t, c, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: maple, tools.ArgCapability: installations.CustomerPortal}); !isErr || !strings.Contains(text, "spruce") || !strings.Contains(text, "federation.installations") {
		t.Fatalf("an unknown installation on record: isErr %v, %s", isErr, text)
	}
}

// githubAppIDField is the portal's GitHub App id as the plan names it among
// the values supplied at commit; hubGitHubAppFile is the hub portal's GitHub
// App Secret on record, SOPS encrypted: the skeleton the render has, the
// values one ENC[...] scalar.
const (
	githubAppIDField     = "plugins.github.appId"
	hubGitHubAppFile     = "management-clusters/" + hub + "/extras/backstage/backstage/github-app-credentials.enc.yaml"
	hubGitHubAppOnRecord = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: github-app-credentials-backstage\n  namespace: flux-giantswarm\ntype: Opaque\nstringData:\n  values: ENC[AES256_GCM,data:fixture,iv:fixture,tag:fixture,type:str]\nsops:\n  age:\n    - recipient: age1fixture\n  encrypted_regex: ^(data|stringData)$\n  version: 3.9.0\n"
)

// portalConfigWithGitHub is portalConfig with the github integration on: the
// app-config lists it, so plugins.github.enabled reads back true.
func portalConfigWithGitHub(names ...string) string {
	return strings.Replace(portalConfig(names...), "        organization:\n", "        integrations:\n          github:\n            - host: github.com\n        organization:\n", 1)
}

// A portal on record with the github plugin on: its GitHub App id lives only
// in the encrypted file, so no read-back recovers it — the definition refuses
// nothing, the encrypted file on record reads as defined with its values
// opaque, the plan names the id among the values supplied at commit, and a
// commit without it is refused by field before anything is recorded.
func TestVerifyCapabilityTakesThePortalsGitHubAppIDAsSupplied(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), portalConfigWithGitHub(hub, alder, birch, "rowan", "willow", "oak", "larch"))
	st.ghs.addFile(hubMCs, hubGitHubAppFile, hubGitHubAppOnRecord)
	c := st.mcpClient(t, aliceToken)
	text, isErr := call(t, c, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal})
	if isErr {
		t.Fatal(text)
	}
	var res verify.Result
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	if res.Inputs.Source != verify.Source(true, false) || res.Refused != "" || res.Inputs.ReadBack["plugins.github.enabled"] != true {
		t.Fatalf("inputs %q refused %q read back %v", res.Inputs.Source, res.Refused, res.Inputs.ReadBack)
	}
	if !slices.Contains(res.SuppliedSecrets, githubAppIDField) || !slices.Contains(res.SuppliedSecrets, "plugins.github.privateKey") {
		t.Fatalf("supplied %v", res.SuppliedSecrets)
	}
	if d := dimension(t, feature(t, res, "secrets"), "github-app-credentials"); d.Mark != verify.AsDefined || !slices.Contains(d.Files, hubMCs+":"+hubGitHubAppFile) || len(d.Differences) != 0 {
		t.Errorf("github-app-credentials: %+v", d)
	}
	secrets := map[string]any{}
	for _, f := range res.SuppliedSecrets {
		if f != githubAppIDField {
			secrets[f] = "fixture-" + f
		}
	}
	if _, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal, tools.ArgSecrets: secrets}); !isErr || !strings.Contains(text, githubAppIDField) {
		t.Fatalf("a commit without the app id: %v %s", isErr, text)
	}
	if got := listActionsOf(t, c, hub); len(got) != 0 {
		t.Fatalf("a refused commit recorded %d action(s)", len(got))
	}
}

// The portal's input keys and the chart line every portal fixture follows.
const (
	domainKey  = "domain"
	grafanaKey = "grafana"
	chartLine  = ">=2.1.0 <3.0.0"
)

// rowanPortalInputs are the typed inputs of rowan's portal, with the grafana
// plugin as given.
func rowanPortalInputs(grafana map[string]any) map[string]any {
	return map[string]any{
		portalKey: map[string]any{domainKey: "portal.rowan.acme.test", "organization": "ACME"},
		"chart":   map[string]any{"line": chartLine},
		"plugins": map[string]any{"github": map[string]any{enabledKey: false}, grafanaKey: grafana, "flux": map[string]any{enabledKey: false}, "sentry": map[string]any{enabledKey: false}},
		"tunnel":  map[string]any{enabledKey: false},
	}
}

// verifyPortal is verify_capability of the customer portal on name, with the
// person's typed inputs.
func verifyPortal(t *testing.T, c *client.Client, name string, inputs map[string]any) verify.Result {
	t.Helper()
	args := map[string]any{tools.ArgInstallation: name, tools.ArgCapability: installations.CustomerPortal}
	if inputs != nil {
		args[tools.ArgInputs] = inputs
	}
	text, isErr := call(t, c, tools.ToolVerifyCapability, args)
	if isErr {
		t.Fatalf("verify_capability %s: %s", name, text)
	}
	var out verify.Result
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	return out
}

// putPortalOnRecord puts the portal a dry run rendered on record, with old
// replaced by new in its app-config (there must be something to replace),
// and answers the app-config's file key and content as recorded.
func putPortalOnRecord(t *testing.T, st *stack, p plan.Installation, old, new string) (key, appConfig string) {
	t.Helper()
	for _, f := range p.Files {
		content := f.Content
		if f.Path == installations.PortalConfigPath(rowan) {
			key = f.Repository + ":" + f.Path
			content = strings.Replace(content, old, new, 1)
			if content == f.Content {
				t.Fatalf("nothing to replace in the app-config:\n%s", content)
			}
			appConfig = content
		}
		st.ghs.addFile(f.Repository, f.Path, content)
	}
	if key == "" {
		t.Fatal("the plan renders no app-config")
	}
	return key, appConfig
}

// A portal on record without a choice its app-config would carry — the
// organisation's name — reads it back as a choice not on record. The
// comparison refuses nothing — the app-config's leaf carries the Missing
// marker, its dimension is not checked with the reason naming the choice,
// inputs.missing names it, nothing drifts — and neither does the dry run,
// which says a commit would be refused; a commit is refused by field and
// records nothing. The choice typed, the portal compares whole.
func TestVerifyCapabilityTakesAMissingChoiceAsNotChecked(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	const organization, name = "portal.organization", "ACME"
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal, tools.ArgInputs: rowanPortalInputs(map[string]any{enabledKey: false})})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" || len(p.MissingInputs) != 0 || p.CommitRefused != "" {
		t.Fatalf("the portal as typed: refused %q missing %v commit refused %q", p.Refused, p.MissingInputs, p.CommitRefused)
	}
	appConfig, _ := putPortalOnRecord(t, st, p, "        organization:\n          name: "+name+"\n", "")

	res := verifyPortal(t, c, rowan, nil)
	if res.Refused != "" || res.Inputs.ReadBack["portal.domain"] != "portal.rowan.acme.test" || !slices.Equal(res.Inputs.Missing, []string{organization}) {
		t.Fatalf("refused %q read back %v missing %v", res.Refused, res.Inputs.ReadBack, res.Inputs.Missing)
	}
	// Every choice not on record is named, the required one among them
	// and the optional ones the portal does not carry; the ones on record
	// (read back or by default) are not, nor is the Grafana host, which is
	// no choice.
	for _, field := range []string{organization, "portal.supportUrl", "portal.telemetrydeckAppId", "federation.tokenBroker"} {
		if !slices.Contains(res.Inputs.Unset, field) {
			t.Errorf("unset %v does not name %s", res.Inputs.Unset, field)
		}
	}
	for _, field := range []string{"portal.domain", "portal.title", "plugins.grafana.enabled", "plugins.grafana.domain", "chart.line"} {
		if slices.Contains(res.Inputs.Unset, field) {
			t.Errorf("unset %v names %s, which is on record or no choice", res.Inputs.Unset, field)
		}
	}
	wantRefused := "Choose " + organization + " (the organisation's name as the portal shows it) before a commit."
	if res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 || res.State != installations.StateEnabled || res.CommitRefused != wantRefused {
		t.Fatalf("summary %v state %q commit refused %q", res.Summary, res.State, res.CommitRefused)
	}
	want := verify.ReasonMissingChoice + ": " + organization
	var notChecked []string
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			if d.Reason != want {
				continue
			}
			notChecked = append(notChecked, d.ID)
			if d.Mark != verify.NotChecked || !slices.Contains(d.Files, appConfig) || len(d.Differences) != 0 {
				t.Errorf("%s: %+v", d.ID, d)
			}
		}
	}
	if len(notChecked) != 1 {
		t.Fatalf("dimensions not checked for the choice: %v", notChecked)
	}
	t.Logf("the app-config's leaf is observed under %s", notChecked[0])

	out, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal})
	if isErr {
		t.Fatal(text)
	}
	if p = findPlan(t, out, rowan); p.Refused != "" || !slices.Equal(p.MissingInputs, []string{organization}) || p.CommitRefused != wantRefused {
		t.Fatalf("dry run: refused %q missing %v commit refused %q", p.Refused, p.MissingInputs, p.CommitRefused)
	}
	if _, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal}); !isErr || !strings.Contains(text, organization) || strings.Contains(text, "type them") {
		t.Fatalf("a commit without the choice: %v %s", isErr, text)
	}
	if got := listActionsOf(t, c, rowan); len(got) != 0 {
		t.Fatalf("a refused commit recorded %d action(s)", len(got))
	}

	res = verifyPortal(t, c, rowan, map[string]any{portalKey: map[string]any{"organization": name}})
	if res.Refused != "" || len(res.Inputs.Missing) != 0 || res.Summary[verify.Drifted] != 0 || res.CommitRefused != "" {
		t.Fatalf("the choice typed: refused %q missing %v summary %v commit refused %q", res.Refused, res.Inputs.Missing, res.Summary, res.CommitRefused)
	}
	if d := anyDimension(t, res, notChecked[0]); d.Mark != verify.DiffersByInput || d.Reason != "" || len(d.Differences) != 1 || d.Differences[0].File != appConfig || d.Differences[0].Input != organization {
		t.Errorf("the choice typed: %+v", d)
	}
	// The portal's anonymous probes run against the portal's own domain.
	portalDomain := rowanPortalInputs(nil)[portalKey].(map[string]any)[domainKey].(string)
	for _, p := range []struct{ feature, id, url string }{
		{"portal", "portal-root", "https://" + portalDomain + "/"},
		{"identity", "portal-oidc-start", "https://" + portalDomain + "/api/auth/oidc-" + rowan + "/start?env=production"},
	} {
		if d := dimension(t, feature(t, res, p.feature), p.id); d.Mark != verify.AsDefined || len(d.Probe.Requests) != 1 || d.Probe.Requests[0].URL != p.url {
			t.Errorf("%s: mark %q reason %q requests %+v", p.id, d.Mark, d.Reason, d.Probe.Requests)
		}
	}
}

// A portal on record whose Grafana section names another instance than the
// installation's own — the hub's entry copied onto it — and no proxy entry:
// the host reads back as the record has it and the plugin as not wired; the
// host is no choice (not among the choices not on record, never an input,
// refused when typed), and the plugins dimension drifts, the render naming
// the installation's Grafana. With a proxy entry on record the plugin reads
// back wired, its token is among the values supplied at commit, the proxy's
// target is off the installation's Grafana too, and the extension list the
// render includes is the shared one with the dashboards card where the
// record's is the baseline; the registry reads the wiring off the entry, and
// the agent platform's fragment for the installation includes the platform's
// list with the card.
func TestVerifyCapabilityGrafanaIsTheInstallationsOwn(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	const grafanaDomain, grafanaEnabled, grafanaToken = "plugins.grafana.domain", "plugins.grafana.enabled", "plugins.grafana.token" // #nosec G101 -- field names, not values
	const own, central = "https://grafana." + rowan + ".acme.test", "https://giantswarm.grafana.net"
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal, tools.ArgInputs: rowanPortalInputs(map[string]any{enabledKey: false})})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" || slices.Contains(p.SuppliedSecrets, grafanaToken) {
		t.Fatalf("the portal not wired: refused %q supplied %v", p.Refused, p.SuppliedSecrets)
	}
	section := func(id, domain string) string {
		return "        grafana:\n          hosts:\n            - id: " + id + "\n              domain: " + domain + "\n"
	}
	appConfig, content := putPortalOnRecord(t, st, p, section(rowan, own), section("grafana-net", central))

	res := verifyPortal(t, c, rowan, nil)
	if res.Refused != "" || res.Inputs.ReadBack[grafanaDomain] != central || res.Inputs.ReadBack[grafanaEnabled] != false || len(res.Inputs.Missing) != 0 || slices.Contains(res.Inputs.Unset, grafanaDomain) {
		t.Fatalf("refused %q read back %v missing %v unset %v", res.Refused, res.Inputs.ReadBack, res.Inputs.Missing, res.Inputs.Unset)
	}
	if grafana, _ := res.Inputs.Values["plugins"].(map[string]any)[grafanaKey].(map[string]any); grafana[domainKey] != nil || grafana[enabledKey] != false {
		t.Errorf("values %v: the host read back is no input", res.Inputs.Values)
	}
	if res.State != installations.StateDrifted || res.Summary[verify.Drifted] != 1 || slices.Contains(res.SuppliedSecrets, grafanaToken) {
		t.Fatalf("state %q summary %v supplied %v", res.State, res.Summary, res.SuppliedSecrets)
	}
	d := dimension(t, feature(t, res, "portal"), "plugins")
	var names, drifts []string
	for _, diff := range d.Differences {
		if diff.File != appConfig || !strings.Contains(diff.Path, "grafana.hosts[") {
			t.Errorf("a difference outside the Grafana section: %+v", diff)
		}
		if diff.Rendered == own {
			names = append(names, diff.Path)
		}
		if diff.Current == central && diff.Input == "" && diff.Planned == "" {
			drifts = append(drifts, diff.Path)
		}
	}
	if d.Mark != verify.Drifted || len(names) != 1 || len(drifts) != 1 {
		t.Errorf("the plugins dimension: mark %q, the installation's Grafana rendered at %v, the central instance drifted at %v: %+v", d.Mark, names, drifts, d.Differences)
	}

	// The host typed is refused in one sentence naming the input, its key
	// and what supplies it; a commit would be refused for the same reason.
	res = verifyPortal(t, c, rowan, map[string]any{"plugins": map[string]any{grafanaKey: map[string]any{domainKey: own}}})
	if want := "the installation's own Grafana (" + grafanaDomain + ") is no input; it is derived from installation.baseDomain"; res.Refused != want || res.CommitRefused != want {
		t.Errorf("the host typed: refused %q, commit refused %q, want %q", res.Refused, res.CommitRefused, want)
	}

	// Wired on record, against the central instance.
	proxy := "        proxy:\n          endpoints:\n            /grafana/api:\n              target: " + central + "/\n              headers:\n                Authorization: Bearer $${GRAFANA_TOKEN}\n"
	repo, path, _ := strings.Cut(appConfig, ":")
	st.ghs.addFile(repo, path, strings.Replace(content, section("grafana-net", central), section("grafana-net", central)+proxy, 1))
	res = verifyPortal(t, c, rowan, nil)
	if res.Refused != "" || res.Inputs.ReadBack[grafanaEnabled] != true || !slices.Contains(res.SuppliedSecrets, grafanaToken) {
		t.Fatalf("wired on record: refused %q read back %v supplied %v", res.Refused, res.Inputs.ReadBack, res.SuppliedSecrets)
	}
	var target bool
	for _, diff := range dimension(t, feature(t, res, "portal"), "plugins").Differences {
		target = target || strings.HasSuffix(diff.Path, "/grafana/api.target") && diff.Rendered == own+"/" && diff.Current == central+"/"
	}
	if !target {
		t.Errorf("the proxy's target is not off the installation's Grafana: %+v", dimension(t, feature(t, res, "portal"), "plugins").Differences)
	}
	var include bool
	for _, diff := range dimension(t, feature(t, res, "portal"), "plugins").Differences {
		include = include || strings.HasSuffix(diff.Path, ":app.extensions.$include") && diff.Rendered == render.PortalExtensionsInclude(false, true) && diff.Current == render.PortalExtensionsInclude(false, false)
	}
	if !include {
		t.Errorf("wired, the render does not include the list with the dashboards card over the record's baseline: %+v", dimension(t, feature(t, res, "portal"), "plugins").Differences)
	}

	// The registry reads the wiring off the proxy entry: rowan's own portal
	// is wired, the hub's — listing rowan without the entry — is not.
	list, text, isErr := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{rowan}})
	if isErr {
		t.Fatal(text)
	}
	wired := map[string]bool{}
	for _, p := range find(t, list, rowan).Portals {
		wired[p.Installation] = p.GrafanaWired
	}
	if len(wired) != 2 || !wired[rowan] || wired[hub] {
		t.Fatalf("the portals' Grafana wiring read off the record: %v", wired)
	}

	// The agent platform's fragment is the list Backstage keeps, so on rowan
	// it includes the platform's list with the card.
	out, text, isErr = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.AgentPlatform, tools.ArgInputs: kagentEnabled()})
	if isErr {
		t.Fatal(text)
	}
	fragment := strings.TrimSuffix(installations.PortalConfigPath(rowan), render.PortalDir+"/app-config.yaml") + render.PortalPlatformDir + "/app-config.yaml"
	var rendered string
	var paths []string
	for _, f := range findPlan(t, out, rowan).Files {
		paths = append(paths, f.Path)
		if f.Path == fragment {
			rendered = f.Content
		}
	}
	if want := "$include: " + render.PortalExtensionsInclude(true, true) + "\n"; !strings.Contains(rendered, want) {
		t.Errorf("the platform's fragment %s for the wired portal does not include %q:\n%s\nthe plan's files: %v", fragment, want, rendered, paths)
	}
}

// An installation enabled before the mcp-* servers' Valkey password became
// a generated Secret lacks mcp-prometheus's valkey-credentials.enc.yaml and
// its kustomization entry: each difference is M9's planned addition, and
// its reason names the server the key's <name> matched — mcp-prometheus,
// not the mcp-* server.
func TestVerifyCapabilityPlannedAdditionNamesTheServer(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	const valkeyFile = "valkey-credentials.enc.yaml"
	st.ghs.mu.Lock()
	kustomization := ""
	for p, content := range st.ghs.files[acmeMCs] {
		switch {
		case strings.HasSuffix(p, "/extras/mcp-prometheus/"+valkeyFile):
			delete(st.ghs.files[acmeMCs], p)
		case strings.HasSuffix(p, "/extras/mcp-prometheus/kustomization.yaml"):
			kustomization = p
			st.ghs.files[acmeMCs][p] = strings.ReplaceAll(content, "  - "+valkeyFile+"\n", "")
			if st.ghs.files[acmeMCs][p] == content {
				t.Fatalf("the kustomization does not list %s:\n%s", valkeyFile, content)
			}
		}
	}
	st.ghs.mu.Unlock()
	if kustomization == "" {
		t.Fatal("mcp-prometheus's kustomization is not in the repository")
	}

	res := verifyRowan(t, c, rowan)
	if res.State != installations.StateEnabled || res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 || res.Summary[verify.Planned] == 0 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	named := 0
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			for _, diff := range d.Differences {
				if !strings.Contains(diff.File, "/extras/mcp-prometheus/") {
					t.Errorf("%s/%s: a difference outside mcp-prometheus: %+v", f.ID, d.ID, diff)
					continue
				}
				if d.Mark != verify.Planned || !strings.HasSuffix(diff.Planned, "· M9") || !strings.Contains(diff.Planned, "the mcp-prometheus server") || strings.Contains(diff.Planned, "mcp-*") {
					t.Errorf("%s/%s (%s): %+v", f.ID, d.ID, d.Mark, diff)
				}
				named++
			}
		}
	}
	if named == 0 {
		t.Error("no difference under mcp-prometheus")
	}
}

// The patch on record carries a hand-written agent-platform-mcps.mcpServers
// list the definition does not render for an installation without targets.
// The entries of its own three servers — at the in-cluster Service, or on
// the installation's own base domain — are the template's, each reason
// naming the server, observed under own-mcp-servers; an entry on another
// installation's host, what a hub keeps of a target it once listed by hand,
// is M19 like any other server, observed under federated-mcp-servers.
func TestVerifyCapabilityOwnMCPServersByHost(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	st.ghs.mu.Lock()
	content := st.ghs.files[acmeConfigs][marker]
	st.ghs.mu.Unlock()
	if strings.Contains(content, "agent-platform-mcps:") {
		t.Fatalf("the patch on record already carries agent-platform-mcps:\n%s", content)
	}
	const inCluster = "http://mcp-kubernetes.mcp-kubernetes.svc:8080/mcp"
	ownHost, targetHost := "mcp-prometheus."+rowan+".acme.test", "mcp-kubernetes."+birch+".acme.test"
	list := "agent-platform-mcps:\n  mcpServers:\n"
	for _, u := range []string{inCluster, "https://" + ownHost + "/mcp", "https://" + targetHost + "/mcp"} {
		list += "    - url: " + u + "\n      timeout: 30\n"
	}
	st.ghs.addFiles(acmeConfigs, map[string]string{marker: content + "\n" + list})

	res := verifyRowan(t, c, rowan)
	if res.State != installations.StateEnabled || res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	// The in-cluster entry's url is own-mcp-kubernetes-url's, the rest of
	// the own entries own-mcp-servers', the target's entry federated-mcp-servers'.
	var diffs []verify.Difference
	for _, want := range []struct {
		id string
		n  int
	}{{"own-mcp-servers", 3}, {"own-mcp-kubernetes-url", 1}, {"federated-mcp-servers", 2}} {
		d := anyDimension(t, res, want.id)
		if d.Mark != verify.Planned || len(d.Differences) != want.n {
			t.Fatalf("%s %q: %+v", want.id, d.Mark, d.Differences)
		}
		diffs = append(diffs, d.Differences...)
	}
	for _, diff := range diffs {
		entry, _, _ := strings.Cut(strings.TrimPrefix(diff.Path, "agent-platform-mcps.mcpServers["), "]")
		m19 := strings.HasSuffix(diff.Planned, "· M19")
		switch entry {
		case inCluster:
			if m19 || !strings.Contains(diff.Planned, "this mcp-kubernetes entry repeats the in-cluster one") {
				t.Errorf("the in-cluster entry: %+v", diff)
			}
		case "https://" + ownHost + "/mcp":
			if m19 || !strings.Contains(diff.Planned, "this mcp-prometheus entry at "+ownHost) {
				t.Errorf("the entry on the installation's own host: %+v", diff)
			}
		case "https://" + targetHost + "/mcp":
			if !m19 {
				t.Errorf("the entry on another installation's host: %+v", diff)
			}
		default:
			t.Errorf("a difference of no entry: %+v", diff)
		}
	}
}

// The registrations an installation's record carries beyond the platform's
// own — MCPServer objects under extras/agent-platform/mcpservers, a client
// with a stable callback under extras/agent-platform/mcpclients, both listed
// by their kustomizations and the tree's — as they land in rowan's tree.
const (
	rowanRegisteredServer = "pond-mcp-timescale"
	rowanRegisteredClient = "hosted-agent-runtime"
	rowanClientCallback   = "https://agents.runtime.test/identities/oauth2/callback/0000"
)

func registerOnRowan(st *stack) {
	tree := "management-clusters/" + rowan + "/extras/agent-platform/"
	kustomization := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n"
	st.ghs.addFiles(acmeMCs, map[string]string{
		tree + "kustomization.yaml":            kustomization + "  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main\n  - ./secrets\n  - ./mcpservers\n  - ./mcpclients\n",
		tree + "mcpservers/kustomization.yaml": kustomization + "  - timescale.yaml\n",
		tree + "mcpservers/timescale.yaml": "apiVersion: muster.giantswarm.io/v1alpha1\nkind: MCPServer\nmetadata:\n  name: " + rowanRegisteredServer + "\n  namespace: agent-platform\nspec:\n  type: streamable-http\n  url: https://mcp-timescale.pond.acme.test/mcp\n" +
			"  auth:\n    type: oauth\n    forwardToken: false\n    tokenExchange:\n      enabled: true\n      dexTokenEndpoint: https://dex.pond.acme.test/token\n      connectorId: giantswarm-rowan\n",
		tree + "mcpclients/kustomization.yaml": kustomization + "  - runtime.yaml\n",
		tree + "mcpclients/runtime.yaml":       "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + rowanRegisteredClient + "\n  namespace: agent-platform\ndata:\n  redirectURIs: |\n    " + rowanClientCallback + "\n",
	})
}

// What the record registers with muster is a fact of the record: the
// MCPServer objects and the client ConfigMap on record read as
// installation.mcpServers and installation.mcpClients, muster's exchange
// client is allowed a private address because a registered server exchanges,
// the client's callback is muster's public-registration allowlist, both land
// in the patch the enable renders, and the comparison reads them as defined.
func TestVerifyCapabilityRegistrationsOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	registerOnRowan(st)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())

	res := verifyRowan(t, c, rowan)
	if res.State != installations.StateEnabled || res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 {
		t.Errorf("state %q summary %v", res.State, res.Summary)
	}
	inst, _ := res.Inputs.Values["installation"].(map[string]any)
	servers, _ := inst["mcpServers"].([]any)
	clients, _ := inst["mcpClients"].([]any)
	if len(servers) != 1 || len(clients) != 1 {
		t.Fatalf("the registrations on the inputs: servers %v clients %v", servers, clients)
	}
	if s, _ := servers[0].(map[string]any); s["name"] != rowanRegisteredServer || s["auth"] != "exchange" || s["dexTokenEndpoint"] != "https://dex.pond.acme.test/token" {
		t.Errorf("the registered server: %v", servers[0])
	}
	cl, _ := clients[0].(map[string]any)
	if uris, _ := cl["redirectURIs"].([]any); cl["name"] != rowanRegisteredClient || len(uris) != 1 || uris[0] != rowanClientCallback {
		t.Errorf("the registered client: %v", clients[0])
	}
	marker := installations.Capabilities()[0].EnabledMarker(rowan)
	st.ghs.mu.Lock()
	content := st.ghs.files[acmeConfigs][marker]
	st.ghs.mu.Unlock()
	for _, want := range []string{"allowPrivateIP: true", "trustedPublicRegistrationRedirectURIs:\n          - " + rowanClientCallback} {
		if !strings.Contains(content, want) {
			t.Errorf("the patch on record lacks %q:\n%s", want, content)
		}
	}
	for _, id := range []string{"muster-private-exchange", "muster-public-registration-redirect-uris"} {
		if d := anyDimension(t, res, id); d.Mark != verify.AsDefined {
			t.Errorf("%s: %s %+v", id, d.Mark, d.Differences)
		}
	}
	// The tree's kustomization keeps the registration directories: another owner's entries.
	extras := "management-clusters/" + rowan + "/extras/agent-platform/kustomization.yaml"
	st.ghs.mu.Lock()
	tree := st.ghs.files[acmeMCs][extras]
	st.ghs.mu.Unlock()
	if !strings.Contains(tree, "./mcpservers") || !strings.Contains(tree, "./mcpclients") {
		t.Errorf("the rendered tree kustomization dropped a registration directory:\n%s", tree)
	}
}
