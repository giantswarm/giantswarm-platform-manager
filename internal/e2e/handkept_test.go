package e2e

// A hand-kept portal moves onto the customer-portal definition in three
// actions — the customer-portal commit, the agent-platform reconcile that
// takes the portal's lists over, the customer-portal reconcile that hands the
// section over — and at no step does the running portal lose the platform's
// extensions or its skill repositories: Backstage loads the Component's
// fragment after the portal's app-config and keeps a list whole from the
// later file, so the effective list is the fragment's where it carries one,
// the app-config's otherwise. Nor does it lose the chat's key: the portal's
// user secrets carry it until the Component's own credentials Secret does.

import (
	"path"
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

// chatKeyField is the chat's Anthropic API key as both definitions name the
// supplied value.
const chatKeyField = "aiChat.anthropic.apiKey"

// The secret files of rowan's portal that can carry the chat's key: the
// portal's user secrets, and the agent-platform Component's credentials.
var (
	rowanUserSecretsPath = strings.TrimSuffix(installations.PortalConfigPath(rowan), "app-config.yaml") + "user-secrets.enc.yaml"
	rowanCredentialsPath = strings.TrimSuffix(rowanFragmentPath, "app-config.yaml") + "ai-chat-credentials.enc.yaml"
)

// handKeptUserSecrets is rowan's hand-kept user secrets as SOPS keeps them
// on record: one encrypted values text, no type stated; handKeptUserValues
// is what that text holds, as only the test knows it — the chat's key (the
// marker stands for the value) and a token nothing references.
var (
	handKeptUserValues  = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: user-secrets-backstage\n  namespace: flux-giantswarm\nstringData:\n  values: |\n    anthropic:\n      apiKey: " + render.Supplied(chatKeyField) + "\n    externalAccess:\n      mcpToken: hand-kept\n"
	handKeptUserSecrets = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: user-secrets-backstage\n  namespace: flux-giantswarm\nstringData:\n  values: " + sopsValue + "\n" + sopsBlock
)

// sopsValue and sopsBlock are a value SOPS encrypted and the block it keeps
// beneath the file, as the fleet's rule writes them.
const (
	sopsValue = "ENC[AES256_GCM,data:fixture,iv:fixture,tag:fixture,type:str]"
	sopsBlock = "sops:\n  age:\n    - recipient: age1fixture\n  encrypted_regex: ^(data|stringData)$\n  version: 3.9.0\n"
)

// sealed is a secret file as the commit leaves it on record: every value
// under data and stringData encrypted, SOPS's block beneath.
func sealed(t *testing.T, content string) string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatal(err)
	}
	m := doc.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if key := m.Content[i].Value; key == dataKey || key == "stringData" {
			for j := 1; j < len(m.Content[i+1].Content); j += 2 {
				v := m.Content[i+1].Content[j]
				v.Kind, v.Style, v.Tag, v.Value = yaml.ScalarNode, 0, "!!str", sopsValue
			}
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out) + sopsBlock
}

// migrationStep is one action's dry run over rowan with the content, its
// written files put on record as the merged pull request leaves them — a
// secret file encrypted, what it holds recorded in secrets by path — and the
// dry run again, which writes nothing: the state is the definition's fixed
// point. It answers the plan the step wrote.
func migrationStep(t *testing.T, st *stack, c *client.Client, capability string, typed map[string]any, secrets map[string]string) plan.Installation {
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
		switch {
		case f.Change == plan.ChangeUnchanged:
		case isSecretFile(f.Path):
			secrets[f.Path] = f.Content
			st.ghs.addFile(f.Repository, f.Path, sealed(t, f.Content))
			written[f.Repository+":"+f.Path] = ""
		case !strings.Contains(f.Content, "GENERATED(") && !strings.Contains(f.Content, "SUPPLIED("):
			written[f.Repository+":"+f.Path] = f.Content
			st.ghs.addFile(f.Repository, f.Path, f.Content)
		}
	}
	for _, f := range dry().Files {
		if want, ok := written[f.Repository+":"+f.Path]; ok && (f.Change != plan.ChangeUnchanged || want != "" && f.Content != want) {
			t.Errorf("%s: %s is %s the second time:\n%s\nwant\n%s", capability, f.Path, f.Change, f.Content, want)
		}
	}
	return first
}

