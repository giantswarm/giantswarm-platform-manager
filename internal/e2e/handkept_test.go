package e2e

// A hand-kept portal moves onto the customer-portal definition in three
// actions — the customer-portal commit, the agent-platform reconcile that
// takes the portal's lists over, the customer-portal reconcile that hands the
// section over — and at no step does the running portal lose the platform's
// extensions or its skill repositories: Backstage loads the Component's
// fragment after the portal's app-config and keeps a list whole from the
// later file, so the effective list is the fragment's where it carries one,
// the app-config's otherwise.

import (
	"slices"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// sharedExtensions stands for the fleet base's shared-config.yaml: each
// extension list the portals include by anchor, the platform's section and
// the chat's entries added one by one.
var sharedExtensions = func() map[string][]any {
	base := []any{"page:catalog", "nav-item:catalog", "page:home"}
	platform := append(slices.Clone(base), "page:agent-platform", "nav-item:agent-platform")
	chat := append(slices.Clone(platform), "page:ai-chat", "api:ai-chat/service", "api:ai-chat/drawer", "app-root-element:ai-chat/drawer")
	return map[string][]any{
		render.PortalExtensionsInclude(false, false, false): base,
		render.PortalExtensionsInclude(true, false, false):  platform,
		render.PortalExtensionsInclude(true, true, false):   chat,
	}
}()

// keptSection are the keys of the platform's section the portal's
// app-config carries until the Component's fragment owns the lists.
var keptSection = []string{"muster", "agentPlatform", "aiChat", "mcpActions"}

// handKeptSkills are the skill repositories rowan's hand-kept portal lists.
var handKeptSkills = []any{"https://github.com/example/agent-skills", "https://github.com/example/more-skills"}

// handKeptPortal is rowan's portal as its people keep it by hand: the
// extensions listed one by one (the shared list with the platform's section
// and the chat), the muster registry, the kagent installation and the skill
// repositories, the chat's blocks.
func handKeptPortal() string {
	var extensions strings.Builder
	for _, x := range sharedExtensions[render.PortalExtensionsInclude(true, true, false)] {
		extensions.WriteString("            - " + x.(string) + "\n")
	}
	var skills strings.Builder
	for _, r := range handKeptSkills {
		skills.WriteString("              - " + r.(string) + "\n")
	}
	const portal, muster = "https://portal.rowan.acme.test", "https://muster.rowan.acme.test/mcp"
	return "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n    backstage:\n      appConfig: |\n" +
		"        app:\n          title: Dev Portal\n          baseUrl: " + portal + "\n          extensions:\n" + extensions.String() +
		"        organization:\n          name: ACME\n" +
		"        gs:\n          installations:\n            rowan:\n              authProvider: oidc\n              baseDomain: rowan.acme.test\n              providers:\n                - capa\n" +
		"        kubernetes:\n          clusterLocatorMethods:\n            - type: config\n              clusters:\n                - name: rowan\n                  url: https://happaapi.rowan.acme.test\n" +
		"        muster:\n          installations:\n            - name: rowan\n              url: " + muster + "\n              authProvider: oidc-rowan\n" +
		"        agentPlatform:\n          kagent:\n            installations:\n              rowan: {}\n          skills:\n            repositories:\n" + skills.String() +
		"        aiChat:\n          anthropic:\n            apiKey: $${ANTHROPIC_API_KEY}\n          model: claude-opus-5\n" +
		"          mcp:\n            - name: backstage-actions\n              url: " + portal + "/api/mcp-actions/v1\n              useBackstageUserToken: true\n" +
		"            - name: muster\n              url: " + muster + "\n              authProvider: oidc-rowan\n" +
		"        mcpActions:\n          namespacedToolNames: false\n" +
		"        backend:\n          actions:\n            pluginSources: [auth, catalog, gs]\n            filter:\n              exclude:\n                - id: gs:get-pagerduty-ids-for-entity\n"
}

// rowanFragmentPath is the agent-platform Component's app-config fragment in
// rowan's portal tree.
var rowanFragmentPath = strings.TrimSuffix(installations.PortalConfigPath(rowan), render.PortalDir+"/app-config.yaml") + render.PortalPlatformDir + "/app-config.yaml"

// effective is what the running portal gets from its two files on record:
// the extension list (the fragment's where it carries one, else the
// app-config's, each literal or expanded from its anchor) and the skill
// repositories (the fragment's where it carries them, else the app-config's).
func effective(t *testing.T, st *stack) (extensions, skills []any) {
	t.Helper()
	appConfig, fragment := portalDocs(t, st)
	list := func(doc map[string]any) ([]any, bool) {
		app, _ := doc["app"].(map[string]any)
		switch v := app["extensions"].(type) {
		case []any:
			return v, true
		case map[string]any:
			anchor, _ := v["$include"].(string)
			l, known := sharedExtensions[anchor]
			if !known {
				t.Fatalf("an include of no list the fleet's shared config carries: %s", anchor)
			}
			return l, true
		}
		return nil, false
	}
	repositories := func(doc map[string]any) ([]any, bool) {
		platform, _ := doc[keptSection[1]].(map[string]any)
		s, _ := platform["skills"].(map[string]any)
		r, ok := s["repositories"].([]any)
		return r, ok
	}
	var ok bool
	if extensions, ok = list(fragment); !ok {
		extensions, _ = list(appConfig)
	}
	if skills, ok = repositories(fragment); !ok {
		skills, _ = repositories(appConfig)
	}
	return extensions, skills
}

// portalDocs are rowan's portal app-config and the Component's fragment on
// record, decoded; the fragment empty where it is not on record.
func portalDocs(t *testing.T, st *stack) (appConfig, fragment map[string]any) {
	t.Helper()
	decode := func(text string) map[string]any {
		doc := map[string]any{}
		if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
			t.Fatal(err)
		}
		return doc
	}
	main, ok := st.ghs.file(acmeMCs, installations.PortalConfigPath(rowan))
	if !ok {
		t.Fatal("rowan's portal app-config is not on record")
	}
	values, _ := decode(main)["data"].(map[string]any)["values"].(string)
	text, _ := decode(values)["backstage"].(map[string]any)["appConfig"].(string)
	appConfig = decode(text)
	if raw, ok := st.ghs.file(acmeMCs, rowanFragmentPath); ok {
		data, _ := decode(raw)["data"].(map[string]any)
		fragmentText, _ := data["app-config.agent-platform.yaml"].(string)
		return appConfig, decode(fragmentText)
	}
	return appConfig, map[string]any{}
}

