package e2e

// The dry run of enable_capability and reconcile_capability over the
// invented registry, and get_action/list_actions over a seeded Action — every
// GitHub read as alice, the Action records with the manager's own account.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// The installations and values the scenarios name, as the fixtures do.
const (
	rowan, alder, birch = "rowan", "alder", "birch"
	acme, capz          = "acme", "capz"
	enabledKey          = "enabled"
	kagentKey           = "kagent"
	approvalsChannel    = "platform-approvals"
)

func dryRun(t *testing.T, c *client.Client, tool string, args map[string]any) (tools.CapabilityResult, string, bool) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args[tools.ArgDryRun] = true
	text, isErr := call(t, c, tool, args)
	var out tools.CapabilityResult
	if !isErr {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("decode: %v\n%s", err, text)
		}
	}
	return out, text, isErr
}

// minimalInputs are the choices the definition's schema requires and never
// chooses for the person: nothing enabled, no federation.
func minimalInputs(over map[string]any) map[string]any {
	in := map[string]any{"secrets": map[string]any{}, kagentKey: map[string]any{enabledKey: false}, "portal": map[string]any{enabledKey: false},
		"toolAccess": map[string]any{"agentManager": map[string]any{enabledKey: false}}, "federation": map[string]any{"targets": []any{}, "hubs": []any{}}}
	for k, v := range over {
		in[k] = v
	}
	return in
}

func findPlan(t *testing.T, out tools.CapabilityResult, name string) plan.Installation {
	t.Helper()
	for _, p := range out.Installations {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("%s is not in the plan: order %v, skipped %+v", name, out.Order, out.Skipped)
	return plan.Installation{}
}

// One opted-in installation without the capability: every file of the golden
// fileset is a create in the registry's repositories, the pull requests run
// configs before management-clusters, the secrets are names only.
func TestEnableCapabilityDryRunRendersOneInstallation(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan})
	if isErr {
		t.Fatal(text)
	}
	if bare := findPlan(t, out, rowan); !strings.Contains(bare.Refused, "'kagent'") || len(bare.Files) != 0 {
		t.Fatalf("without the choices the schema requires: refused %q, files %d", bare.Refused, len(bare.Files))
	}
	out, text, isErr = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	if out.Caller != alice || out.Hub != hub || !out.DryRun || len(out.Installations) != 1 || len(out.Skipped) != 0 || strings.Join(out.Order, ",") != rowan {
		t.Fatalf("answer: %s", text)
	}
	p := out.Installations[0]
	if p.State != installations.StateNotEnabled || p.CommitRefused != "" || p.Refused != "" || p.OptIn == nil || p.OptIn.State != installations.OptedIn {
		t.Fatalf("plan head: state %q commitRefused %q refused %q optIn %+v", p.State, p.CommitRefused, p.Refused, p.OptIn)
	}
	if len(p.Files) < 15 || p.Diff[plan.ChangeUpdate] != 1 || p.Diff[plan.ChangeCreate] != len(p.Files)-1 {
		t.Fatalf("files: %d, diff %v", len(p.Files), p.Diff)
	}
	for _, f := range p.Files {
		if f.Repository != acmeConfigs && f.Repository != acmeMCs {
			t.Fatalf("file %s lands in %s, not the registry's repositories", f.Path, f.Repository)
		}
		if f.Content == "" || strings.Contains(f.Content, "SUPPLIED(") {
			t.Fatalf("file %s: content %q", f.Path, f.Content)
		}
	}
	if len(out.PullRequests) != 2 || out.PullRequests[0].Repository != acmeConfigs || out.PullRequests[0].Order != 1 || out.PullRequests[1].Repository != acmeMCs || out.PullRequests[1].Order != 2 {
		t.Fatalf("pull requests: %+v", out.PullRequests)
	}
	inputs := p.Inputs["installation"].(map[string]any)
	if inputs["name"] != rowan || inputs["customer"] != acme || inputs["baseDomain"] != "rowan.acme.test" || inputs["chartLine"] != "3" {
		t.Fatalf("effective inputs: %v", inputs)
	}
	var names []string
	for _, g := range p.GeneratedSecrets {
		names = append(names, g.Name)
		if g.Kind == "" || g.Length == 0 || len(g.Files) == 0 {
			t.Fatalf("generated secret %+v", g)
		}
	}
	if joined := strings.Join(names, ","); !strings.Contains(joined, "muster-dex-client-secret") || !strings.Contains(joined, "muster-valkey-password") {
		t.Fatalf("generated secrets: %v", names)
	}
	var muster bool
	for _, d := range p.DexClients {
		muster = muster || (d.Client == "muster" && d.ID == "muster-rowan" && d.SecretRef == "dex-client-muster")
	}
	if !muster || len(p.SuppliedSecrets) != 0 || len(p.Probes) == 0 || len(p.Includes) == 0 || p.Includes[0].Repository != acmeMCs || p.Includes[0].List != plan.ListResources || p.Includes[0].Change != plan.ChangeUpdate {
		t.Fatalf("dex clients %+v, supplied %v, probes %d, includes %+v", p.DexClients, p.SuppliedSecrets, len(p.Probes), p.Includes)
	}
}

