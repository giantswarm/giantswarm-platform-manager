package e2e

// The dry run of enable_capability and reconcile_capability over the
// invented registry, and get_action/list_actions over a seeded Action — every
// GitHub read as alice, the Action records with the manager's own account.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// The installations and values the scenarios name, as the fixtures do.
const (
	rowan, alder, birch = "rowan", "alder", "birch"
	acme, capz          = "acme", "capz"
	enabledKey          = "enabled"
	kagentKey           = "kagent"
	portalKey           = "portal"
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
	in := map[string]any{}
	for k, v := range over {
		in[k] = v
	}
	return in
}

func findPlan(t *testing.T, out tools.CapabilityResult, name string) plan.Installation {
	t.Helper()
	return findEntry(t, out, name).Installation
}

// findEntry is the dry run's entry for name: the plan with its marks.
func findEntry(t *testing.T, out tools.CapabilityResult, name string) tools.DryRun {
	t.Helper()
	for _, p := range out.Installations {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("%s is not in the plan: order %v, skipped %+v", name, out.Order, out.Skipped)
	return tools.DryRun{}
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
	if bare := findPlan(t, out, rowan); bare.Refused != "" || len(bare.Files) == 0 {
		t.Fatalf("the record alone renders (the one choice defaults): refused %q, files %d", bare.Refused, len(bare.Files))
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
	// The entry carries the comparison's marks: the same computation as
	// verify_capability, grouped as the plan.
	if len(p.Features) == 0 || p.Summary[verify.AsDefined]+p.Summary[verify.DiffersByInput]+p.Summary[verify.Drifted] == 0 {
		t.Fatalf("marks: %v, %d features", p.Summary, len(p.Features))
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

// The customer-portal definition is in the registry, so the capability tools
// take it as they take agent-platform: the plan rendered from the portal's
// schema over the facts the schema names (the registry's region and pipeline,
// the agent-platform capability's enabled state; not the chart line), its
// files the portal's tree, its Dex client the portal's, its plugin signing
// keys one ES256 pair; the verify answers for it: without the portal on
// record the tunnel alone reads back (off, its file absent) and the
// comparison names the choices not on record, refusing nothing.
func TestCapabilityToolsTakeTheCustomerPortal(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	inputs := rowanPortalInputs(map[string]any{enabledKey: false})
	inputs[portalKey].(map[string]any)["supportUrl"] = "https://support.acme.test/"
	for _, tool := range []string{tools.ToolEnableCapability, tools.ToolReconcileCapability} {
		out, text, isErr := dryRun(t, c, tool, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal, tools.ArgInputs: inputs})
		if isErr {
			t.Fatal(text)
		}
		p := findPlan(t, out, rowan)
		if out.Capability != installations.CustomerPortal || p.Refused != "" || p.State != installations.StateNotEnabled {
			t.Fatalf("%s: capability %q, refused %q, state %q", tool, out.Capability, p.Refused, p.State)
		}
		facts, _ := p.Inputs["installation"].(map[string]any)
		if _, chartLine := facts["chartLine"]; chartLine || facts["agentPlatform"] != false || facts["region"] != "eu-central-1" || facts["pipeline"] != "stable" {
			t.Fatalf("%s: facts %v", tool, facts)
		}
		var portalFiles, dexPatch int
		for _, f := range p.Files {
			if strings.Contains(f.Path, "/extras/backstage/") {
				portalFiles++
			}
			if strings.HasSuffix(f.Path, "/apps/dex-app/configmap-values.yaml.patch") {
				dexPatch++
			}
		}
		var pair bool
		for _, g := range p.GeneratedSecrets {
			pair = pair || (g.Name == "backstage-plugin-keys" && g.Kind == "keypair-es256" && g.Length == 0 && len(g.Files) == 1)
		}
		var backstage bool
		for _, d := range p.DexClients {
			backstage = backstage || (d.ID == "backstage" && d.SecretRef == "dex-client-backstage" && len(d.RedirectURIs) > 0)
		}
		if portalFiles == 0 || dexPatch != 1 || !pair || !backstage || len(p.Probes) == 0 || len(p.CustomerActions) != 0 {
			t.Fatalf("%s: portal files %d, dex patch %d, key pair %v, backstage client %v, probes %d, actions %d", tool, portalFiles, dexPatch, pair, backstage, len(p.Probes), len(p.CustomerActions))
		}
	}
	text, isErr := call(t, c, tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal})
	if isErr || !strings.Contains(text, `"capability": "`+installations.CustomerPortal+`"`) || !strings.Contains(text, `"source": "`+verify.Source(true, false)+`"`) || strings.Contains(text, `"refused": "`) {
		t.Fatalf("verify customer-portal: isErr %v, %s", isErr, text)
	}
	var res verify.Result
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, text)
	}
	want := []string{"chart.line", "plugins.flux.enabled", "plugins.github.enabled", "plugins.grafana.enabled", "plugins.sentry.enabled", "portal.domain", "portal.organization"}
	if !slices.Equal(res.Inputs.Missing, want) || !strings.Contains(res.CommitRefused, "portal.domain") {
		t.Fatalf("missing %v, commit refused %q", res.Inputs.Missing, res.CommitRefused)
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
// marker; an unknown key and a hub without the connector its targets trust it
// through refuse with their names, as the plan's answer.
func TestEnableCapabilityDryRunTypedInputs(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan,
		tools.ArgInputs: minimalInputs(map[string]any{argInstallation: map[string]any{"provider": capz}})})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" || p.Inputs["installation"].(map[string]any)["provider"] != capz || strings.Join(p.SuppliedSecrets, ",") != suppliedFields {
		t.Fatalf("plan: refused %q inputs %v supplied %v", p.Refused, p.Inputs["installation"], p.SuppliedSecrets)
	}
	var marker, kagent bool
	for _, f := range p.Files {
		marker = marker || strings.Contains(f.Content, "SUPPLIED(kagent.modelKey)")
	}
	for _, d := range p.DexClients {
		kagent = kagent || (d.ID == kagentKey && d.SecretRef == "dex-client-kagent" && len(d.RedirectURIs) == 1 && strings.HasSuffix(d.RedirectURIs[0], "/oauth2/callback"))
	}
	// The model key is the installation's own: no supplied value, no marker; the kagent client is rendered all the same.
	if marker || !kagent {
		t.Fatalf("marker %v, kagent client %v: %+v", marker, kagent, p.DexClients)
	}

	out, text, isErr = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{"modelServing": map[string]any{enabledKey: false, "bogus": true}})})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, out, rowan); !strings.Contains(p.Refused, "bogus") || len(p.Files) != 0 || len(out.PullRequests) != 0 {
		t.Fatalf("unknown key: refused %q files %d prs %d", p.Refused, len(p.Files), len(out.PullRequests))
	}
	out, text, isErr = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: minimalInputs(map[string]any{argInstallation: map[string]any{"federation": map[string]any{"targets": []any{map[string]any{"installation": alder, "baseDomain": alder + ".example", argPrivate: false}}, "hubs": []any{}}}})})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, out, rowan); !strings.Contains(p.Refused, "federation.brokerClientId") || len(p.Files) != 0 {
		t.Fatalf("hub without a broker client on record: refused %q files %d", p.Refused, len(p.Files))
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
	// maple's fileset on record does not open the gate: without the declaration it is skipped like alder.
	if len(skipped) != 5 || skipped[alder] != tools.SkippedNotOptedIn || skipped[maple] != tools.SkippedNotOptedIn || skipped["willow"] != tools.SkippedNotOptedIn || skipped["oak"] != tools.SkippedUnreadable || skipped["larch"] != tools.SkippedNoRepositories {
		t.Fatalf("skipped: %+v", out.Skipped)
	}
	hazel := findPlan(t, out, hub)
	// The updates: the hub's patch, its dex patch (the portal's client on record) and its portal tree's kustomization, which the platform's Component joins.
	if hazel.Diff[plan.ChangeUpdate] != 3 || hazel.Diff[plan.ChangeUnchanged] != 1 || hazel.Diff[plan.ChangeCreate] != len(hazel.Files)-4 || hazel.Files[0].Content != "" {
		t.Fatalf("hazel diff %v, first file %+v", hazel.Diff, hazel.Files[0])
	}
	// The portal's client id is a fact from the hub's Dex patch, on every
	// installation the portal lists; what an installation's own patches trust
	// besides is no fact (the plan keeps it, TestDryRunKeepsEachAudienceListsOwnEntries).
	birchFacts := findPlan(t, out, privateFixture).Inputs["installation"].(map[string]any)
	portal, _ := birchFacts["portals"].([]any)[0].(map[string]any)
	if _, has := birchFacts["portalAudiences"]; portal["clientId"] != hubPortalClientID || has {
		t.Fatalf("birch portals %v, facts %v", birchFacts["portals"], birchFacts)
	}
	seen := map[string]int{}
	for _, pr := range out.PullRequests {
		seen[pr.Repository] = pr.Order
	}
	if seen[acmeConfigs] >= seen[acmeMCs] || seen[hubConfigs] >= seen[hubMCs] || len(out.PullRequests) != 4 {
		t.Fatalf("pull requests: %+v", out.PullRequests)
	}
}

