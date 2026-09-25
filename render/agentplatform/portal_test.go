package agentplatform

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The names of the portal cases: the fleet's hub and its organisation, a
// Giant Swarm-owned installation with a portal of its own, a customer's
// aggregator that hosts the organisation's portal and a sibling of its.
const (
	portalCaseHub        = "aspen"
	portalCaseOrg        = "giantswarm"
	portalCaseOwn        = "gopher"
	portalCaseCustomer   = "oakridge"
	portalCaseAggregator = "kestrel"
	portalCaseSibling    = "plover"
)

// The platform's portal section lands in the portal hosted on the installation
// itself, else in the organisation's portal on a sibling that is not hand-kept,
// else nowhere: a hand-kept sibling portal (the hub's Dev Portal) and another
// organisation's portal take no Component. The Component sets the portal's
// lists only where the portal is not hand-kept.
func TestHostedPortal(t *testing.T) {
	hub := PortalRef{Installation: portalCaseHub, Customer: portalCaseOrg, Domain: "portal." + portalCaseHub + ".example.io", HandKept: true}
	own := PortalRef{Installation: portalCaseOwn, Customer: portalCaseOrg, Domain: "portal." + portalCaseOwn + ".example.io", HandKept: true}
	aggregator := PortalRef{Installation: portalCaseAggregator, Customer: portalCaseCustomer, Domain: "portal." + portalCaseAggregator + ".oakridge.example"}
	cases := []struct {
		name      string
		in        Installation
		host      string
		ownsLists bool
	}{
		{"own portal first, whatever the record's order", Installation{Name: portalCaseOwn, Customer: portalCaseOrg, Portals: []PortalRef{hub, own}}, portalCaseOwn, false},
		{"own portal, rendered", Installation{Name: portalCaseAggregator, Customer: portalCaseCustomer, Portals: []PortalRef{hub, aggregator}}, portalCaseAggregator, true},
		{"the organisation's portal on a sibling", Installation{Name: portalCaseSibling, Customer: portalCaseCustomer, Portals: []PortalRef{hub, aggregator}}, portalCaseAggregator, true},
		{"the hub's hand-kept portal alone", Installation{Name: "marmot", Customer: portalCaseOrg, Portals: []PortalRef{hub}}, "", false},
		{"another organisation's portal alone", Installation{Name: portalCaseSibling, Customer: portalCaseCustomer, Portals: []PortalRef{hub}}, "", false},
		{"no portal", Installation{Name: portalCaseSibling, Customer: portalCaseCustomer}, "", false},
	}
	for _, c := range cases {
		in := &Input{Installation: c.in}
		if got := in.portalHost(); got != c.host {
			t.Errorf("%s: host %q, want %q", c.name, got, c.host)
		}
		if got := in.portalOwnsLists(); got != c.ownsLists {
			t.Errorf("%s: owns lists %v, want %v", c.name, got, c.ownsLists)
		}
	}
}

// The organisation and the portal host of the inputs built by hand below.
const (
	testOrganisation = "acme"
	testPortalHost   = "maple"
)

