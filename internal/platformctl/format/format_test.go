package format

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/muster"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// Invented installations and repositories, as in the manager's own fixtures.
const (
	hazel         = "hazel"
	rowan         = "rowan"
	oak           = "oak"
	agentPlatform = installations.AgentPlatform
	enabled       = "enabled"
	acmeConfigs   = "giantswarm/acme-configs"
	acmeMCs       = "giantswarm/acme-management-clusters"
	namespace     = "platform-manager"
	someone       = "someone"
	rowanAction   = "enable-rowan-abc123"
	kagent        = "kagent"
	// dexClientKagent names the Secret a Dex client's secretRef points at.
	dexClientKagent = "dex-client-kagent"
)

func contains(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestInstallationsTable(t *testing.T) {
	yes := true
	r := tools.ListInstallationsResult{
		Caller: someone, Hub: hazel,
		Registry:     tools.RegistryInfo{Catalog: installations.Location{Repository: "giantswarm/github", Path: "catalog/installations.yaml"}},
		Capabilities: []string{agentPlatform},
		Installations: []installations.Report{
			{Installation: installations.Installation{Name: hazel, Customer: "acme", Provider: "capa", Hub: true},
				OptIn:        &installations.OptIn{State: installations.OptedIn, Value: &yes},
				Capabilities: []installations.CapabilityState{{Name: agentPlatform, State: installations.StateEnabled, LastAction: &installations.ActionRef{Name: "enable-1", Result: enabled}}}},
			{Installation: installations.Installation{Name: oak}, Errors: []string{"403 as you"}},
		},
		Unreadable: []string{oak},
	}
	var b bytes.Buffer
	if err := Installations(&b, r); err != nil {
		t.Fatal(err)
	}
	contains(t, b.String(), "Caller: someone   Hub: hazel   Registry: giantswarm/github:catalog/installations.yaml",
		"NAME", "AGENT-PLATFORM", "LAST ACTION",
		hazel, "acme", "capa", "yes", "opted in", enabled, "enable-1 (enabled)",
		oak, "unreadable", "unknown",
		"Unreadable as you: oak", "  oak: 403 as you")
}

func TestPlan(t *testing.T) {
	r := tools.CapabilityResult{
		Caller: someone, Tool: tools.ToolEnableCapability, Capability: agentPlatform, Hub: hazel, DryRun: true,
		Order: []string{rowan},
		Installations: []plan.Installation{{
			Name: rowan, State: installations.StateNotEnabled, OptIn: &installations.OptIn{State: installations.OptedIn},
			CommitRefused: "the commit is not in this version",
			Files: []plan.File{
				{Repository: acmeConfigs, Path: "installations/rowan/apps/agent-platform/configmap-values.yaml.patch", Change: plan.ChangeCreate, Content: "a: 1\n"},
				{Repository: acmeMCs, Path: "management-clusters/rowan/extras/agent-platform/kustomization.yaml", Change: plan.ChangeUnchanged},
			},
			Includes:         []plan.Include{{Repository: acmeMCs, Path: "management-clusters/rowan/extras/kustomization.yaml", List: plan.ListResources, Resource: "./agent-platform/", Change: plan.ChangeUpdate}},
			GeneratedSecrets: []plan.GeneratedSecret{{Name: "muster-valkey-password", Kind: "alphanumeric", Length: 32, Files: []string{"x", "y"}, FrozenIn: []string{"x"}, Rotates: true}},
			SuppliedSecrets:  []string{"kagent.modelKey"},
			DexClients:       []plan.DexClient{{ID: kagent, Client: kagent, SecretRef: dexClientKagent, RedirectURIs: []string{"https://kagent.rowan.example/callback"}}},
			CustomerActions:  []plan.CustomerAction{{Installation: rowan, Action: "create the apiKeySecret", Why: "the model key is theirs"}},
			Probes:           []plan.Probe{{ID: "muster-ready", Feature: "muster", Key: "ready"}},
			Diff:             map[plan.Change]int{plan.ChangeCreate: 1, plan.ChangeUnchanged: 1},
		}},
		PullRequests: []plan.PullRequest{{Order: 1, Repository: acmeConfigs, Installations: []string{rowan}, Changes: 1, GeneratedSecrets: []string{"muster-valkey-password"}}},
		Skipped:      []tools.Skipped{{Name: "alder", Reason: tools.SkippedNotOptedIn, OptIn: &installations.OptIn{HowToOptIn: "a PR by the owners"}}},
		Commit:       "not implemented yet",
	}
	var b bytes.Buffer
	if err := Plan(&b, r, false); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	contains(t, out, "enable_capability dry run: agent-platform on hub hazel, as someone", "Order: rowan",
		"rowan: not enabled (opted in)", "A commit would be refused: the commit is not in this version",
		"Files (1 create, 1 unchanged):", "    CHANGE", "create", "unchanged", "configmap-values.yaml.patch",
		"Includes: giantswarm/acme-management-clusters:management-clusters/rowan/extras/kustomization.yaml resources ./agent-platform/ (update)",
		"muster-valkey-password (alphanumeric, 32): x, y", "rotates: muster-valkey-password (x)", "You supply at commit: kagent.modelKey",
		"kagent: client kagent; secretRef dex-client-kagent; redirect URIs https://kagent.rowan.example/callback",
		"rowan: create the apiKeySecret (the model key is theirs)", "muster-ready: muster ready",
		"1. giantswarm/acme-configs: 1 change for rowan", "generated secrets: muster-valkey-password",
		"alder: not opted in", "how to opt in: a PR by the owners", "Commit: not implemented yet")
	if strings.Contains(out, "a: 1") {
		t.Error("content printed without --content")
	}
	b.Reset()
	if err := Plan(&b, r, true); err != nil {
		t.Fatal(err)
	}
	contains(t, b.String(), "--- giantswarm/acme-configs/installations/rowan/apps/agent-platform/configmap-values.yaml.patch (create)\na: 1\n")
}

func TestActions(t *testing.T) {
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	a := actions.Action{
		Name: "enable-rowan-1", Namespace: namespace, CreatedAt: time.Now().Add(-3 * time.Hour),
		Spec: actions.Spec{Actor: actions.Actor{Login: someone}, Capability: agentPlatform, Kind: "enable", Installations: []string{rowan}, Inputs: map[string]any{kagent: map[string]any{enabled: true}}},
		Status: actions.Status{State: string(installations.StatePendingApproval),
			PullRequests: []actions.PullRequest{{Repository: acmeConfigs, Number: 12, URL: "https://github.com/giantswarm/acme-configs/pull/12", State: "open"}},
			Approval:     &actions.Approval{Channel: "reviews", Decision: "approved", DecidedBy: "reviewer", At: &at, Reason: "fine"},
			Rollout:      &actions.Rollout{StartedAt: &at, Installations: []actions.InstallationRollout{{Name: rowan, State: string(installations.StateRollingOut)}}},
			Probes:       []actions.Probe{{ID: "muster-ready", Installation: rowan, Result: "pass"}},
			Result:       &actions.Result{State: enabled, Message: "all probes pass"}},
	}
	var b bytes.Buffer
	if err := Action(&b, a); err != nil {
		t.Fatal(err)
	}
	contains(t, b.String(), "Action enable-rowan-1 in platform-manager, created", "Kind: enable   Capability: agent-platform   Actor: someone",
		"Installations: rowan", "State: pending approval", "giantswarm/acme-configs#12 open https://github.com/giantswarm/acme-configs/pull/12",
		"Approval: approved by reviewer at 2026-09-18T12:00:00Z in reviews — fine", "Rollout: started at 2026-09-18T12:00:00Z, finished",
		"  rowan: rolling out", "muster-ready on rowan: pass", "Result: enabled — all probes pass", `"enabled": true`)

	b.Reset()
	if err := Actions(&b, tools.ListActionsResult{Namespace: namespace, Actions: []actions.Action{a}}); err != nil {
		t.Fatal(err)
	}
	contains(t, b.String(), "NAME", "AGE", "enable-rowan-1", "enable", agentPlatform, someone, "pending approval", rowan, "3h")

	b.Reset()
	if err := Actions(&b, tools.ListActionsResult{Namespace: namespace, Filter: actions.Filter{Installation: oak}}); err != nil {
		t.Fatal(err)
	}
	contains(t, b.String(), "No actions in platform-manager for installation oak.")
}

func TestJSONAndAuthRequired(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, json.RawMessage(`{"a":{"b":1}}`)); err != nil {
		t.Fatal(err)
	}
	if b.String() != "{\n  \"a\": {\n    \"b\": 1\n  }\n}\n" {
		t.Fatalf("got %q", b.String())
	}
	if err := JSON(&b, json.RawMessage(`not json`)); err == nil {
		t.Fatal("want an error for a non-JSON answer")
	}
	b.Reset()
	if err := AuthRequired(&b, &muster.AuthRequired{Server: muster.Server, URL: "https://example.test/login"}); err != nil {
		t.Fatal(err)
	}
	contains(t, b.String(), "Sign in required: giantswarm-platform-manager is not connected for you in muster.", "  https://example.test/login", "muster auth login --server giantswarm-platform-manager")
}

