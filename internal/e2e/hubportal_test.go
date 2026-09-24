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
