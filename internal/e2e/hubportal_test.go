package e2e

// The customer-portal definition renders a customer's portal and no hub
// shape: a commit over a portal whose record carries sections of the hub's
// Dev Portal would strip them, so it is held while the comparison still
// plans their removal.

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// withAppConfig is a portal's app-config on record with sections added
// before its organisation.
func withAppConfig(appConfig, sections string) string {
	return strings.Replace(appConfig, "        organization:\n", sections+"        organization:\n", 1)
}

// hubSections are sections of the hub's Dev Portal as its app-config
// carries them: its incident links, a page over muster, its scaffolder and
// its Postgres database, and the roadmap kept without a value.
const hubSections = "        pagerDuty:\n          eventsBaseUrl: https://events.example.test\n        plans:\n          muster: true\n" +
	"        scaffolder:\n          defaultAuthor:\n            name: Example\n        roadmap: null\n        backend:\n          database:\n            client: pg\n"

// The hub's portal on record with sections of the hub's Dev Portal: the
// comparison runs and plans their removal as before, and says a commit
// would be refused, naming the sections with a value on record — not the
// roadmap, which has none; the dry run says the same; commit mode refuses
// with it before any write, and so does a wave over a set with the hub in
// it, whole: no pull request, no action. A customer's portal carrying a
// section without a value names none.
func TestCommitHeldByTheHubsPortalOnRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), withAppConfig(portalConfig(hub, alder, birch, rowan, willow, maple, "oak", "larch"), hubSections))

	res := verifyPortal(t, c, hub, nil)
	sections := []string{"backstage:app-config:pagerDuty", "backstage:app-config:plans", "backstage:app-config:scaffolder", "backstage:app-config:backend.database"}
	hold := res.Plan().HubRefusal()
	if res.Refused != "" || !slices.Equal(res.HubSections, sections) || hold == "" || res.CommitRefused != hold || !strings.Contains(hold, "(app-config: pagerDuty, plans, scaffolder, backend.database)") {
		t.Fatalf("refused %q, hub sections %v, commitRefused %q", res.Refused, res.HubSections, res.CommitRefused)
	}
	var removals int
	for _, d := range differencesOf(res, hubMCs+":"+installations.PortalConfigPath(hub)) {
		if strings.Contains(d.Path, ":pagerDuty.") || strings.Contains(d.Path, ":backend.database.client") {
			removals++
			if !strings.HasPrefix(d.Planned, "Removed: ") {
				t.Errorf("%s is planned %q, want the removal's reason", d.Path, d.Planned)
			}
		}
	}
	if removals != 2 || res.Diff[plan.ChangeUpdate] == 0 {
		t.Fatalf("the comparison still runs: %d planned removal(s), diff %v", removals, res.Diff)
	}

	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, dry, hub); p.Refused != "" || p.CommitRefused != hold || !slices.Equal(p.HubSections, sections) {
		t.Fatalf("the dry run: refused %q, commitRefused %q, hub sections %v", p.Refused, p.CommitRefused, p.HubSections)
	}
	_, text, isErr = commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal})
	if !isErr || !strings.Contains(text, hub+": "+hold) || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("commit mode: %v %s", isErr, text)
	}
	_, text, isErr = commitCall(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{hub, maple}, tools.ArgCapability: installations.CustomerPortal})
	if !isErr || !strings.Contains(text, hub+": "+hold) || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("a wave: %v %s", isErr, text)
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("the remote saw %d pull request(s)", len(prs))
	}
	for _, name := range []string{hub, maple} {
		if got := listActionsOf(t, c, name); len(got) != 0 {
			t.Fatalf("the refusal recorded %d action(s) for %s", len(got), name)
		}
	}

	st.ghs.addFile(umbrellaMCs, installations.PortalConfigPath(maple), withAppConfig(portalConfig(maple), "        scaffolder: null\n"))
	if res := verifyPortal(t, c, maple, nil); res.Refused != "" || len(res.HubSections) != 0 || strings.Contains(res.CommitRefused, "hub's Dev Portal") {
		t.Fatalf("a customer's portal with scaffolder: null: refused %q, hub sections %v, commitRefused %q", res.Refused, res.HubSections, res.CommitRefused)
	}
}

// handKeptHubSections are the hub's Dev Portal kept by hand: its extensions
// listed one by one, the PagerDuty pages among them, ahead of its sections.
const handKeptHubSections = "          extensions:\n            - page:catalog\n            - entity-card:pagerduty/EntityPagerDutyCard\n            - page:pagerduty\n" + hubSections

// The hub's Dev Portal kept by hand — its own extension list, sections of
// the hub's Dev Portal on record — is the hub's shape: a reconcile that moves
// the chart line plans that one value in the portal's kustomization on
// record, every other byte of it and every other file of the portal as it
// stands. Nothing is refused, no secret is drawn or asked for, no list is
// replaced; the commit opens one pull request with that one change, and an
// unchanged line plans nothing at all.
func TestHandKeptHubPortalMovesItsChartLineAlone(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	appConfig := withAppConfig(portalConfig(hub, alder, birch, rowan, willow, maple, "oak", "larch"), handKeptHubSections)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), appConfig)
	sopsFor(t, st.ghs, hubConfigs, hubMCs)
	seedRemote(t, st)
	kustomization := installations.PortalKustomizationPath(hub)
	moved := strings.Replace(hubPortalKustomization, "'>=2.1.0 <3.0.0'", "'>=2.1.0 <4.0.0'", 1)

	args := func(line string) map[string]any {
		return map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal, tools.ArgInputs: map[string]any{"chart": map[string]any{"line": line}}}
	}
	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args(">=2.1.0 <4.0.0"))
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, dry, hub)
	if p.Refused != "" || p.CommitRefused != "" || len(p.HubSections) != 0 || len(p.GeneratedSecrets) != 0 || len(p.SuppliedSecrets) != 0 || len(p.Includes) != 0 {
		t.Fatalf("refused %q, commitRefused %q, hub sections %v, generated %v, supplied %v, includes %v", p.Refused, p.CommitRefused, p.HubSections, p.GeneratedSecrets, p.SuppliedSecrets, p.Includes)
	}
	if len(p.Files) != 1 || p.Files[0].Path != kustomization || p.Files[0].Change != plan.ChangeUpdate || p.Files[0].Content != moved {
		t.Fatalf("the plan writes %+v, want the chart line alone in %s:\n%s", p.Files, kustomization, moved)
	}

	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args(">=2.1.0 <4.0.0"))
	if isErr || out.Action == nil || len(out.PullRequests) != 1 || out.PullRequests[0].Repository != hubMCs {
		t.Fatalf("the commit opens one pull request in %s: %s", hubMCs, text)
	}
	onBranch := st.remote.Files(repoOf(t, hubMCs), branchPrefix+out.Action.Name+"/"+hub)
	if got := string(onBranch[kustomization]); got != moved {
		t.Fatalf("the branch's %s:\n%s\nwant:\n%s", kustomization, got, moved)
	}
	if got := string(onBranch[installations.PortalConfigPath(hub)]); got != appConfig {
		t.Fatalf("the branch changed the hub's app-config:\n%s", got)
	}

	dry, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, args(">=2.1.0 <3.0.0"))
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, dry, hub); p.CommitRefused != "" || p.Diff[plan.ChangeUpdate] != 0 || p.Diff[plan.ChangeUnchanged] != 1 {
		t.Fatalf("the line on record: commitRefused %q, diff %v", p.CommitRefused, p.Diff)
	}
}