// TestPrinterKeepsTheFirstError: a closed pipe is one error, not a line each.
func TestPrinterKeepsTheFirstError(t *testing.T) {
	p := &printer{w: failing{}}
	p.f("a")
	p.f("b")
	p.table(0, [][]string{{"x"}})
	if p.err == nil || p.err.Error() != "closed" {
		t.Fatalf("got %v", p.err)
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errClosed }

var errClosed = &closedError{}

type closedError struct{}

func (*closedError) Error() string { return "closed" }

func TestCommitNamesTheActionAndItsPullRequests(t *testing.T) {
	r := tools.CommitResult{
		Caller: someone, Tool: tools.ToolEnableCapability, Capability: agentPlatform, Hub: hazel, Installation: rowan,
		Action: &actions.Action{Name: rowanAction, Status: actions.Status{State: actions.StatePendingApproval}},
		Plan:   plan.Installation{Name: rowan, State: enabled, SuppliedSecrets: []string{"kagent.modelKey"}},
		PullRequests: []actions.PullRequest{
			{Repository: acmeConfigs, Number: 7, URL: "https://github.com/" + acmeConfigs + "/pull/7"},
			{Repository: acmeMCs, Number: 8, URL: "https://github.com/" + acmeMCs + "/pull/8"},
		},
		UnchangedRepositories: []string{"giantswarm/acme-other"},
		Next:                  "the Team review decides",
	}
	var buf bytes.Buffer
	if err := Commit(&buf, r, false); err != nil {
		t.Fatal(err)
	}
	contains(t, buf.String(),
		"enable_capability commit: agent-platform on rowan (hub hazel), as someone",
		"Action: "+rowanAction+" (pending approval)",
		"1. "+acmeConfigs+"#7 https://github.com/"+acmeConfigs+"/pull/7",
		"2. "+acmeMCs+"#8",
		"Nothing to commit in: giantswarm/acme-other",
		"You supply at commit: kagent.modelKey",
		"Next: the Team review decides")
}

func TestVerifyPrintsFeaturesWithMarksAndDimensions(t *testing.T) {
	r := verify.Result{
		Caller: someone, Installation: rowan, Capability: agentPlatform, Hub: hazel, State: "drifted",
		Inputs:  verify.Inputs{Source: "action " + rowanAction},
		Summary: map[verify.Mark]int{verify.AsDefined: 2, verify.Drifted: 1, verify.NotChecked: 1},
		Features: []verify.Feature{
			{ID: kagent, Title: "kagent, the agent runtime", Mark: verify.Drifted, Marks: map[verify.Mark]int{verify.AsDefined: 1, verify.Drifted: 1, verify.NotChecked: 1},
				Dimensions: []verify.Dimension{
					{ID: "runtime/patch-top-level-keys", Kind: "configmap", Key: "patch top-level keys", Mark: verify.Drifted,
						Files:       []string{acmeConfigs + ":management-clusters/rowan/kagent.yaml"},
						Differences: []verify.Difference{{File: acmeConfigs + ":management-clusters/rowan/kagent.yaml", Path: "spec.values.replicas", Rendered: "1", Current: "3"}}},
					{ID: "runtime/private", Kind: "configmap", Key: "installation.private", Mark: verify.DiffersByInput,
						Differences: []verify.Difference{{File: acmeConfigs + ":x.yaml", Path: "spec.private", Input: "installation.private", Rendered: "false", Current: "true"}}},
					{ID: "kagent/live", Kind: "live", Key: "deployment", Mark: verify.NotChecked, Reason: verify.ReasonAuthority},
					{ID: "oauth2-proxy-gate", Kind: "probe", Key: "gate", Mark: verify.AsDefined,
						Probe: &verify.ProbeResult{Expect: []int{302, 403}, Requests: []verify.Request{{URL: "https://kagent.rowan.example/", Status: 302, OK: true}}}},
					{ID: "dex-auth-request", Kind: "probe", Key: "dex", Mark: verify.Drifted,
						Probe: &verify.ProbeResult{Expect: []int{302}, Requests: []verify.Request{{URL: "https://dex.rowan.example/auth", Client: kagent, Error: "dial tcp: timeout", OK: false}}}},
				}},
		},
	}
	var buf bytes.Buffer
	if err := Verify(&buf, r, nil); err != nil {
		t.Fatal(err)
	}
	contains(t, buf.String(),
		"verify agent-platform on rowan (hub hazel), as someone",
		"State: drifted   Inputs on record: action "+rowanAction,
		"Summary: 1 drifted, 2 as defined, 1 not checked",
		"kagent, the agent runtime: drifted (1 drifted, 1 as defined, 1 not checked)",
		"[drifted] runtime/patch-top-level-keys (configmap: patch top-level keys)",
		"in "+acmeConfigs+":management-clusters/rowan/kagent.yaml",
		"kagent.yaml spec.values.replicas: rendered \"1\", current \"3\" (drift)",
		"x.yaml spec.private: rendered \"false\", current \"true\" (input installation.private)",
		"[not checked] kagent/live (live: deployment) — "+verify.ReasonAuthority,
		"expect 302|403",
		"ok   https://kagent.rowan.example/ → 302",
		"FAIL https://dex.rowan.example/auth (kagent) — dial tcp: timeout")
}

func TestDecisionAndMergeCarryTheMessageAndTheAction(t *testing.T) {
	a := &actions.Action{Name: rowanAction, Spec: actions.Spec{Kind: actions.KindEnable, Capability: agentPlatform, Installations: []string{rowan}}, Status: actions.Status{State: actions.StatePendingApproval}}
	var buf bytes.Buffer
	if err := Decision(&buf, tools.Decision{Message: "approved by carol; the actor merges", Action: a}); err != nil {
		t.Fatal(err)
	}
	contains(t, buf.String(), "approved by carol; the actor merges", "Action "+rowanAction, "State: pending approval")

	buf.Reset()
	m := tools.MergeResult{Message: "one merged, one waiting", Action: a,
		Merged:  []actions.PullRequest{{Repository: acmeConfigs, Number: 7, URL: "https://github.com/" + acmeConfigs + "/pull/7"}},
		Waiting: acmeMCs + "#8: checks pending"}
	if err := Merge(&buf, m); err != nil {
		t.Fatal(err)
	}
	contains(t, buf.String(), "one merged, one waiting", "Merged by this call, in order:", acmeConfigs+"#7 https://", "Waiting: "+acmeMCs+"#8: checks pending", "Action "+rowanAction)

	buf.Reset()
	if err := Decision(&buf, tools.Decision{Message: "denied"}); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "denied\n" {
		t.Errorf("a decision without an action prints only the message, got %q", got)
	}
}

func TestWatchPrintsTheRolloutPictureAndTheReport(t *testing.T) {
	a := &actions.Action{Name: rowanAction, Spec: actions.Spec{Kind: actions.KindEnable, Capability: agentPlatform, Installations: []string{rowan}}, Status: actions.Status{State: actions.StateEnabled}}
	r := tools.WatchResult{Message: "rowan is enabled (action " + rowanAction + ", enabled): verified: 9 as defined", Action: a, Installation: rowan, State: actions.StateEnabled, Ready: true,
		Objects: []actions.RolloutObject{{Kind: "HelmRelease", Namespace: "flux-giantswarm", Name: "agent-platform", Ready: "True", Revision: "4.44.1"}, {Kind: "HelmRelease", Namespace: "flux-giantswarm", Name: "muster", Ready: "False", Message: "Ready=False: install retries exhausted"}},
		Red:     []string{"live-model-configs (runtime): Accepted=False: secret not found"},
		Verify:  &verify.Result{Summary: map[verify.Mark]int{verify.AsDefined: 9, verify.Drifted: 1}},
		Report:  "*rowan* is *enabled* — watched as someone.\nProbes: ✅ 9 as defined",
		Next:    "nothing: the action is enabled"}
	var buf bytes.Buffer
	if err := Watch(&buf, r); err != nil {
		t.Fatal(err)
	}
	contains(t, buf.String(), "rowan is enabled (action "+rowanAction+", enabled)", "Rollout of rowan (ready: yes):", "HelmRelease flux-giantswarm/agent-platform", "Ready=True", "4.44.1",
		"HelmRelease flux-giantswarm/muster", "Ready=False", "install retries exhausted", "Red:", "live-model-configs (runtime)", "Probes: 1 drifted, 9 as defined", "Report:", "  Probes: ✅ 9 as defined", "Next: nothing: the action is enabled", "Action "+rowanAction)
}

func TestWaveNamesTheOrderTheSkippedAndTheStages(t *testing.T) {
	r := tools.WaveResult{
		Caller: someone, Tool: tools.ToolReconcileCapability, Capability: agentPlatform, Hub: hazel,
		Action:    &actions.Action{Name: "reconcile-wave-abc123", Status: actions.Status{State: actions.StatePendingApproval}},
		Order:     []string{rowan, hazel},
		Skipped:   []actions.Skipped{{Name: oak, Reason: "not opted in"}},
		Unchanged: []string{"alder"},
		PullRequests: []actions.PullRequest{
			{Installation: rowan, Repository: acmeConfigs, Number: 7, URL: "https://github.com/" + acmeConfigs + "/pull/7"},
			{Installation: hazel, Repository: acmeMCs, Number: 8, URL: "https://github.com/" + acmeMCs + "/pull/8"},
		},
		Next: "the Team review decides; merge rolls out one stage per call",
	}
	var buf bytes.Buffer
	if err := Wave(&buf, r); err != nil {
		t.Fatal(err)
	}
	contains(t, buf.String(),
		"reconcile_capability commit: agent-platform wave on hub hazel, as someone",
		"Action: reconcile-wave-abc123 (pending approval)",
		"Order: rowan, hazel",
		"  oak: not opted in",
		"Unchanged: alder",
		"1. rowan: "+acmeConfigs+"#7 https://github.com/"+acmeConfigs+"/pull/7",
		"2. hazel: "+acmeMCs+"#8",
		"Next: the Team review decides; merge rolls out one stage per call")
}