// chatKeyHolders are the secret files on record that carry the chat's key
// into the portal's environment: each holds it (secrets) and its
// kustomization lists it, the portal's or the Component's.
func chatKeyHolders(t *testing.T, st *stack, secrets map[string]string) []string {
	t.Helper()
	var held []string
	for _, p := range []string{rowanUserSecretsPath, rowanCredentialsPath} {
		if !strings.Contains(secrets[p], render.Supplied(chatKeyField)) {
			continue
		}
		raw, _ := st.ghs.file(acmeMCs, strings.TrimSuffix(p, path.Base(p))+"kustomization.yaml")
		var k struct {
			Resources []string `yaml:"resources"`
		}
		if err := yaml.Unmarshal([]byte(raw), &k); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(k.Resources, path.Base(p)) {
			held = append(held, p)
		}
	}
	return held
}

// asksForChatKey says whether a step's plan asks for the chat's key at
// commit, and whether it keeps the key on record without asking.
func asksForChatKey(p plan.Installation) (asks, onRecord bool) {
	return slices.Contains(p.SuppliedSecrets, chatKeyField), slices.Contains(p.SuppliedOnRecord, chatKeyField)
}

// rowan's portal is hand-kept and the platform runs there, its Component's
// fragment in the hand-kept shape (the object-shaped keys alone), its chat's
// Anthropic key in its hand-kept user secrets, one encrypted values text. The
// customer-portal commit keeps the platform's section in the app-config and
// includes the list with it, and asks for the chat's key into the user
// secrets it writes whole — naming the encrypted text it replaces; the
// agent-platform reconcile then takes the lists over, the skill repositories
// read back from the app-config, and asks for no key; the next
// customer-portal reconcile hands the section over, planned as moved, and
// keeps the key on record; the agent-platform's next action, the chat no
// longer the app-config's, asks for the key into the Component's credentials
// Secret; the customer-portal's next reconcile asks for none and renders
// none. At every step the portal's effective extensions and skill
// repositories are the record's, a file on record carries the key into its
// environment, exactly one step's plan asks for it before and one after the
// hand-over, and every state is each definition's fixed point.
func TestHandKeptPortalMigrationKeepsThePlatformSection(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	st.ghs.addFiles(acmeMCs, map[string]string{
		installations.PortalConfigPath(rowan):        handKeptPortal(),
		installations.PortalKustomizationPath(rowan): strings.Replace(hubPortalKustomization, "  - app-config.yaml\n", "  - app-config.yaml\n  - user-secrets.enc.yaml\n", 1),
		rowanUserSecretsPath:                         handKeptUserSecrets,
	})
	secrets := map[string]string{rowanUserSecretsPath: handKeptUserValues}
	keyHeld := func(step string, want ...string) {
		t.Helper()
		if got := chatKeyHolders(t, st, secrets); !slices.Equal(got, want) {
			t.Errorf("%s: the chat's key reaches the portal from %v, want %v", step, got, want)
		}
	}
	asks := func(step string, p plan.Installation, wantAsks, wantOnRecord bool) {
		t.Helper()
		if a, r := asksForChatKey(p); a != wantAsks || r != wantOnRecord {
			t.Errorf("%s: asks for the chat's key %v, keeps it on record %v; want %v, %v (supplied %v, on record %v)", step, a, r, wantAsks, wantOnRecord, p.SuppliedSecrets, p.SuppliedOnRecord)
		}
	}
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
	keyHeld("on record", rowanUserSecretsPath)

	portal := rowanPortalInputs(map[string]any{enabledKey: false})
	commit := migrationStep(t, st, c, installations.CustomerPortal, portal, secrets)
	holds("after the customer-portal commit")
	keyHeld("after the customer-portal commit", rowanUserSecretsPath)
	asks("the customer-portal commit", commit, true, false)
	for _, f := range commit.Files {
		if f.Path == rowanUserSecretsPath && (f.Change != plan.ChangeUpdate || !slices.Equal(f.Replaced, []string{"stringData.values"})) {
			t.Errorf("the customer-portal commit writes the hand-kept user secrets %s, replacing %v; want an update that names the values text it replaces", f.Change, f.Replaced)
		}
	}
	appConfig, _ = portalDocs(t, st)
	if got := appConfig["app"].(map[string]any)["extensions"]; !mapEqual(got, render.PortalExtensionsInclude(true, true, false)) {
		t.Errorf("the app-config includes %v, want the list with the platform's section and the chat", got)
	}
	for _, key := range keptSection {
		if appConfig[key] == nil {
			t.Errorf("the app-config lost %s while the fragment does not own the lists", key)
		}
	}

	takeOver := migrationStep(t, st, c, installations.AgentPlatform, nil, secrets)
	holds("after the agent-platform reconcile")
	keyHeld("after the agent-platform reconcile", rowanUserSecretsPath)
	asks("the agent-platform reconcile", takeOver, false, false)
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

	handOver := migrationStep(t, st, c, installations.CustomerPortal, portal, secrets)
	holds("after the customer-portal reconcile")
	keyHeld("after the customer-portal reconcile", rowanUserSecretsPath)
	asks("the customer-portal reconcile", handOver, false, true)
	appConfig, _ = portalDocs(t, st)
	if got := appConfig["app"].(map[string]any)["extensions"]; !mapEqual(got, render.PortalExtensionsInclude(false, false, false)) {
		t.Errorf("the app-config includes %v, want the list without the platform's section", got)
	}
	for _, key := range keptSection {
		if appConfig[key] != nil {
			t.Errorf("the app-config keeps %s after the hand-over", key)
		}
	}
	credentials := migrationStep(t, st, c, installations.AgentPlatform, nil, secrets)
	holds("after the migration")
	asks("the agent-platform's credentials", credentials, true, false)
	keyHeld("after the agent-platform's credentials", rowanUserSecretsPath, rowanCredentialsPath)

	// The key is the Component's now: the portal's next reconcile asks for
	// none and renders none — the copy in the user secrets on record goes
	// with the file's next rewrite.
	after := migrationStep(t, st, c, installations.CustomerPortal, portal, secrets)
	asks("the customer-portal reconcile after the move", after, false, false)
	for _, f := range after.Files {
		if f.Path == rowanUserSecretsPath && (f.Change != plan.ChangeUnchanged || strings.Contains(f.Content, "anthropic:")) {
			t.Errorf("the user secrets after the move are %s and carry the chat's key:\n%s", f.Change, f.Content)
		}
	}
	keyHeld("after the move", rowanUserSecretsPath, rowanCredentialsPath)
}