// The fragment's extension list is the shared one with the platform's section
// and, where the chat is on, with the chat, and, where the hosted portal's
// Grafana plugin is wired on record, with the dashboards card: Backstage
// keeps the fragment's list, so the chat's entries and the card's switch
// have to be in it. A hand-kept portal gets no list.
func TestPortalFragmentExtensions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		portal PortalRef
		chat   bool
		want   string
	}{
		{"not wired", PortalRef{Installation: testPortalHost, Customer: testOrganisation}, false, "shared-config.yaml#extensionsAgentPlatform"},
		{"wired", PortalRef{Installation: testPortalHost, Customer: testOrganisation, GrafanaWired: true}, false, "shared-config.yaml#extensionsAgentPlatformGrafanaDashboards"},
		{"the chat", PortalRef{Installation: testPortalHost, Customer: testOrganisation}, true, "shared-config.yaml#extensionsAgentPlatformAiChat"},
		{"the chat, wired", PortalRef{Installation: testPortalHost, Customer: testOrganisation, GrafanaWired: true}, true, "shared-config.yaml#extensionsAgentPlatformAiChatGrafanaDashboards"},
		{"hand-kept, wired", PortalRef{Installation: testPortalHost, Customer: testOrganisation, GrafanaWired: true, HandKept: true}, false, ""},
		{"hand-kept, the chat", PortalRef{Installation: testPortalHost, Customer: testOrganisation, HandKept: true}, true, ""},
	} {
		in := &Input{Installation: Installation{Name: testPortalHost, Customer: testOrganisation, Portals: []PortalRef{tc.portal}}, AIChat: AIChat{Enabled: tc.chat, Model: testChatModel}}
		app, _ := fragmentValue(in.portalAppConfig(), "app").(render.Map)
		extensions, _ := fragmentValue(app, "extensions").(render.Map)
		if got, _ := fragmentValue(extensions, "$include").(string); got != tc.want {
			t.Errorf("%s: the fragment includes %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The fragment carries the skill repositories the person lists under
// agentPlatform.skills.repositories, on a rendered portal and on a hand-kept
// one alike — there the list is read back from the portal's own app-config,
// so the list the fragment sets is the portal's — beside the kagent
// installation where kagent runs, and alone where it does not; none listed,
// no key, the plugin's empty default. Listed without a portal to carry them,
// the input is refused.
func TestPortalFragmentSkills(t *testing.T) {
	const portal2x = ">=2.1.0 <3.0.0"
	repositories := []string{"https://github.com/example/agent-skills", "https://github.com/example/more-skills"}
	for _, tc := range []struct {
		name         string
		handKept     bool
		kagent       bool
		repositories []string
		want         []string
	}{
		{"rendered portal", false, true, repositories, repositories},
		{"hand-kept portal", true, true, repositories, repositories},
		{"without kagent", false, false, repositories, repositories},
		{"none listed", false, true, nil, nil},
	} {
		in := &Input{Installation: Installation{Name: testPortalHost, Customer: testOrganisation, Portals: []PortalRef{{Installation: testPortalHost, Customer: testOrganisation, HandKept: tc.handKept, ChartLine: portal2x}}},
			Components: map[string]bool{componentKagent: tc.kagent}, SkillRepositories: tc.repositories}
		platform, _ := fragmentValue(in.portalAppConfig(), "agentPlatform").(render.Map)
		skills, _ := fragmentValue(platform, "skills").(render.Map)
		if got, _ := fragmentValue(skills, "repositories").([]string); !slices.Equal(got, tc.want) {
			t.Errorf("%s: agentPlatform.skills.repositories %v, want %v", tc.name, got, tc.want)
		}
		if kagent := fragmentValue(platform, "kagent") != nil; kagent != tc.kagent {
			t.Errorf("%s: agentPlatform.kagent rendered %v, want %v", tc.name, kagent, tc.kagent)
		}
		if platform == nil && (tc.kagent || len(tc.want) > 0) || platform != nil && !tc.kagent && len(tc.want) == 0 {
			t.Errorf("%s: agentPlatform %v", tc.name, platform)
		}
	}
	in := &Input{Installation: Installation{Name: testPortalHost, Customer: testOrganisation}, SkillRepositories: repositories}
	if err := in.checkRecord(); err == nil || !strings.Contains(err.Error(), "skills.repositories") || !errors.Is(err, ErrInput) {
		t.Errorf("skill repositories without a portal: %v", err)
	}
}

// fragmentValue is the value of key in m, nil where m has no such entry.
func fragmentValue(m render.Map, key string) any {
	for _, entry := range m {
		if entry.Key == key {
			return entry.Value
		}
	}
	return nil
}

// testChatModel is the model the chat tests choose.
const testChatModel = "claude-opus-5"

// With the chat on, the fragment carries the aiChat block — Anthropic's API
// with the key from the chart's environment, the model, the portal's own
// actions server with the person's Backstage token and the installation's
// muster with the sign-in provider — the actions server's tool naming and
// the actions the service lists for it; on a hand-kept portal the same
// blocks and no list. Off, none of the three keys.
func TestPortalFragmentChat(t *testing.T) {
	portal := PortalRef{Installation: testPortalHost, Customer: testOrganisation, Domain: "portal." + testPortalHost + ".example"}
	for _, handKept := range []bool{false, true} {
		portal.HandKept = handKept
		in := &Input{Installation: Installation{Name: testPortalHost, BaseDomain: testPortalHost + ".example", Customer: testOrganisation, Portals: []PortalRef{portal}}, AIChat: AIChat{Enabled: true, Model: testChatModel}}
		m := in.portalAppConfig()
		chat, _ := fragmentValue(m, "aiChat").(render.Map)
		anthropic, _ := fragmentValue(chat, "anthropic").(render.Map)
		if got := fragmentValue(anthropic, "apiKey"); got != anthropicKeyEnv || fragmentValue(chat, "model") != testChatModel {
			t.Errorf("hand-kept %v: the chat's provider and model: %v", handKept, chat)
		}
		servers, _ := fragmentValue(chat, "mcp").([]render.Map)
		if len(servers) != 2 || fragmentValue(servers[0], "name") != chatActionsServer || fragmentValue(servers[0], "url") != "https://"+portal.Domain+chatActionsPath || fragmentValue(servers[0], "useBackstageUserToken") != true ||
			fragmentValue(servers[1], "name") != chatMusterServer || fragmentValue(servers[1], "url") != "https://muster."+testPortalHost+".example/mcp" || fragmentValue(servers[1], "authProvider") != "oidc-"+testPortalHost {
			t.Errorf("hand-kept %v: the chat's servers: %v", handKept, servers)
		}
		tools, _ := fragmentValue(m, "mcpActions").(render.Map)
		backend, _ := fragmentValue(m, "backend").(render.Map)
		actions, _ := fragmentValue(backend, "actions").(render.Map)
		if fragmentValue(tools, "namespacedToolNames") != false || fragmentValue(actions, "pluginSources") == nil || fragmentValue(actions, "filter") == nil {
			t.Errorf("hand-kept %v: the actions server's configuration: %v %v", handKept, tools, backend)
		}
		if hasList := fragmentValue(m, "app") != nil; hasList == handKept {
			t.Errorf("hand-kept %v: the fragment sets the extension list: %v", handKept, hasList)
		}
		if fields := in.suppliedSecretFields(); len(fields) != 1 || fields[0] != fieldAnthropicKey {
			t.Errorf("hand-kept %v: the supplied fields %v, want the key alone", handKept, fields)
		}
	}
	// The chat by hand in the portal's app-config: the same blocks, the key the portal's — no Secret, nothing supplied.
	portal.HandKeptChat = true
	byHand := &Input{Installation: Installation{Name: testPortalHost, BaseDomain: testPortalHost + ".example", Customer: testOrganisation, Portals: []PortalRef{portal}}, AIChat: AIChat{Enabled: true, Model: testChatModel}}
	if chat := fragmentValue(byHand.portalAppConfig(), "aiChat"); chat == nil || byHand.chatKeyIsComponents() || len(byHand.suppliedSecretFields()) != 0 {
		t.Errorf("a hand-kept chat: block %v, the Component carries the key %v, supplied %v", chat != nil, byHand.chatKeyIsComponents(), byHand.suppliedSecretFields())
	}
	r := &render.Result{}
	byHand.portalFiles(r, "giantswarm/acme-management-clusters", "management-clusters/"+testPortalHost+"/extras/backstage/"+portalDir, nil)
	dir := "giantswarm/acme-management-clusters/management-clusters/" + testPortalHost + "/extras/backstage/" + portalDir + "/"
	var files []string
	for path := range r.Tree() {
		if strings.HasPrefix(path, dir) {
			files = append(files, strings.TrimPrefix(path, dir))
		}
	}
	if len(files) != 3 || slices.Contains(files, portalCredentialsFile) {
		t.Errorf("a hand-kept chat renders %v, want the fragment, the values and the Component alone", files)
	}
	in := &Input{Installation: Installation{Name: testPortalHost, Customer: testOrganisation, Portals: []PortalRef{portal}}}
	m := in.portalAppConfig()
	for _, key := range []string{"aiChat", "mcpActions", "backend"} {
		if fragmentValue(m, key) != nil {
			t.Errorf("the chat off: the fragment carries %s", key)
		}
	}
	if fields := in.suppliedSecretFields(); len(fields) != 0 {
		t.Errorf("the chat off: the supplied fields %v, want none", fields)
	}
}

// A chat on Vertex AI names the provider and the Google project, location
// and the mounted credentials file in its block, no API key; the Component's
// values carry the project and location the chart exports; the credentials
// Secret carries the service account's JSON the person supplies, not a key;
// on Anthropic's API none of the Google keys render.
func TestPortalFragmentVertexChat(t *testing.T) {
	portal := PortalRef{Installation: testPortalHost, Customer: testOrganisation, Domain: "portal." + testPortalHost + ".example"}
	const project, location = "example-project", "eu"
	in := &Input{Installation: Installation{Name: testPortalHost, BaseDomain: testPortalHost + ".example", Customer: testOrganisation, Portals: []PortalRef{portal}},
		AIChat: AIChat{Enabled: true, Model: testChatModel, Provider: providerVertex, Google: GoogleVertex{Project: project, Location: location}}}
	chat, _ := fragmentValue(in.portalAppConfig(), "aiChat").(render.Map)
	anthropic, _ := fragmentValue(chat, "anthropic").(render.Map)
	google, _ := fragmentValue(chat, "google").(render.Map)
	if fragmentValue(anthropic, "provider") != providerVertex || fragmentValue(anthropic, "apiKey") != nil || fragmentValue(google, "project") != project || fragmentValue(google, "location") != location || fragmentValue(google, "keyFilename") != googleCredentialsPath {
		t.Errorf("the Vertex block: %v", chat)
	}
	values, _ := fragmentValue(in.portalValues(), "google").(render.Map)
	if fragmentValue(values, "project") != project || fragmentValue(values, "location") != location {
		t.Errorf("the Component's values: %v", in.portalValues())
	}
	if fields := in.suppliedSecretFields(); len(fields) != 1 || fields[0] != fieldGoogleCredentials {
		t.Errorf("the supplied fields %v, want the credentials alone", fields)
	}
	secret := string(in.chatCredentials(map[string]string{fieldGoogleCredentials: "x"}).Content)
	if !strings.Contains(secret, "credentialsJson: x") || strings.Contains(secret, "apiKey") {
		t.Errorf("the credentials Secret:\n%s", secret)
	}

	in.AIChat.Provider = providerAnthropic
	chat, _ = fragmentValue(in.portalAppConfig(), "aiChat").(render.Map)
	if fragmentValue(chat, "google") != nil || fragmentValue(in.portalValues(), "google") != nil {
		t.Errorf("on Anthropic's API the Google keys render: %v %v", chat, in.portalValues())
	}
}

// A portal's chart line admits versions from its floor: the lower bound of
// the bounded range the customer-portal definition writes, or the tag itself.
// The fragment names the agents' Flux identity where that floor lies before
// the plugin's removal of the key, and a line of another form is refused.
func TestPortalChartFloor(t *testing.T) {
	for _, tc := range []struct {
		line  string
		floor string
		reads bool
	}{
		{">=0.244.7 <1.0.0", "0.244.7", true},
		{">=1.0.0 <2.0.0", "1.0.0", true},
		{">=1.1.0 <2.0.0", "1.1.0", false},
		{">=2.1.0 <3.0.0", "2.1.0", false},
		{">=0.244.7 <3.0.0", "0.244.7", true},
		{"0.120.0", "0.120.0", true},
		{"2.53.2", "2.53.2", false},
	} {
		floor, err := portalChartFloor(tc.line)
		if err != nil || floor.String() != tc.floor {
			t.Errorf("%q: floor %v, %v; want %s", tc.line, floor, err, tc.floor)
			continue
		}
		in := &Input{Installation: Installation{Customer: testOrganisation, Portals: []PortalRef{{Installation: testPortalHost, Customer: testOrganisation, ChartLine: tc.line}}}}
		if got := in.portalReadsFluxServiceAccount(); got != tc.reads {
			t.Errorf("%q: reads the Flux identity %v, want %v", tc.line, got, tc.reads)
		}
	}
	for _, line := range []string{"", "x.x.x", "^0.244.7", ">=0.244.7", "<1.0.0", ">=0.244.7 <1.0.0 || >=2.0.0", "latest", ">=a.b.c <1.0.0"} {
		if _, err := portalChartFloor(line); err == nil {
			t.Errorf("%q: a line of another form is an error", line)
		}
	}
	if (&Input{Installation: Installation{Customer: testOrganisation}}).portalReadsFluxServiceAccount() {
		t.Error("an installation without a hosted portal reads nothing")
	}
}

// The Component's values carry the fragment's checksum, the sha256 of the
// file its ConfigMap holds, where the hosted portal's chart line resolves to
// a chart that rolls the pod on it; a changed fragment changes the checksum.
// A line whose charts all precede portalFragmentChecksum, and a portal whose
// line is not on record, get none: their chart's schema refuses the key.
func TestPortalFragmentChecksum(t *testing.T) {
	for _, tc := range []struct {
		line     string
		checksum bool
	}{
		{">=2.1.0 <3.0.0", true},
		{">=1.0.0 <2.0.0", false},
		{">=2.1.0 <" + portalFragmentChecksum, false},
		{portalFragmentChecksum, true},
		{"2.60.1", false},
		{"", false},
	} {
		portal := PortalRef{Installation: testPortalHost, Customer: testOrganisation, ChartLine: tc.line}
		in := &Input{Installation: Installation{Name: testPortalHost, Customer: testOrganisation, Portals: []PortalRef{portal}}}
		checksum := fragmentChecksum(t, in)
		if tc.checksum != (checksum != "") {
			t.Errorf("%q: checksum %q, want one %v", tc.line, checksum, tc.checksum)
			continue
		}
		if checksum == "" {
			continue
		}
		r := &render.Result{}
		in.portalFiles(r, "giantswarm/acme-management-clusters", "extras/backstage/"+portalDir, nil)
		var cm struct {
			Data map[string]string `yaml:"data"`
		}
		if err := yaml.Unmarshal(r.Tree()["giantswarm/acme-management-clusters/extras/backstage/"+portalDir+"/app-config.yaml"], &cm); err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("%x", sha256.Sum256([]byte(cm.Data[portalAppConfigFile]))); checksum != want {
			t.Errorf("%q: checksum %s, the fragment's ConfigMap hashes to %s", tc.line, checksum, want)
		}
		in.AIChat = AIChat{Enabled: true, Model: testChatModel}
		if fragmentChecksum(t, in) == checksum {
			t.Errorf("%q: the chat on leaves the checksum %s", tc.line, checksum)
		}
	}
}

// fragmentChecksum is the checksum of the Component's one extraAppConfig
// entry, empty where it has none.
func fragmentChecksum(t *testing.T, in *Input) string {
	t.Helper()
	backstage, _ := fragmentValue(in.portalValues(), "backstage").(render.Map)
	entries, _ := fragmentValue(backstage, "extraAppConfig").([]render.Map)
	if len(entries) != 1 {
		t.Fatalf("the Component mounts %d fragments, want one", len(entries))
	}
	checksum, _ := fragmentValue(entries[0], "checksum").(string)
	return checksum
}

// Where kagent runs on an installation whose hosted portal is on record
// without its chart line, or with one of another form, the plan is
// refused naming the portal and the file; a portal of another organisation,
// or an installation without kagent, is not held to it.
func TestCheckRecordRefusesAPortalWithoutItsChartLine(t *testing.T) {
	input, secrets := loadInput(t, shapePublicCustomer)
	portals := input["installation"].(map[string]any)["portals"].([]any)
	own := portals[0].(map[string]any)
	delete(own, "chartLine")
	_, err := Render(input, secrets, render.ModeCommit)
	if !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "installation.portals[kestrel].chartLine") || !strings.Contains(err.Error(), "management-clusters/kestrel/extras/backstage/backstage/kustomization.yaml") {
		t.Fatalf("a portal without its chart line: %v", err)
	}
	own["chartLine"] = "x.x.x"
	if _, err := Render(input, secrets, render.ModeCommit); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), `"x.x.x"`) {
		t.Fatalf("a portal with a chart line of another form: %v", err)
	}
	own["chartLine"] = ">=0.244.7 <1.0.0"
	delete(portals[1].(map[string]any), "chartLine")
	if _, err := Render(input, secrets, render.ModeCommit); err != nil {
		t.Fatalf("the hub's portal without its chart line is not this organisation's: %v", err)
	}
	in := &Input{Installation: Installation{Customer: testOrganisation, ChartLine: lineThree, Portals: []PortalRef{{Installation: testPortalHost, Customer: testOrganisation}}}, Components: map[string]bool{}}
	if err := in.checkRecord(); err != nil {
		t.Fatalf("without kagent the fragment carries no agentPlatform section, so the line is not needed: %v", err)
	}
	in.Components[componentKagent] = true
	if err := in.checkRecord(); !errors.Is(err, ErrInput) {
		t.Fatalf("with kagent the line is needed: %v", err)
	}
}

