package format

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// appPurpose is the portal App's purpose in the record.
const appPurpose = "customerPortal"

// The dry run names each GitHub App of the installation's own with what the
// record holds of it, the creation step of one not on record with the
// commit's references, and the rotation of one due a successor.
func TestPlanPrintsTheGitHubApps(t *testing.T) {
	spec := func(name string, onRecord render.GitHubApp) render.AppSpec {
		return render.AppSpec{Purpose: appPurpose, Name: name, OnRecord: onRecord, Create: "beekeeper app create giantswarm-portal --installation " + oak,
			Secrets: []string{"--secret plugins.github.appId=beekeeper:op://<vault>/" + name + "/app-id"}}
	}
	r := tools.CapabilityResult{Caller: "finch", Hub: "kestrel", Tool: tools.ToolEnableCapability, Capability: installations.CustomerPortal, DryRun: true, Order: []string{oak, hazel},
		Installations: []tools.DryRun{
			{Installation: plan.Installation{Name: oak, GitHubApps: []plan.GitHubApp{{AppSpec: spec("giantswarm-portal-oak", render.GitHubApp{}), NotOnRecord: true}}}},
			{Installation: plan.Installation{Name: hazel, GitHubApps: []plan.GitHubApp{{AppSpec: spec("giantswarm-portal-hazel", render.GitHubApp{ID: 7, Created: "2026-01-05"}), RotationDue: "2026-07-05", Rotation: "beekeeper app create … --replace; then platformctl …"}}}},
		}}
	var buf bytes.Buffer
	if err := Plan(&buf, r, false); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"  GitHub Apps:\n    giantswarm-portal-oak (customerPortal): no id on record\n      not on record: create it with beekeeper app create giantswarm-portal --installation oak, then commit with --secret plugins.github.appId=beekeeper:op://<vault>/giantswarm-portal-oak/app-id\n",
		"    giantswarm-portal-hazel (customerPortal): id 7 on record, created 2026-01-05\n      rotation due since 2026-07-05: beekeeper app create … --replace; then platformctl …\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dry run lacks %q:\n%s", want, got)
		}
	}
}

// installation list prints the credential inventory of GitHub Apps the
// records keep, with the rotation of one due; nothing where none is kept.
func TestInstallationsPrintsTheAppInventory(t *testing.T) {
	defer func(f func() time.Time) { clock = f }(clock)
	clock = func() time.Time { return time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC) }
	r := tools.ListInstallationsResult{Installations: []installations.Report{
		{Installation: installations.Installation{Name: oak}, Record: &installations.Record{GitHubApps: map[string]render.GitHubApp{appPurpose: {Name: "giantswarm-portal-oak", ID: 7, Created: "2026-01-05"}}}},
		{Installation: installations.Installation{Name: hazel}, Record: &installations.Record{GitHubApps: map[string]render.GitHubApp{appPurpose: {Name: "giantswarm-portal-hazel", ID: 9, Created: "2026-09-01"}}}},
	}}
	var buf bytes.Buffer
	if err := Installations(&buf, r); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"GitHub Apps on record:", "giantswarm-portal-oak", "due since 2026-07-05", "giantswarm-portal-hazel", "2027-03-01"} {
		if !strings.Contains(got, want) {
			t.Errorf("the list lacks %q:\n%s", want, got)
		}
	}
	buf.Reset()
	if err := Installations(&buf, tools.ListInstallationsResult{Installations: []installations.Report{{Installation: installations.Installation{Name: oak}, Record: &installations.Record{}}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "GitHub Apps") {
		t.Errorf("an inventory printed without an App:\n%s", buf.String())
	}
}
