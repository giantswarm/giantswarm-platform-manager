package customerportal

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The portal runs on a GitHub App of the installation's own: its name derives
// from the installation, the record keeps its name, id and creation date
// under githubApps.customerPortal, and the plan's App names the step that
// creates it and the vault references its commit takes the credentials by.
func TestThePortalsGitHubAppIsTheInstallations(t *testing.T) {
	defer func(f func() string) { today = f }(today)
	today = func() string { return commitDate }
	const created = "2026-04-01"

	entry := func(t *testing.T, input map[string]any, secrets map[string]string, mode render.Mode) map[string]any {
		t.Helper()
		result, err := Render(input, secrets, mode)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := result.Files["giantswarm/acme-configs"][render.RecordPath("maple")]
		if !ok {
			t.Fatal("no record file rendered for the portal's App")
		}
		var doc map[string]map[string]map[string]any
		if err := yaml.Unmarshal(f.Content, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc) != 1 || len(doc[render.GitHubAppsKey]) != 1 {
			t.Fatalf("the record file edits more than githubApps.customerPortal:\n%s", f.Content)
		}
		return doc[render.GitHubAppsKey][appPurpose]
	}
	withRecord := func(app map[string]any) map[string]any {
		input, _ := loadInput(t, "customer-portal")
		inst := input["installation"].(map[string]any)
		delete(inst, "githubApps")
		if app != nil {
			inst["githubApps"] = map[string]any{appPurpose: app}
		}
		return input
	}
	_, secrets := loadInput(t, "customer-portal")
	markers := map[string]string{}
	for f := range secrets {
		markers[f] = Supplied(f)
	}
	supplied := func(id string) map[string]string {
		out := map[string]string{}
		for f, v := range secrets {
			out[f] = v
		}
		out[fieldGitHubAppID] = id
		return out
	}

	for _, c := range []struct {
		name    string
		record  map[string]any
		secrets map[string]string
		mode    render.Mode
		want    map[string]any
	}{
		{"a first commit learns the id and the date", nil, supplied("123456"), render.ModeCommit, appEntry(123456, commitDate)},
		{"the id on record keeps its date", appEntry(123456, created), supplied("123456"), render.ModeCommit, appEntry(123456, created)},
		{"a new id is a new App", appEntry(123456, created), supplied("777"), render.ModeCommit, appEntry(777, commitDate)},
		{"a dry run keeps the entry on record", appEntry(123456, created), markers, render.ModeCompare, appEntry(123456, created)},
		{"a dry run without an entry names the App alone", nil, markers, render.ModeCompare, appEntry(0, "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := entry(t, withRecord(c.record), c.secrets, c.mode)
			if len(got) != len(c.want) {
				t.Fatalf("entry %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("%s: %v, want %v", k, got[k], v)
				}
			}
		})
	}

	in, err := Parse(withRecord(appEntry(123456, created)))
	if err != nil {
		t.Fatal(err)
	}
	apps := in.GitHubApps()
	if len(apps) != 1 {
		t.Fatalf("apps %+v", apps)
	}
	a := apps[0]
	if a.Name != "giantswarm-portal-maple" || a.Purpose != appPurpose || a.IDField != fieldGitHubAppID || a.OnRecord.ID != 123456 || a.OnRecord.Created != created {
		t.Errorf("app %+v", a)
	}
	if want := "beekeeper app create giantswarm-portal --installation maple --base-domain maple.acme.example.test"; a.Create != want || a.Replace != want+" --replace" {
		t.Errorf("create %q, replace %q", a.Create, a.Replace)
	}
	if len(a.Secrets) != len(appVaultFields) || a.Secrets[0] != "--secret plugins.github.appId=beekeeper:op://<vault>/giantswarm-portal-maple/app-id" {
		t.Errorf("secrets %v", a.Secrets)
	}
	for _, s := range a.Secrets {
		if !strings.Contains(s, "=beekeeper:op://<vault>/giantswarm-portal-maple/") {
			t.Errorf("%s: not the App's vault item", s)
		}
	}
}

// A portal without the GitHub integration runs on no App: nothing of one is
// rendered into the record or named in the plan.
func TestAPortalWithoutGitHubRunsOnNoApp(t *testing.T) {
	input, secrets := loadInput(t, "customer-portal")
	input["plugins"].(map[string]any)["github"] = map[string]any{"enabled": false}
	for f := range secrets {
		if strings.HasPrefix(f, "plugins.github.") {
			delete(secrets, f)
		}
	}
	result, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Files["giantswarm/acme-configs"][render.RecordPath("maple")]; ok {
		t.Error("a record entry rendered for a portal without the GitHub integration")
	}
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if apps := in.GitHubApps(); len(apps) != 0 {
		t.Errorf("apps %+v", apps)
	}
}

// nameKey is an App entry's name.
const nameKey = "name"

// commitDate is the date the tests' commits learn an App on.
const commitDate = "2026-10-18"

// appEntry is maple's App entry in the record: its name, and its id and
// creation date where given.
func appEntry(id int, created string) map[string]any {
	entry := map[string]any{nameKey: GitHubAppName("maple")}
	if id > 0 {
		entry["id"] = id
	}
	if created != "" {
		entry["created"] = created
	}
	return entry
}