// An installation without the opt-in still gets its dry run; the answer says
// a commit would be refused and how the owners opt in.
func TestEnableCapabilityDryRunWithoutOptIn(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	out, text, isErr := dryRun(t, st.mcpClient(t, aliceToken), tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: alder, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, alder)
	if p.State != installations.StateNotOptedIn || !strings.Contains(p.CommitRefused, "not opted in") || !strings.Contains(p.CommitRefused, installations.OptInPath(alder)) || len(p.Files) == 0 || len(out.Skipped) != 0 {
		t.Fatalf("plan: state %q commitRefused %q files %d skipped %+v", p.State, p.CommitRefused, len(p.Files), out.Skipped)
	}
}

// Typed inputs lay over the record; a supplied secret is a field name and a
// marker; an unknown key and an input the definition does not render refuse
// with their names, as the plan's answer.
func TestEnableCapabilityDryRunTypedInputs(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan,
		tools.ArgInputs: minimalInputs(map[string]any{"installation": map[string]any{"provider": capz}, kagentKey: map[string]any{enabledKey: true, modelKeySecretKey: managedModelKey}})})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" || p.Inputs["installation"].(map[string]any)["provider"] != capz || strings.Join(p.SuppliedSecrets, ",") != "kagent.modelKey" {
		t.Fatalf("plan: refused %q inputs %v supplied %v", p.Refused, p.Inputs["installation"], p.SuppliedSecrets)
	}
	var marker, kagent bool
	for _, f := range p.Files {
		marker = marker || strings.Contains(f.Content, "SUPPLIED(kagent.modelKey)")
	}
	for _, d := range p.DexClients {
		kagent = kagent || (d.ID == kagentKey && d.SecretRef == "dex-client-kagent" && len(d.RedirectURIs) == 1 && strings.HasSuffix(d.RedirectURIs[0], "/oauth2/callback"))
	}
	if !marker || !kagent {
		t.Fatalf("marker %v, kagent client %v: %+v", marker, kagent, p.DexClients)
	}

	out, text, isErr = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{kagentKey: map[string]any{enabledKey: false, "bogus": true}})})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, out, rowan); !strings.Contains(p.Refused, "bogus") || len(p.Files) != 0 || len(out.PullRequests) != 0 {
		t.Fatalf("unknown key: refused %q files %d prs %d", p.Refused, len(p.Files), len(out.PullRequests))
	}
	out, text, isErr = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{"federation": map[string]any{"targets": []any{map[string]any{"installation": alder, "private": false, "groups": []any{"kubernetes"}}}, "hubs": []any{}}})})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, out, rowan); !strings.Contains(p.Refused, "not rendered") {
		t.Fatalf("not rendered: %q", p.Refused)
	}
	if text, isErr := call(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgDryRun: true}); !isErr || !strings.Contains(text, "needs installation") {
		t.Fatalf("without an installation: %s", text)
	}
}