// Each audience list keeps its own entries on record, after the definition's
// and in no other list: the id the hub's kagent UI accepts alone stays in
// oidc-extra-audience, the peer its authenticator trusts alone in
// trustedPeers, the client birch's muster trusts alone in trustedAudiences —
// each named on the file — and the comparison, which follows the plan, finds
// no difference for the hub's.
func TestDryRunKeepsEachAudienceListsOwnEntries(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInputs: minimalInputs(nil)})
	if isErr {
		t.Fatal(text)
	}
	file := func(p plan.Installation, suffix string) plan.File {
		for _, f := range p.Files {
			if strings.HasSuffix(f.Path, suffix) {
				return f
			}
		}
		t.Fatalf("%s: no file ends in %s", p.Name, suffix)
		return plan.File{}
	}
	platformPatch, dexPatch := "/apps/"+installations.AgentPlatform+"/configmap-values.yaml.patch", "/apps/dex-app/configmap-values.yaml.patch"
	hazel := findPlan(t, out, hub)
	patch, dex := file(hazel, platformPatch), file(hazel, dexPatch)
	// The hub's dex patch keeps the portal's client on record too: the definition declares backstage, not its id.
	if !slices.Equal(patch.Kept, []plan.Kept{{List: plan.ListExtraAudience, Entry: hubExtraAudienceID}}) || !slices.Equal(dex.Kept, []plan.Kept{{List: plan.ListTrustedPeers, Entry: hubPeerClientID}, {List: "oidc.extraStaticClients", Entry: hubPortalClientID}}) {
		t.Fatalf("hazel kept: patch %v, dex %v", patch.Kept, dex.Kept)
	}
	if !strings.Contains(patch.Content, "oidc-extra-audience: dex-k8s-authenticator,kagent,"+hubPortalClientID+",backstage,"+hubExtraAudienceID+" # gitleaks:allow\n") || strings.Count(patch.Content, hubExtraAudienceID) != 1 || strings.Contains(patch.Content, hubPeerClientID) {
		t.Errorf("hazel platform patch:\n%s", patch.Content)
	}
	if !strings.Contains(dex.Content, "trustedPeers:\n        - "+hubPortalClientID+"\n        - backstage\n        - "+hubPeerClientID+"\n") || strings.Count(dex.Content, hubPeerClientID) != 1 || strings.Contains(dex.Content, hubExtraAudienceID) {
		t.Errorf("hazel dex patch:\n%s", dex.Content)
	}
	// birch's authenticator trusts itself on record: that stays as well.
	birch := findPlan(t, out, privateFixture)
	patch, dex = file(birch, platformPatch), file(birch, dexPatch)
	if !slices.Equal(patch.Kept, []plan.Kept{{List: plan.ListTrustedAudiences, Entry: birchPortalClientID}}) || !slices.Equal(dex.Kept, []plan.Kept{{List: plan.ListTrustedPeers, Entry: "dex-k8s-authenticator"}, {List: plan.ListTrustedPeers, Entry: birchPeerClientID}}) {
		t.Fatalf("birch kept: patch %v, dex %v", patch.Kept, dex.Kept)
	}
	if strings.Count(patch.Content, birchPortalClientID) != 1 || strings.Contains(patch.Content, birchPeerClientID) || strings.Count(dex.Content, birchPeerClientID) != 1 || strings.Contains(dex.Content, birchPortalClientID) {
		t.Errorf("birch platform patch:\n%s\nbirch dex patch:\n%s", patch.Content, dex.Content)
	}
	res := verifyWith(t, c, hub, nil)
	for _, f := range res.Features {
		for _, d := range f.Dimensions {
			for _, diff := range d.Differences {
				if strings.Contains(diff.Rendered+diff.Current, hubExtraAudienceID) || strings.Contains(diff.Rendered+diff.Current, hubPeerClientID) {
					t.Errorf("%s/%s: a kept id is a difference: %+v", f.ID, d.ID, diff)
				}
			}
		}
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

// The 4 line's prerequisite: kagent's Agent Substrate needs a cluster that
// serves PodCertificateRequest, and the record says whether it does — the
// cluster App's values in the management-clusters repository enable the
// three feature gates, or its chart does by default. rowan's manifest is on
// record without them (cluster-aws 10.2.0, no gates), so its record reads
// false and the 3 line renders regardless; typed onto the 4 line it is refused
// at plan time naming the fact, the gates, the path and the charts, and the
// dry run says a commit would be refused. The hub's manifest carries the
// gates: its record reads true and the 4 line renders with the probe of the
// API among the runtime feature's.
func TestEnableCapabilityRefusesTheFourLineWithoutPodCertificateRequest(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, rowan)
	if p.Refused != "" || p.Inputs["installation"].(map[string]any)["podCertificateRequest"] != false {
		t.Fatalf("rowan on the 3 line renders with the record saying no: refused %q, inputs %v", p.Refused, p.Inputs["installation"])
	}
	out, text, isErr = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: rowan,
		tools.ArgInputs: map[string]any{argInstallation: map[string]any{"chartLine": "4"}}})
	if isErr {
		t.Fatal(text)
	}
	p = findPlan(t, out, rowan)
	for _, want := range []string{"installation.podCertificateRequest", "certificates.k8s.io/v1beta1 podcertificaterequests",
		"PodCertificateRequest, ClusterTrustBundle, ClusterTrustBundleProjection", "controlPlane.apiServer,controlPlane.controllerManager,kubelet",
		installations.ClusterAppManifestPath(rowan), "cluster-aws 10.3.0, cluster-azure 9.3.0, cluster-cloud-director 7.3.0"} {
		if !strings.Contains(p.Refused, want) {
			t.Errorf("refused %q does not name %q", p.Refused, want)
		}
	}
	if p.CommitRefused == "" || !strings.Contains(p.CommitRefused, "refuses these inputs") || len(p.Files) != 0 || len(out.PullRequests) != 0 {
		t.Fatalf("a refused render commits nothing: commitRefused %q, files %d, pull requests %d", p.CommitRefused, len(p.Files), len(out.PullRequests))
	}
	out, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{hub}})
	if isErr {
		t.Fatal(text)
	}
	p = findPlan(t, out, hub)
	if p.Refused != "" || p.Inputs["installation"].(map[string]any)["podCertificateRequest"] != true || p.Inputs["installation"].(map[string]any)["chartLine"] != "4" {
		t.Fatalf("the hub's record carries the gates: refused %q, inputs %v", p.Refused, p.Inputs["installation"])
	}
	var probed bool
	for _, pr := range p.Probes {
		probed = probed || (pr.ID == "live-pod-certificate-request" && pr.Feature == "runtime")
	}
	if !probed {
		t.Errorf("the 4 line's plan names the API's live dimension: %+v", p.Probes)
	}
}
