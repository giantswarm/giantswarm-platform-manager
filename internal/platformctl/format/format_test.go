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
			GeneratedSecrets: []plan.GeneratedSecret{{Name: "muster-valkey-password", Kind: "alphanumeric", Length: 32, Files: []string{"x", "y"}}},
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
		"muster-valkey-password (alphanumeric, 32): x, y", "You supply at commit: kagent.modelKey",
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