// migrationStep is one action's dry run over rowan with the content, its
// written files put on record as the merged pull request leaves them — the
// plaintext ones: a secret file stands as it is — and the dry run again,
// which writes nothing: the state is the definition's fixed point. It
// answers the plan the step wrote.
func migrationStep(t *testing.T, st *stack, c *client.Client, capability string, typed map[string]any) plan.Installation {
	t.Helper()
	dry := func() plan.Installation {
		t.Helper()
		args := map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: capability, tools.ArgContent: true}
		if typed != nil {
			args[tools.ArgInputs] = typed
		}
		out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args)
		if isErr {
			t.Fatal(text)
		}
		p := findPlan(t, out, rowan)
		if p.Refused != "" {
			t.Fatalf("%s refused: %s", capability, p.Refused)
		}
		return p
	}
	first := dry()
	written := map[string]string{}
	for _, f := range first.Files {
		if f.Change == plan.ChangeUnchanged || isSecretFile(f.Path) || strings.Contains(f.Content, "GENERATED(") || strings.Contains(f.Content, "SUPPLIED(") {
			continue
		}
		written[f.Repository+":"+f.Path] = f.Content
		st.ghs.addFile(f.Repository, f.Path, f.Content)
	}
	for _, f := range dry().Files {
		if want, ok := written[f.Repository+":"+f.Path]; ok && (f.Change != plan.ChangeUnchanged || f.Content != want) {
			t.Errorf("%s: %s is %s the second time:\n%s\nwant\n%s", capability, f.Path, f.Change, f.Content, want)
		}
	}
	return first
}

