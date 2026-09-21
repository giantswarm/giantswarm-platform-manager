package e2e

// verify_capability over the invented registry: the three marks and the
// roll-up, the probes, and the live dimensions reported as not checked.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
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
	args := map[string]any{tools.ArgInstallation: name}
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
	if len(res.Files) == 0 || res.Diff[plan.ChangeUnchanged] != len(res.Files) || len(res.PullRequests) != 0 || res.CommitRefused != "" || res.OptIn == nil || res.OptIn.State != installations.OptedIn || len(res.Probes) == 0 {
		t.Errorf("plan view: %d files, diff %v, %d pull requests, commit refused %q, opt-in %+v, %d probes", len(res.Files), res.Diff, len(res.PullRequests), res.CommitRefused, res.OptIn, len(res.Probes))
	}
	if res.Files[0].Content != "" {
		t.Errorf("content without asking: %q", res.Files[0].Content)
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
			if !strings.HasPrefix(diff.Planned, "M5 ") || diff.Current != "" || diff.Rendered == "" {
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
	for _, m := range []string{"M5 — kagent", "M5 — muster and the kagent UI trust the authenticator", "M30 — the portals'"} {
		if !strings.Contains(diff.Planned, m) {
			t.Errorf("planned %q lacks %q", diff.Planned, m)
		}
	}
}

// The repositories express another value of an input than the one on record:
// every difference names the input, nothing is drift, the feature differs by input.
func TestVerifyCapabilityDiffersByInput(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	inRepos := kagentEnabled()
	inRepos["installation"] = map[string]any{argPrivate: true}
	enableRowan(t, st, c, kagentEnabled(), inRepos)

	res := verifyRowan(t, c, rowan)
	if res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] == 0 || res.State != installations.StateEnabled {
		t.Fatalf("state %q summary %v", res.State, res.Summary)
	}
	differs := 0
	for _, f := range res.Features {
		if f.Mark == verify.DiffersByInput {
			differs++
		}
		for _, d := range f.Dimensions {
			for _, diff := range d.Differences {
				if diff.Input != privateInput {
					t.Errorf("%s/%s: %s#%s is %q, want %s", f.ID, d.ID, diff.File, diff.Path, diff.Input, privateInput)
				}
			}
		}
	}
	if differs == 0 {
		t.Errorf("no feature differs by input: %v", res.Summary)
	}
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
	if res.Inputs.Source != verify.SourceRecord || res.State != installations.StateNotOptedIn || res.Refused != "" {
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
// paths that differ, the values redacted: no value of it is shown.
func TestVerifyCapabilityRedactsAnEncryptedFile(t *testing.T) {
	st := newStack(t)
	c := enabledOnRecord(t, st)
	repo, path, content := onRecord(t, st, "/secrets/kagent-oauth2-proxy-credentials.yaml")
	st.ghs.addFiles(repo, map[string]string{path: content + "handEdited: true\n"})

	res := verifyRowan(t, c, rowan)
	d := dimension(t, feature(t, res, "secrets"), "oauth2-proxy-credentials-secret")
	if d.Mark != verify.Drifted || res.Summary[verify.Drifted] != 1 || len(d.Differences) != 1 {
		t.Fatalf("oauth2-proxy-credentials-secret: %+v summary %v", d, res.Summary)
	}
	if diff := d.Differences[0]; diff.File != repo+":"+path || diff.Path != "handEdited" || diff.Rendered != "" || diff.Current != verify.Redacted || diff.Input != "" {
		t.Errorf("the difference: %+v", diff)
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

// A portal on record with the grafana plugin on and no domain in its
// app-config: the plugin reads back on, the domain is a choice not on
// record. The comparison refuses nothing — the app-config's leaf carries the
// Missing marker, its dimension is not checked with the reason naming the
// choice, inputs.missing names it, nothing drifts — and neither does the
// dry run, which says a commit would be refused; a commit is refused by
// field and records nothing. The choice typed, the portal compares whole.
func TestVerifyCapabilityTakesAMissingChoiceAsNotChecked(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	const grafanaDomain, domain = "plugins.grafana.domain", "https://grafana.rowan.acme.test"
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal, tools.ArgInputs: rowanPortalInputs(map[string]any{enabledKey: true, domainKey: domain})})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" || len(p.MissingInputs) != 0 || p.CommitRefused != "" {
		t.Fatalf("the portal as typed: refused %q missing %v commit refused %q", p.Refused, p.MissingInputs, p.CommitRefused)
	}
	var appConfig string
	for _, f := range p.Files {
		content := f.Content
		if f.Path == installations.PortalConfigPath(rowan) {
			appConfig = f.Repository + ":" + f.Path
			content = strings.Replace(content, "        grafana:\n          domain: "+domain+"\n", "        grafana: {}\n", 1)
			if content == f.Content {
				t.Fatalf("no grafana section to drop from the app-config:\n%s", content)
			}
		}
		st.ghs.addFile(f.Repository, f.Path, content)
	}

	res := verifyPortal(t, c, rowan, nil)
	if res.Refused != "" || res.Inputs.ReadBack["plugins.grafana.enabled"] != true || !slices.Equal(res.Inputs.Missing, []string{grafanaDomain}) {
		t.Fatalf("refused %q read back %v missing %v", res.Refused, res.Inputs.ReadBack, res.Inputs.Missing)
	}
	if res.Summary[verify.Drifted] != 0 || res.Summary[verify.DiffersByInput] != 0 || res.State != installations.StateEnabled || !strings.Contains(res.CommitRefused, grafanaDomain) {
		t.Fatalf("summary %v state %q commit refused %q", res.Summary, res.State, res.CommitRefused)
	}
	want := verify.ReasonMissingChoice + ": " + grafanaDomain
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
	if p = findPlan(t, out, rowan); p.Refused != "" || !slices.Equal(p.MissingInputs, []string{grafanaDomain}) || !strings.Contains(p.CommitRefused, grafanaDomain) {
		t.Fatalf("dry run: refused %q missing %v commit refused %q", p.Refused, p.MissingInputs, p.CommitRefused)
	}
	if _, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal}); !isErr || !strings.Contains(text, grafanaDomain) {
		t.Fatalf("a commit without the choice: %v %s", isErr, text)
	}
	if got := listActionsOf(t, c, rowan); len(got) != 0 {
		t.Fatalf("a refused commit recorded %d action(s)", len(got))
	}

	res = verifyPortal(t, c, rowan, map[string]any{"plugins": map[string]any{grafanaKey: map[string]any{domainKey: domain}}})
	if res.Refused != "" || len(res.Inputs.Missing) != 0 || res.Summary[verify.Drifted] != 0 || res.CommitRefused != "" {
		t.Fatalf("the choice typed: refused %q missing %v summary %v commit refused %q", res.Refused, res.Inputs.Missing, res.Summary, res.CommitRefused)
	}
	if d := dimension(t, feature(t, res, "portal"), notChecked[0]); d.Mark != verify.DiffersByInput || d.Reason != "" || len(d.Differences) != 1 || d.Differences[0].File != appConfig || d.Differences[0].Input == "" {
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