// The set: every opted-in, readable installation is rendered in the wave's
// order (the hub first here: no test installation of its customer), the rest
// is skipped with the reason; the hub's existing marker is an update.
func TestReconcileCapabilityDryRunOverTheSet(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	out, text, isErr := dryRun(t, st.mcpClient(t, aliceToken), tools.ToolReconcileCapability, map[string]any{tools.ArgContent: false, tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	if strings.Join(out.Order, ",") != "hazel,birch,rowan" {
		t.Fatalf("order: %v", out.Order)
	}
	skipped := map[string]string{}
	for _, s := range out.Skipped {
		skipped[s.Name] = s.Reason
	}
	if len(skipped) != 4 || skipped[alder] != tools.SkippedNotOptedIn || skipped["willow"] != tools.SkippedNotOptedIn || skipped["oak"] != tools.SkippedUnreadable || skipped["larch"] != tools.SkippedNoRepositories {
		t.Fatalf("skipped: %+v", out.Skipped)
	}
	hazel := findPlan(t, out, hub)
	if hazel.Diff[plan.ChangeUpdate] != 1 || hazel.Diff[plan.ChangeUnchanged] != 1 || hazel.Diff[plan.ChangeCreate] != len(hazel.Files)-2 || hazel.Files[0].Content != "" {
		t.Fatalf("hazel diff %v, first file %+v", hazel.Diff, hazel.Files[0])
	}
	seen := map[string]int{}
	for _, pr := range out.PullRequests {
		seen[pr.Repository] = pr.Order
	}
	if seen[acmeConfigs] >= seen[acmeMCs] || seen[hubConfigs] >= seen[hubMCs] || len(out.PullRequests) != 4 {
		t.Fatalf("pull requests: %+v", out.PullRequests)
	}
}

// A seeded Action is read back by get_action and list_actions, and
// list_installations carries it as the installation's last action with the
// action's state standing over the repositories'.
func TestGetActionOnASeededAction(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	seeded := actions.Action{Name: "enable-rowan-1", Namespace: actionsNamespace, CreatedAt: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC),
		Spec: actions.Spec{Actor: actions.Actor{Login: alice}, Capability: installations.AgentPlatform, Installations: []string{rowan}, Kind: "enable", Inputs: map[string]any{kagentKey: map[string]any{enabledKey: true}}},
		Status: actions.Status{State: string(installations.StatePendingApproval), PullRequests: []actions.PullRequest{{Repository: acmeConfigs, Number: 12, URL: "https://github.com/" + acmeConfigs + "/pull/12", State: "open"}},
			Approval: &actions.Approval{Channel: approvalsChannel, ReviewID: "rev-1"}}}
	if _, err := st.dyn.Resource(actions.GVR).Namespace(actionsNamespace).Create(context.Background(), actions.Unstructured(seeded), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	c := st.mcpClient(t, aliceToken)
	text, isErr := call(t, c, tools.ToolGetAction, map[string]any{tools.ArgName: seeded.Name})
	if isErr {
		t.Fatal(text)
	}
	var got actions.Action
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != seeded.Name || got.Spec.Actor.Login != alice || got.Spec.Installations[0] != rowan || got.Status.State != "pending approval" ||
		len(got.Status.PullRequests) != 1 || got.Status.PullRequests[0].Number != 12 || got.Status.Approval == nil || got.Status.Approval.ReviewID != "rev-1" || got.Spec.Inputs[kagentKey] == nil {
		t.Fatalf("action: %s", text)
	}
	if text, isErr := call(t, c, tools.ToolGetAction, map[string]any{tools.ArgName: "never"}); !isErr || !strings.Contains(text, "not found") {
		t.Fatalf("unknown action: %s", text)
	}
	var list tools.ListActionsResult
	if text, isErr := call(t, c, tools.ToolListActions, map[string]any{tools.ArgInstallation: rowan}); isErr || json.Unmarshal([]byte(text), &list) != nil || len(list.Actions) != 1 || list.Namespace != actionsNamespace {
		t.Fatalf("list for rowan: %s", text)
	}
	if text, isErr := call(t, c, tools.ToolListActions, map[string]any{tools.ArgInstallation: birch}); isErr || json.Unmarshal([]byte(text), &list) != nil || len(list.Actions) != 0 {
		t.Fatalf("list for birch: %s", text)
	}
	out, text, isErr := listInstallations(t, c, map[string]any{tools.ArgInstallations: []string{rowan, birch}})
	if isErr {
		t.Fatal(text)
	}
	rowan, birch := find(t, out, rowan).Capabilities[0], find(t, out, birch).Capabilities[0]
	if rowan.LastAction == nil || rowan.LastAction.Name != seeded.Name || rowan.State != installations.StatePendingApproval || birch.LastAction != nil || birch.State != installations.StateEnabled {
		t.Fatalf("last actions: rowan %+v, birch %+v", rowan, birch)
	}
}
