package e2e

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The portal runs on a GitHub App of the installation's own: rowan's dry run
// names giantswarm-portal-rowan and, its credentials not on record, the
// secret tooling's step that creates it and the vault references its commit
// takes them by. maple, another owner's installation, keeps App 4242 on
// record: a commit supplying that id for rowan is refused naming both, with
// nothing recorded; a commit with rowan's own App writes the App's name, id
// and the commit's date into rowan's record, every other key kept, and the
// inventory lists maple's App. A dry run over an id another owner holds is a
// commit refusal. No supplied value appears in an answer, the action, a pull
// request or the log.
func TestThePortalRunsOnTheInstallationsOwnGitHubApp(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFor(t, st.ghs, acmeConfigs, acmeMCs)
	const umbrellaConfigs = "example/umbrella-configs"
	st.ghs.addFile(umbrellaConfigs, installations.ConfigPatchPath(maple), "codename: maple\nbase: umbrella.test\ngithubApps:\n  customerPortal:\n    name: giantswarm-portal-maple\n    id: 4242\n    created: \"2026-01-05\"\n")
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)

	inputs := rowanPortalInputs(map[string]any{enabledKey: false})
	inputs["plugins"].(map[string]any)["github"] = map[string]any{enabledKey: true}
	args := func(secrets map[string]any) map[string]any {
		a := map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal, tools.ArgInputs: inputs, tools.ArgReason: commitReason}
		if secrets != nil {
			a[tools.ArgSecrets] = secrets
		}
		return a
	}

	dry, dryText, isErr := dryRun(t, c, tools.ToolEnableCapability, args(nil))
	if isErr {
		t.Fatal(dryText)
	}
	p := findPlan(t, dry, rowan)
	if p.Refused != "" || p.CommitRefused != "" || len(p.GitHubApps) != 1 {
		t.Fatalf("refused %q, commit refused %q, apps %+v", p.Refused, p.CommitRefused, p.GitHubApps)
	}
	app := p.GitHubApps[0]
	if app.Name != "giantswarm-portal-rowan" || !app.NotOnRecord || app.OnRecord.ID != 0 || !strings.HasPrefix(app.Create, "beekeeper app create giantswarm-portal --installation rowan --base-domain ") ||
		len(app.Secrets) != 5 || !strings.Contains(strings.Join(app.Secrets, " "), "--secret plugins.github.appId=beekeeper:op://<vault>/giantswarm-portal-rowan/app-id") {
		t.Fatalf("app %+v", app)
	}

	values := map[string]string{}
	secrets := func(appID string) map[string]any {
		out := map[string]any{}
		for _, f := range p.SuppliedSecrets {
			values[f] = "fixture-" + strings.ReplaceAll(f, ".", "-") + "-9c1f"
			out[f] = values[f]
		}
		out[githubAppIDField] = appID
		return out
	}
	noValue := func(where, text string) {
		t.Helper()
		for f, v := range values {
			if f != githubAppIDField && strings.Contains(text, v) {
				t.Fatalf("%s carries the value of %s", where, f)
			}
		}
	}

	_, text, isErr := commitCall(t, c, tools.ToolEnableCapability, args(secrets("4242")))
	if !isErr || !strings.Contains(text, "GitHub App id 4242 for giantswarm-portal-rowan on rowan (acme) is on record for "+maple+" (umbrella) as giantswarm-portal-maple") || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("a commit on another owner's App: %v %s", isErr, text)
	}
	noValue("the refusal", text)
	if got := listActionsOf(t, c, hub); len(got) != 0 {
		t.Fatalf("a refused commit recorded %d action(s)", len(got))
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("a refused commit opened %d pull request(s)", len(prs))
	}

	out, text, isErr := commitCall(t, c, tools.ToolEnableCapability, args(secrets("5151")))
	if isErr || out.Action == nil {
		t.Fatalf("a commit on rowan's own App: %s", text)
	}
	noValue("the commit's answer", text)
	branch := "platform/" + out.Action.Name + "/" + rowan
	record := st.remote.Files(repoOf(t, acmeConfigs), branch)[installations.ConfigPatchPath(rowan)]
	var doc struct {
		Codename   string                      `yaml:"codename"`
		GatewayAPI map[string]any              `yaml:"gatewayApi"`
		GitHubApps map[string]render.GitHubApp `yaml:"githubApps"`
	}
	if err := yaml.Unmarshal(record, &doc); err != nil {
		t.Fatalf("rowan's record on the branch: %v\n%s", err, record)
	}
	want := render.GitHubApp{Name: "giantswarm-portal-rowan", ID: 5151, Created: time.Now().UTC().Format(time.DateOnly)}
	if doc.GitHubApps["customerPortal"] != want || doc.Codename != rowan || doc.GatewayAPI["clusterIssuer"] != "private-example" {
		t.Fatalf("rowan's record on the branch:\n%s", record)
	}
	for _, pr := range st.remote.PullRequests() {
		noValue("pull request "+pr.Title, pr.Title+pr.Body)
		for path, content := range st.remote.Files(pr.Repository, pr.Head) {
			if !strings.Contains(path, ".enc.") {
				noValue(path, string(content))
			}
		}
	}
	noValue("the server's log", st.logs.String())

	li, text, isErr := listInstallations(t, c, map[string]any{tools.ArgInstallations: []any{maple}})
	if isErr {
		t.Fatal(text)
	}
	if r := find(t, li, maple); r.Record == nil || r.Record.GitHubApps["customerPortal"] != (render.GitHubApp{Name: "giantswarm-portal-maple", ID: 4242, Created: "2026-01-05"}) {
		t.Fatalf("maple's inventory: %+v", r.Record)
	}

	// rowan's record keeping maple's App, written by hand: the dry run's commit is refused.
	st.ghs.addFile(acmeConfigs, installations.ConfigPatchPath(rowan), "codename: rowan\nbase: acme.test\ngithubApps:\n  customerPortal:\n    name: giantswarm-portal-rowan\n    id: 4242\n")
	dry, dryText, isErr = dryRun(t, c, tools.ToolEnableCapability, args(nil))
	if isErr {
		t.Fatal(dryText)
	}
	if p := findPlan(t, dry, rowan); !strings.Contains(p.CommitRefused, "on record for "+maple+" (umbrella)") || p.GitHubApps[0].OnRecord.ID != 4242 {
		t.Fatalf("a dry run over another owner's App: commit refused %q, apps %+v", p.CommitRefused, p.GitHubApps)
	}
}