// TestGoldensCoverPortalLines holds the golden shapes to both sides of the
// removal: the fragment of a shape whose hosted portal follows a line
// before backstage 1.1.0 names the agents' Flux identity, one whose portal
// follows a later line does not, and at least one shape renders each.
func TestGoldensCoverPortalLines(t *testing.T) {
	rendered := map[bool]bool{}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		host := in.portalHost()
		if host == "" || !in.kagent() {
			continue
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		fragment := string(result.Tree()["giantswarm/"+in.Installation.Customer+"-management-clusters/management-clusters/"+host+"/extras/backstage/"+portalDir+"/app-config.yaml"])
		named := strings.Contains(fragment, "fluxServiceAccountName: "+fluxServiceAccount+"\n")
		if want := in.portalReadsFluxServiceAccount(); named != want {
			t.Errorf("%s (portal %s on %s): the fragment names the Flux identity: %v, want %v\n%s", shape, host, in.hostedPortal().ChartLine, named, want, fragment)
		}
		rendered[named] = true
	}
	for _, named := range []bool{true, false} {
		if !rendered[named] {
			t.Errorf("no golden shape renders a portal whose fragment names the Flux identity: %v", named)
		}
	}
}

// The chat's Anthropic key is written into the credentials Secret
// base64-encoded: the chart copies the chart value anthropic.apiKey under its
// secrets Secret's data, which Kubernetes takes base64-encoded, and a
// hand-kept portal carries it so in its user secrets. A marker stays a marker.
func TestPortalChatCredentialsEncodeTheKey(t *testing.T) {
	portal := PortalRef{Installation: testPortalHost, Customer: testOrganisation, Domain: "portal." + testPortalHost + ".example"}
	in := &Input{Installation: Installation{Name: testPortalHost, BaseDomain: testPortalHost + ".example", Customer: testOrganisation, Portals: []PortalRef{portal}},
		AIChat: AIChat{Enabled: true, Model: testChatModel}}
	const key = "sk-ant-fixture"
	if secret := string(in.chatCredentials(map[string]string{fieldAnthropicKey: key}).Content); !strings.Contains(secret, "apiKey: "+base64.StdEncoding.EncodeToString([]byte(key))+"\n") || strings.Contains(secret, key) {
		t.Errorf("the credentials Secret:\n%s", secret)
	}
	marker := render.Supplied(fieldAnthropicKey)
	if secret := string(in.chatCredentials(map[string]string{fieldAnthropicKey: marker}).Content); !strings.Contains(secret, "apiKey: "+marker+"\n") {
		t.Errorf("the marker encoded:\n%s", secret)
	}
}

