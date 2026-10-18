package plan

import (
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The installation and App the tests name.
const (
	linden        = "linden"
	lindenApp     = "giantswarm-portal-linden"
	portalPurpose = "customerPortal"
	appIDField    = "plugins.github.appId"
)

// appInput is an Input whose render runs on GitHub Apps.
type appInput struct {
	render.Input
	apps []render.AppSpec
}

func (a appInput) GitHubApps() []render.AppSpec { return a.apps }

func portalApp(installation string, onRecord render.GitHubApp) render.AppSpec {
	return render.AppSpec{Purpose: portalPurpose, Name: "giantswarm-portal-" + installation, OnRecord: onRecord, IDField: appIDField,
		Create:           "beekeeper app create giantswarm-portal --installation " + installation + " --base-domain " + installation + ".example",
		Replace:          "beekeeper app create giantswarm-portal --installation " + installation + " --base-domain " + installation + ".example --replace",
		Secrets:          []string{"--secret plugins.github.appId=beekeeper:op://<vault>/giantswarm-portal-" + installation + "/app-id"},
		Successor:        "giantswarm-portal-" + installation + "-2",
		SuccessorSecrets: []string{"--secret plugins.github.appId=beekeeper:op://<vault>/giantswarm-portal-" + installation + "-2/app-id"}}
}

// The plan names the App, the step that creates one whose credentials are not
// on record, and — six months after its creation date on record — the
// rotation: the successor step and the commit that swaps the credentials.
func TestPlanNamesTheAppItsCreationAndItsRotation(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	fresh := githubApps(appInput{apps: []render.AppSpec{portalApp(linden, render.GitHubApp{})}}, Installation{Name: linden, SuppliedSecrets: []string{appIDField}}, "customer-portal", now)
	if len(fresh) != 1 || !fresh[0].NotOnRecord || fresh[0].RotationDue != "" || fresh[0].Rotation != "" {
		t.Fatalf("a fresh App: %+v", fresh)
	}
	young := githubApps(appInput{apps: []render.AppSpec{portalApp(linden, render.GitHubApp{Name: lindenApp, ID: 7, Created: "2026-06-01"})}}, Installation{Name: linden}, "customer-portal", now)
	if len(young) != 1 || young[0].NotOnRecord || young[0].RotationDue != "2026-12-01" || young[0].Rotation != "" {
		t.Fatalf("an App of four months: %+v", young)
	}
	old := githubApps(appInput{apps: []render.AppSpec{portalApp(linden, render.GitHubApp{Name: lindenApp, ID: 7, Created: "2026-03-01"})}}, Installation{Name: linden}, "customer-portal", now)
	want := "beekeeper app create giantswarm-portal --installation linden --base-domain linden.example --replace; then platformctl installation reconcile linden customer-portal --rotate giantswarm-portal-linden --commit --secret plugins.github.appId=beekeeper:op://<vault>/giantswarm-portal-linden-2/app-id"
	if len(old) != 1 || old[0].RotationDue != "2026-09-01" || old[0].Rotation != want {
		t.Fatalf("an App of seven months: %+v", old)
	}
	if apps := githubApps(appInput{}, Installation{Name: linden}, "customer-portal", now); len(apps) != 0 {
		t.Fatalf("an input without Apps: %+v", apps)
	}
}

// An App id another owner's installation has on record refuses the plan,
// naming both; the installation's own entry and one of the same owner do
// not. The id supplied at commit wins over the one on record.
func TestSharedAppRefusal(t *testing.T) {
	holders := map[int][]installations.AppHolder{
		7: {{Installation: linden, Customer: "acme", Purpose: portalPurpose, App: render.GitHubApp{Name: lindenApp, ID: 7}}},
		9: {{Installation: "spruce", Customer: "globex", Purpose: portalPurpose, App: render.GitHubApp{Name: "giantswarm-portal-spruce", ID: 9}}},
	}
	own := Installation{Name: linden, GitHubApps: []GitHubApp{{AppSpec: portalApp(linden, render.GitHubApp{ID: 7})}}}
	if r := SharedAppRefusal(own, "acme", holders, nil); r != "" {
		t.Errorf("the installation's own App: %s", r)
	}
	sameOwner := Installation{Name: "birch", GitHubApps: []GitHubApp{{AppSpec: portalApp("birch", render.GitHubApp{ID: 7})}}}
	if r := SharedAppRefusal(sameOwner, "acme", holders, nil); r != "" {
		t.Errorf("an App of the same owner: %s", r)
	}
	other := Installation{Name: "pine", GitHubApps: []GitHubApp{{AppSpec: portalApp("pine", render.GitHubApp{ID: 7})}}}
	r := SharedAppRefusal(other, "initech", holders, nil)
	for _, want := range []string{"GitHub App id 7", "giantswarm-portal-pine on pine (initech)", "on record for linden (acme) as giantswarm-portal-linden", "beekeeper app create giantswarm-portal --installation pine"} {
		if !strings.Contains(r, want) {
			t.Errorf("refusal %q lacks %q", r, want)
		}
	}
	if r := SharedAppRefusal(own, "acme", holders, map[string]string{appIDField: "9"}); !strings.Contains(r, "on record for spruce (globex)") {
		t.Errorf("a supplied id of another owner's App: %q", r)
	}
	if r := SharedAppRefusal(own, "acme", holders, map[string]string{appIDField: render.Supplied(appIDField)}); r != "" {
		t.Errorf("a marker falls back to the id on record: %q", r)
	}
}