// The hub's portal runs on giantswarm-portal-hazel, created more than six
// months ago with its credentials on record: the dry run raises the
// rotation, the successor step and the commit that swaps the credentials.
// That commit — the App named in rotate — asks for every field of the App
// again, writes its credentials file anew, encrypted, and records the new
// id with the commit's date; a rotate naming no App or generated value of
// the plan is refused by name.
func TestTheRotationSwapsThePortalsAppCredentials(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFor(t, st.ghs, hubConfigs, hubMCs)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), portalConfigWithGitHub(hub, alder, birch, "rowan", "willow", "oak", "larch"))
	st.ghs.addFile(hubMCs, hubGitHubAppFile, hubGitHubAppOnRecord)
	st.ghs.addFile(hubConfigs, installations.ConfigPatchPath(hub), "codename: hazel\nbase: example.test\ncustomer: example\nmanagementCluster:\n  private: false\nagentPlatform:\n  kagentApiV2: true\nservices:\n  muster:\n    clientId: muster-hazel\n"+
		"githubApps:\n  customerPortal:\n    name: giantswarm-portal-hazel\n    id: 123\n    created: \"2026-01-05\"\n")
	seedRemote(t, st)
	c := st.mcpClient(t, aliceToken)
	app := "giantswarm-portal-" + hub
	args := func(rotate []any, secrets map[string]any) map[string]any {
		a := map[string]any{tools.ArgInstallation: hub, tools.ArgCapability: installations.CustomerPortal, tools.ArgReason: commitReason}
		if rotate != nil {
			a[tools.ArgRotate] = rotate
		}
		if secrets != nil {
			a[tools.ArgSecrets] = secrets
		}
		return a
	}

	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args(nil, nil))
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, dry, hub)
	if len(p.GitHubApps) != 1 || p.GitHubApps[0].Name != app || p.GitHubApps[0].NotOnRecord || p.GitHubApps[0].RotationDue != "2026-07-05" ||
		!strings.HasPrefix(p.GitHubApps[0].Rotation, "beekeeper app create giantswarm-portal --installation "+hub+" --base-domain ") ||
		!strings.Contains(p.GitHubApps[0].Rotation, " --replace; then platformctl installation reconcile "+hub+" customer-portal --rotate "+app+" --commit --secret plugins.github.appId=beekeeper:op://<vault>/"+app+"/app-id") {
		t.Fatalf("apps %+v", p.GitHubApps)
	}
	if slices.Contains(p.SuppliedSecrets, githubAppIDField) {
		t.Fatalf("supplied %v: the App's credentials stand on record", p.SuppliedSecrets)
	}

	if _, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args([]any{"giantswarm-portal-nowhere"}, nil)); !isErr || !strings.Contains(text, "giantswarm-portal-nowhere") {
		t.Fatalf("a rotate naming no App of the plan: %v %s", isErr, text)
	}
	dry, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, args([]any{app}, nil))
	if isErr {
		t.Fatal(text)
	}
	p = findPlan(t, dry, hub)
	if !p.GitHubApps[0].Replacing || !slices.Contains(p.SuppliedSecrets, githubAppIDField) || !slices.Contains(p.SuppliedSecrets, "plugins.github.privateKey") || slices.Contains(p.SuppliedOnRecord, githubAppIDField) {
		t.Fatalf("replacing %v, supplied %v, on record %v", p.GitHubApps[0].Replacing, p.SuppliedSecrets, p.SuppliedOnRecord)
	}
	secrets := map[string]any{}
	values := []string{}
	for _, f := range p.SuppliedSecrets {
		v := "fixture-" + strings.ReplaceAll(f, ".", "-") + "-4e7a"
		secrets[f] = v
		values = append(values, v)
	}
	secrets[githubAppIDField] = "999"
	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args([]any{app}, secrets))
	if isErr || out.Action == nil {
		t.Fatalf("the rotation's commit: %s", text)
	}
	branch := "platform/" + out.Action.Name + "/" + hub
	creds := string(st.remote.Files(repoOf(t, hubMCs), branch)[hubGitHubAppFile])
	if creds == "" || creds == hubGitHubAppOnRecord || !strings.Contains(creds, "ENC[") {
		t.Fatalf("the App's credentials file on the branch:\n%s", creds)
	}
	var doc struct {
		Services   map[string]any              `yaml:"services"`
		GitHubApps map[string]render.GitHubApp `yaml:"githubApps"`
	}
	record := st.remote.Files(repoOf(t, hubConfigs), branch)[installations.ConfigPatchPath(hub)]
	if err := yaml.Unmarshal(record, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.GitHubApps["customerPortal"] != (render.GitHubApp{Name: app, ID: 999, Created: time.Now().UTC().Format(time.DateOnly)}) || doc.Services == nil {
		t.Fatalf("the record on the branch:\n%s", record)
	}
	for _, v := range values {
		for _, where := range []string{text, st.logs.String(), string(record)} {
			if strings.Contains(where, v) {
				t.Fatal("a supplied value left the encrypted file")
			}
		}
	}
}