// rowan's portal is hand-kept and the platform runs there, its Component's
// fragment in the hand-kept shape (the object-shaped keys alone). The
// customer-portal commit keeps the platform's section in the app-config and
// includes the list with it; the agent-platform reconcile then takes the
// lists over, the skill repositories read back from the app-config; the next
// customer-portal reconcile hands the section over, planned as moved. At
// every step the portal's effective extensions and skill repositories are the
// record's, and every state is each definition's fixed point.
func TestHandKeptPortalMigrationKeepsThePlatformSection(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	st.ghs.addFiles(acmeMCs, map[string]string{
		installations.PortalConfigPath(rowan):        handKeptPortal(),
		installations.PortalKustomizationPath(rowan): hubPortalKustomization,
	})
	c := st.mcpClient(t, aliceToken)
	putOnRecord(t, st, c, rowan, kagentEnabled())
	// The fragment on record predates the skill repositories as an input: it
	// carries the object-shaped keys and no skills.
	onRecord, _ := st.ghs.file(acmeMCs, rowanFragmentPath)
	skills := "      skills:\n        repositories:\n"
	for _, r := range handKeptSkills {
		skills += "          - " + r.(string) + "\n"
	}
	if without := strings.Replace(onRecord, skills, "", 1); without != onRecord {
		st.ghs.addFile(acmeMCs, rowanFragmentPath, without)
	} else {
		t.Fatalf("the Component's fragment does not carry the skill repositories read back from the portal:\n%s", onRecord)
	}
	record := sharedExtensions[render.PortalExtensionsInclude(true, true, false)]
	holds := func(step string) {
		t.Helper()
		extensions, skills := effective(t, st)
		if !slices.Equal(extensions, record) || !slices.Equal(skills, handKeptSkills) {
			t.Fatalf("%s: the portal runs with the extensions %v and the skill repositories %v, want the record's %v and %v", step, extensions, skills, record, handKeptSkills)
		}
	}
	appConfig, fragment := portalDocs(t, st)
	if _, lists := fragment["app"]; lists || fragment[keptSection[2]] == nil || fragment[keptSection[1]].(map[string]any)["skills"] != nil {
		t.Fatalf("the Component's fragment on record is not in the hand-kept shape without skills: %v", fragment)
	}
	if _, literal := appConfig["app"].(map[string]any)["extensions"].([]any); !literal {
		t.Fatalf("the portal on record is not hand-kept: %v", appConfig["app"])
	}
	holds("on record")

	portal := rowanPortalInputs(map[string]any{enabledKey: false})
	migrationStep(t, st, c, installations.CustomerPortal, portal)
	holds("after the customer-portal commit")
	appConfig, _ = portalDocs(t, st)
	if got := appConfig["app"].(map[string]any)["extensions"]; !mapEqual(got, render.PortalExtensionsInclude(true, true, false)) {
		t.Errorf("the app-config includes %v, want the list with the platform's section and the chat", got)
	}
	for _, key := range keptSection {
		if appConfig[key] == nil {
			t.Errorf("the app-config lost %s while the fragment does not own the lists", key)
		}
	}

	migrationStep(t, st, c, installations.AgentPlatform, nil)
	holds("after the agent-platform reconcile")
	if _, fragment = portalDocs(t, st); !mapEqual(fragment["app"].(map[string]any)["extensions"], render.PortalExtensionsInclude(true, true, false)) || fragment[keptSection[0]] == nil {
		t.Errorf("the Component did not take the portal's lists over: %v", fragment)
	}

	res := verifyPortal(t, c, rowan, portal)
	var moved int
	for _, d := range differencesOf(res, acmeMCs+":"+installations.PortalConfigPath(rowan)) {
		if d.Planned == "" {
			t.Errorf("the hand-over reads as drift at %s: %+v", d.Path, d)
		}
		if strings.HasPrefix(d.Planned, "Moved: ") {
			moved++
		}
	}
	if moved == 0 {
		t.Errorf("the customer-portal reconcile plans no move once the fragment owns the lists: %v", res.Summary)
	}

	migrationStep(t, st, c, installations.CustomerPortal, portal)
	holds("after the customer-portal reconcile")
	appConfig, _ = portalDocs(t, st)
	if got := appConfig["app"].(map[string]any)["extensions"]; !mapEqual(got, render.PortalExtensionsInclude(false, false, false)) {
		t.Errorf("the app-config includes %v, want the list without the platform's section", got)
	}
	for _, key := range keptSection {
		if appConfig[key] != nil {
			t.Errorf("the app-config keeps %s after the hand-over", key)
		}
	}
	migrationStep(t, st, c, installations.AgentPlatform, nil)
	holds("after the migration")
}

// mapEqual says whether v is the include of anchor.
func mapEqual(v any, anchor string) bool {
	m, _ := v.(map[string]any)
	return len(m) == 1 && m["$include"] == anchor
}