// mapEqual says whether v is the include of anchor.
func mapEqual(v any, anchor string) bool {
	m, _ := v.(map[string]any)
	return len(m) == 1 && m["$include"] == anchor
}

// A dry run over a hand-kept portal whose encrypted user secrets carry a key
// the render drops names it, without decrypting anything: the plan's file
// lists the value under its readable key as dropped and the values text as
// replaced whole, and the comparison plans the key's removal by name.
func TestDryRunNamesTheEncryptedKeysTheCommitDrops(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	st.ghs.addFiles(acmeMCs, map[string]string{
		installations.PortalConfigPath(rowan):        handKeptPortal(),
		installations.PortalKustomizationPath(rowan): hubPortalKustomization,
		rowanUserSecretsPath:                         strings.Replace(handKeptUserSecrets, "\n"+plan.SOPSKey+":", "\n  EXTERNAL_ACCESS_MCP_TOKEN: "+sopsValue+"\n"+plan.SOPSKey+":", 1),
	})
	c := st.mcpClient(t, aliceToken)
	putOnRecord(t, st, c, rowan, kagentEnabled())
	portal := rowanPortalInputs(map[string]any{enabledKey: false})
	out, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgCapability: installations.CustomerPortal, tools.ArgInputs: portal})
	if isErr {
		t.Fatal(text)
	}
	const dropped = "stringData.EXTERNAL_ACCESS_MCP_TOKEN"
	var found bool
	for _, f := range findPlan(t, out, rowan).Files {
		if f.Path == rowanUserSecretsPath {
			found = true
			if !slices.Equal(f.Dropped, []string{dropped}) || !slices.Equal(f.Replaced, []string{"stringData.values"}) {
				t.Errorf("the user secrets drop %v and replace %v, want %s dropped and the values text replaced", f.Dropped, f.Replaced, dropped)
			}
		}
	}
	if !found {
		t.Fatalf("the dry run renders no %s", rowanUserSecretsPath)
	}
	var planned string
	for _, d := range differencesOf(verifyPortal(t, c, rowan, portal), acmeMCs+":"+rowanUserSecretsPath) {
		if d.Path == dropped {
			planned = d.Planned
		}
	}
	if !strings.HasPrefix(planned, "Removed: "+dropped+" ") {
		t.Errorf("the comparison plans %s as %q, want its removal by name", dropped, planned)
	}
}