// A portal's client id is a client of its host's Dex: the host trusts it,
// and no other installation the portal lists does — neither in the
// audiences (muster, the kagent UI, the edge) nor among the
// authenticator's trusted peers. Every portal is trusted through the
// definition's client wherever it signs people in.
func TestPortalClientIDStaysOnItsHost(t *testing.T) {
	const hostClient = "host-portal-client-on-record"
	hub := PortalRef{Installation: portalCaseHub, Customer: portalCaseOrg, Domain: "portal." + portalCaseHub + ".example.io", ClientID: hostClient, HandKept: true}
	for _, tc := range []struct {
		name, installation string
		want               []string
	}{
		{"the host", portalCaseHub, []string{hostClient, render.PortalDexClientID}},
		{"a test installation the portal lists", portalCaseOwn, []string{render.PortalDexClientID}},
		{"a customer installation the portal lists", portalCaseSibling, []string{render.PortalDexClientID}},
	} {
		in := &Input{Installation: Installation{Name: tc.installation, Customer: portalCaseOrg, Portals: []PortalRef{hub}}}
		if got := in.portalAudiences(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: portal audiences %v, want %v", tc.name, got, tc.want)
		}
		if tc.installation == portalCaseHub {
			continue
		}
		if got := in.audiences(); slices.Contains(got, hostClient) {
			t.Errorf("%s: audiences %v carry the host's portal client", tc.name, got)
		}
		dex, err := yaml.Marshal(in.dexPatch())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(dex), hostClient) {
			t.Errorf("%s: the dex patch carries the host's portal client:\n%s", tc.name, dex)
		}
	}
}
