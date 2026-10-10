package installations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

const readBackSchema = `{
  "type": "object",
  "x-files": {
    "values": {"repository": "management-clusters", "path": "management-clusters/<name>/values.yaml"},
    "patch": {"repository": "configs", "path": "installations/<name>/patch.yaml"}
  },
  "properties": {
    "installation": {"type": "object", "properties": {"name": {"type": "string"}}},
    "serving": {"type": "object", "properties": {
      "enabled": {"type": "boolean", "default": false, "x-readback": {"file": "values", "key": "components.serving.enabled"}},
      "reason": {"type": "string", "x-readback": {"file": "values", "key": "components.serving.enabled", "kind": "comment", "skip": "^Rendered"}}
    }},
    "tunnel": {"type": "object", "properties": {
      "enabled": {"type": "boolean", "default": false, "x-readback": {"file": "values", "key": "tunnel", "kind": "present"}}
    }},
    "portal": {"type": "object", "properties": {
      "domain": {"type": "string", "x-readback": {"file": "values", "key": "app.baseUrl", "kind": "host"}},
      "organization": {"type": "string", "default": "Giant Swarm"},
      "supportUrl": {"type": "string", "x-readback": {"file": "values", "key": "resources.[icon=LiveHelp].url"}},
      "grafana": {"type": "string", "x-readback": {"file": "values", "key": ["grafana.hosts.0.domain", "grafana.domain"]}}
    }},
    "federation": {"type": "object", "properties": {
      "broker": {"type": "string", "x-readback": {"file": "values", "key": "broker.tokenUrl", "kind": "installation", "prefix": "muster."}}
    }},
    "missing": {"type": "object", "properties": {
      "key": {"type": "string", "x-readback": {"file": "patch", "key": "nothing.here"}}
    }}
  }
}`

// readBackFilesSchema reads one choice from two files: a fragment's
// document, addressed by its dotted ConfigMap key in brackets, before the
// values.
const readBackFilesSchema = `{
  "type": "object",
  "x-files": {
    "values": {"repository": "management-clusters", "path": "management-clusters/<name>/values.yaml"},
    "fragment": {"repository": "management-clusters", "path": "management-clusters/<name>/fragment.yaml", "document": "data.[app-config.fragment.yaml]"}
  },
  "properties": {
    "chat": {"type": "object", "properties": {
      "enabled": {"type": "boolean", "default": false, "x-readback": {"file": ["fragment", "values"], "key": "aiChat", "kind": "present"}},
      "model": {"type": "string", "x-readback": {"file": ["fragment", "values"], "key": "aiChat.model"}}
    }}
  }
}`

// The inputs the tests read back or find unset, by dotted input key.
const (
	keyEnabled         = "enabled"
	keyGitHub          = "github"
	keyRepositories    = "repositories"
	boardRoadmap       = "roadmap"
	keySkills          = "skills"
	keyProvider        = "provider"
	inputChatEnabled   = "chat.enabled"
	inputChatModel     = "chat.model"
	inputAIChatEnabled = "aiChat.enabled"
	inputHiveEnabled   = "hive.enabled"
	inputAIChatModel   = "aiChat.model"
	inputTunnelEnabled = "tunnel.enabled"
	inputSupportURL    = "portal.supportUrl"
	inputGrafanaDomain = "plugins.grafana.domain"
	inputTokenBroker   = "federation.tokenBroker"
	inputSignIn        = "federation.signInInstallation"
	inputBroker        = "federation.broker"
)

// The read-back fixture: the installation read for, its base domain, its
// portal's host, the file its choices are read from, another installation
// of its organisation, and the Grafana it links.
const (
	readBackName    = "rowan"
	readBackDomain  = "rowan.acme.test"
	readBackPortal  = "portal.rowan.acme.test"
	readBackValues  = "acme/mcs:management-clusters/rowan/values.yaml"
	readBackSibling = "birch"
	readBackGrafana = "https://grafana.acme.test"
)

func readBackFixture(t *testing.T) *inputSchema {
	t.Helper()
	var s inputSchema
	if err := json.Unmarshal([]byte(readBackSchema), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

var readBackInstallation = Installation{Name: readBackName, BaseDomain: readBackDomain, Repositories: Repositories{Configs: "acme/configs", ManagementClusters: "acme/mcs"}}

// files answers the files on record by repository:path; a path not there is
// gh.ErrNotFound, as the reader reports it.
func files(m map[string]string) Reader {
	return func(_ context.Context, repository, path string) (string, error) {
		if content, ok := m[repository+":"+path]; ok {
			return content, nil
		}
		return "", gh.ErrNotFound
	}
}

// readBackRegistry is the registry an installation kind resolves a host
// among: another installation of the organisation, and one on a domain of
// the same shape that is nobody's.
var readBackRegistry = []Installation{{Name: readBackSibling, BaseDomain: "birch.acme.test"}, {Name: "alder"}}

// The kinds read the file the definition renders the fileset key to: value
// takes the leaf, present says whether the key exists, host the host of a
// URL, installation the installation whose base domain the host is once
// the service label is stripped (the installation read for here), comment
// the lines written above the key, their markers stripped; a [key=value]
// step selects a list's entry, a list of keys answers from the first on
// record (the deprecated form here, the current one absent); a key or file
// not on record yields nothing.
func TestReadBackKinds(t *testing.T) {
	read := files(map[string]string{
		readBackValues: "components:\n  serving:\n    # Serving on, by hand:\n    #   the GPU pool is in (acme/platform#7).\n    enabled: true\ntunnel:\n  port: 8443\napp:\n  baseUrl: https://portal.rowan.acme.test/\n" +
			"broker:\n  tokenUrl: https://muster.rowan.acme.test/oauth/token\nresources:\n  - $include: shared.yaml#docs\n  - label: Support\n    icon: LiveHelp\n    url: https://support.acme.test/\ngrafana:\n  domain: https://grafana.acme.test\n",
	})
	got, err := readBack(context.Background(), read, readBackInstallation, "", readBackRegistry, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"serving.enabled": true, "serving.reason": "Serving on, by hand:\n  the GPU pool is in (acme/platform#7).", inputTunnelEnabled: true, "portal.domain": readBackPortal,
		inputSupportURL: "https://support.acme.test/", "portal.grafana": readBackGrafana, inputBroker: readBackName,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// A comment kind reads the comment above the key: none where the key
// carries none, or the comment matches the skip (the one the definition
// writes itself); a comment above a list entry selected by [key=value] is
// the entry's, through YAML text in a scalar too.
func TestReadBackCommentKind(t *testing.T) {
	for _, c := range []struct{ name, values, want string }{
		{"no comment", "components:\n  serving:\n    enabled: true\n", ""},
		{"the definition's own", "components:\n  serving:\n    # Rendered by the definition.\n    enabled: true\n", ""},
		{"a comment with blank marker lines", "components:\n  serving:\n    #\n    # Serving on.\n    #\n    enabled: true\n", "Serving on."},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := readBack(context.Background(), files(map[string]string{readBackValues: c.values}), readBackInstallation, "", readBackRegistry, readBackFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			if reason, _ := got["serving.reason"].(string); reason != c.want {
				t.Errorf("read back %q, want %q", reason, c.want)
			}
		})
	}
	node := &yaml.Node{}
	if err := yaml.Unmarshal([]byte("patches:\n  - patch: |-\n      - op: add\n        path: /x\n        value: 1\n      # Held for the window.\n      - op: replace\n        path: /spec/ref/semver\n        value: \"4.1.0\"\n    target:\n      kind: OCIRepository\n"), node); err != nil {
		t.Fatal(err)
	}
	if got, ok := commentAt(node, splitKey("patches.[target.kind=OCIRepository].patch.[path=/spec/ref/semver]")); !ok || got != "Held for the window." {
		t.Errorf("the entry's comment: %q %v", got, ok)
	}
	for _, path := range []string{"patches.[target.kind=OCIRepository].patch.[path=/x]", "patches.[target.kind=Secret].patch", "patches.0.patch.[path=/spec/ref/semver].value", "patches.0.target.kind.deeper"} {
		if got, ok := commentAt(node, splitKey(path)); ok {
			t.Errorf("%s: a comment %q where none is", path, got)
		}
	}
}

// An entries kind reads the list at the key, each scalar entry with the
// comment on its line: an entry without one carries an empty comment; a key
// that holds no list, or a list with an entry that is no scalar, yields
// nothing.
func TestReadBackEntriesKind(t *testing.T) {
	const entriesSchema = `{
  "type": "object",
  "x-files": {"values": {"repository": "management-clusters", "path": "management-clusters/<name>/values.yaml"}},
  "properties": {
    "slack": {"type": "object", "properties": {
      "bots": {"type": "array", "default": [], "x-readback": {"file": "values", "key": "slack.bots", "kind": "entries"}}
    }}
  }
}`
	var s inputSchema
	if err := json.Unmarshal([]byte(entriesSchema), &s); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, values string
		want         any
	}{
		{"entries with and without a comment", "slack:\n  # Above the list.\n  bots:\n    - U1 # Alerting EU\n    - U2\n    - \"U3\"  #  Paging\n", []any{
			map[string]any{"value": "U1", "comment": "Alerting EU"}, map[string]any{"value": "U2", "comment": ""}, map[string]any{"value": "U3", "comment": " Paging"}}},
		{"an empty list", "slack:\n  bots: []\n", []any{}},
		{"no list", "slack:\n  bots: U1\n", nil},
		{"an entry that is no scalar", "slack:\n  bots:\n    - id: U1\n", nil},
		{"no key", "slack: {}\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := readBack(context.Background(), files(map[string]string{readBackValues: c.values}), readBackInstallation, "", readBackRegistry, &s)
			if err != nil {
				t.Fatal(err)
			}
			if v, ok := got["slack.bots"]; !reflect.DeepEqual(v, c.want) || ok != (c.want != nil) {
				t.Errorf("read back %#v (%v), want %#v", v, ok, c.want)
			}
		})
	}
}

// An installation kind resolves the host among the registry's
// installations: another installation's muster is that installation; a
// host that is no installation's, or not under the service label, yields
// nothing. The first of a list of keys on record answers: the current
// form over the deprecated one. A list without the selected entry yields
// nothing.
func TestReadBackInstallationAndKeyList(t *testing.T) {
	values := readBackValues
	read := files(map[string]string{values: "broker:\n  tokenUrl: https://muster.birch.acme.test/oauth/token\ngrafana:\n  domain: https://grafana.old.test\n  hosts:\n    - id: grafana-net\n      domain: https://grafana.acme.test\nresources:\n  - $include: shared.yaml#docs\n"})
	got, err := readBack(context.Background(), read, readBackInstallation, "", readBackRegistry, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{inputTunnelEnabled: false, inputBroker: readBackSibling, "portal.grafana": readBackGrafana}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
	for _, tokenURL := range []string{"https://muster.cedar.acme.test/oauth/token", "https://dex.birch.acme.test/oauth/token", "not a url"} {
		read = files(map[string]string{values: "broker:\n  tokenUrl: " + tokenURL + "\n"})
		got, err = readBack(context.Background(), read, readBackInstallation, "", readBackRegistry, readBackFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got[inputBroker]; ok {
			t.Errorf("read back %v from the token URL %s", got, tokenURL)
		}
	}
}

// Without the file on record nothing is read back but a present kind, which
// says the key is absent; the defaults stand.
func TestReadBackWithoutTheFile(t *testing.T) {
	got, err := readBack(context.Background(), files(nil), readBackInstallation, "", readBackRegistry, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("read back %v from no file", got)
	}
	read := files(map[string]string{readBackValues: "components: {}\napp:\n  baseUrl: not a url\n"})
	got, err = readBack(context.Background(), read, readBackInstallation, "", readBackRegistry, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{inputTunnelEnabled: false}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// A file the person cannot read is the error, not a default.
func TestReadBackForbidden(t *testing.T) {
	read := func(context.Context, string, string) (string, error) { return "", gh.ErrForbidden }
	if _, err := readBack(context.Background(), read, readBackInstallation, "", readBackRegistry, readBackFixture(t)); !errors.Is(err, gh.ErrForbidden) {
		t.Errorf("err %v", err)
	}
}

// A dotted key splits at its dots, a [key=value] selector kept whole
// whatever it holds.
func TestSplitKey(t *testing.T) {
	got := splitKey("gs.homepage.resources.[url=https://support.acme.test/].label")
	if want := []string{"gs", "homepage", "resources", "[url=https://support.acme.test/]", "label"}; !reflect.DeepEqual(got, want) {
		t.Errorf("split %v, want %v", got, want)
	}
}

// Unset names the person's choices no layer holds a value for: the
// customer-portal's, with the record's facts, a default (the title) and a
// read-back or typed value taken away.
func TestUnsetNamesThePersonChoicesWithoutAValue(t *testing.T) {
	def, _ := FindCapability(CustomerPortal)
	values, err := def.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	values["portal"].(map[string]any)["domain"] = readBackPortal
	values["plugins"] = map[string]any{"grafana": map[string]any{keyEnabled: true}}
	got, err := def.Unset(values)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"chart.line", inputSignIn, inputTokenBroker,
		"plugins.flux.enabled", "plugins.flux.gitRepositoryPatterns", "plugins.github.enabled", "plugins.sentry.enabled",
		"portal.friendlyAnnotations", "portal.friendlyLabels", "portal.organization", inputSupportURL, "portal.telemetrydeckAppId",
		inputTunnelEnabled,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unset %v, want %v", got, want)
	}
}

// Defaults is the document the schema's defaults make, installation aside.
func TestDefaults(t *testing.T) {
	got, err := (Capability{Name: AgentPlatform}).Defaults()
	if err != nil {
		t.Fatal(err)
	}
	// The holds and their reasons, by component: none by default.
	holds := map[string]any{"muster": "", "valkey": "", "kagent": "", "agent-manager": "", "klaus-gateway": "", "cluster-manager": "", "model-manager": ""}
	want := map[string]any{"modelServing": map[string]any{keyEnabled: false}, "aiChat": map[string]any{keyEnabled: false, "model": "claude-opus-5", keyProvider: "anthropic"},
		"scheduling": map[string]any{"singletonsCapacity": "any"}, keySkills: map[string]any{keyRepositories: []any{}},
		"hive": map[string]any{keyEnabled: false, "plans": map[string]any{keyRepositories: []any{}}, "magazine": map[string]any{"repository": ""},
			boardRoadmap: map[string]any{"board": boardRoadmap, "teams": []any{}}},
		"clusterManager": map[string]any{keyGitHub: map[string]any{keyEnabled: false}},
		"modelManager":   map[string]any{keyGitHub: map[string]any{keyEnabled: false}},
		"klausGateway":   map[string]any{"slack": map[string]any{"contextBotIDs": []any{}, "contextBotIDsComment": ""}},
		"agentManager":   map[string]any{keyGitHub: map[string]any{keyEnabled: false}, keySkills: map[string]any{keyRepositories: []any{}, "appSecretName": "", "gitAuthSecretName": "", "mintGitAuthSecret": false}},
		"versions":       map[string]any{"chart": "", "components": holds, "reasons": map[string]any{"chart": "", "components": holds}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("defaults %v, want %v", got, want)
	}
}

// A read-back naming several files answers from the first on record that
// holds the key: the fragment's document, addressed by its dotted key in
// brackets, before the values; the key present in the second alone is
// present; in neither, absent; with neither file on record, nothing.
func TestReadBackFromTheFirstFileHoldingTheKey(t *testing.T) {
	var s inputSchema
	if err := json.Unmarshal([]byte(readBackFilesSchema), &s); err != nil {
		t.Fatal(err)
	}
	const fragment = "acme/mcs:management-clusters/rowan/fragment.yaml"
	fragmentDoc := "apiVersion: v1\nkind: ConfigMap\ndata:\n  app-config.fragment.yaml: |\n    aiChat:\n      model: from-the-fragment\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  map[string]any
	}{
		{"both hold it: the fragment answers", map[string]string{fragment: fragmentDoc, readBackValues: "aiChat:\n  model: from-the-values\n"}, map[string]any{inputChatEnabled: true, inputChatModel: "from-the-fragment"}},
		{"the values alone hold it", map[string]string{fragment: "data:\n  app-config.fragment.yaml: |\n    other: {}\n", readBackValues: "aiChat:\n  model: from-the-values\n"}, map[string]any{inputChatEnabled: true, inputChatModel: "from-the-values"}},
		{"neither holds it", map[string]string{fragment: "data: {}\n", readBackValues: "other: {}\n"}, map[string]any{inputChatEnabled: false}},
		{"only the second file on record", map[string]string{readBackValues: "aiChat: {model: m}\n"}, map[string]any{inputChatEnabled: true, inputChatModel: "m"}},
		{"neither file on record", map[string]string{}, map[string]any{}},
	} {
		got, err := readBack(context.Background(), files(tc.files), readBackInstallation, "", nil, &s)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: read back %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The agent-platform definition reads the chat back from the Component's
// fragment on record — the YAML text under the ConfigMap key named after
// the fragment's file — else from the portal's own app-config, where a
// hand-kept portal carries it; the model with it. Without the chat in
// either, off.
func TestAgentPlatformReadsBackTheChat(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	const dir = "acme/mcs:management-clusters/rowan/extras/backstage/"
	fragment := "apiVersion: v1\nkind: ConfigMap\ndata:\n  app-config.agent-platform.yaml: |\n    agentPlatform:\n      kagent:\n        installations:\n          rowan: {}\n    aiChat:\n      model: claude-opus-5\n"
	appConfig := "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n    backstage:\n      appConfig: |\n        app:\n          title: Portal\n        aiChat:\n          anthropic:\n            apiKey: " + dollar + dollar + "{ANTHROPIC_API_KEY}\n          model: claude-opus-4-8\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  map[string]any
	}{
		{"the fragment first", map[string]string{dir + "agent-platform/app-config.yaml": fragment, dir + "backstage/app-config.yaml": appConfig}, map[string]any{inputAIChatEnabled: true, inputAIChatModel: "claude-opus-5", inputHiveEnabled: false}},
		{"a hand-kept portal's app-config", map[string]string{dir + "backstage/app-config.yaml": appConfig}, map[string]any{inputAIChatEnabled: true, inputAIChatModel: "claude-opus-4-8", inputHiveEnabled: false}},
		{"a chat on Vertex by hand", map[string]string{dir + "backstage/app-config.yaml": "data:\n  values: |\n    backstage:\n      appConfig: |\n        aiChat:\n          model: claude-sonnet-5\n          anthropic:\n            provider: vertex\n          google:\n            project: example-project\n            location: eu\n            keyFilename: /app/google/credentials.json\n"},
			map[string]any{inputAIChatEnabled: true, inputAIChatModel: "claude-sonnet-5", "aiChat.provider": "vertex", "aiChat.google.project": "example-project", "aiChat.google.location": "eu", inputHiveEnabled: false}},
		{"a fragment without the chat, no app-config", map[string]string{dir + "agent-platform/app-config.yaml": "data:\n  app-config.agent-platform.yaml: |\n    agentPlatform: {}\n"}, map[string]any{inputAIChatEnabled: false, inputHiveEnabled: false}},
		{"nothing on record", map[string]string{}, map[string]any{}},
	} {
		got, err := def.ReadBack(context.Background(), files(tc.files), readBackInstallation, readBackName, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: read back %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The agent-platform definition reads the Hive back from the plans block's
// presence, in the Component's fragment on record, else in the portal's own
// app-config, where the hub's hand-kept portal carries it; the repositories,
// the magazine and the board with it.
func TestAgentPlatformReadsBackTheHive(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	const dir = "acme/mcs:management-clusters/rowan/extras/backstage/"
	appConfig := "data:\n  values: |\n    backstage:\n      appConfig: |\n        plans:\n          repositories:\n            - acme/plans\n          magazine:\n            repository: acme/magazine\n            ref: data\n" +
		"          muster:\n            installation: rowan\n            server: github\n        roadmap:\n          board: roadmap\n          teams:\n            - Hive\n          muster:\n            installation: rowan\n            server: rowan-mcp-pro\n            toolPrefix: pro\n"
	fragment := "data:\n  app-config.agent-platform.yaml: |\n    plans:\n      repositories:\n        - acme/other-plans\n    roadmap:\n      board: customer\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  map[string]any
	}{
		{"a hand-kept portal's app-config", map[string]string{dir + "backstage/app-config.yaml": appConfig},
			map[string]any{inputAIChatEnabled: false, inputHiveEnabled: true, "hive.plans.repositories": []any{"acme/plans"}, "hive.magazine.repository": "acme/magazine", "hive.roadmap.board": boardRoadmap, "hive.roadmap.teams": []any{"Hive"}}},
		{"the fragment first", map[string]string{dir + "agent-platform/app-config.yaml": fragment, dir + "backstage/app-config.yaml": appConfig},
			map[string]any{inputAIChatEnabled: false, inputHiveEnabled: true, "hive.plans.repositories": []any{"acme/other-plans"}, "hive.magazine.repository": "acme/magazine", "hive.roadmap.board": "customer", "hive.roadmap.teams": []any{"Hive"}}},
		{"neither carries it", map[string]string{dir + "agent-platform/app-config.yaml": "data:\n  app-config.agent-platform.yaml: |\n    agentPlatform: {}\n"}, map[string]any{inputAIChatEnabled: false, inputHiveEnabled: false}},
	} {
		got, err := def.ReadBack(context.Background(), files(tc.files), readBackInstallation, readBackName, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: read back %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The agent-platform definition reads the portal files back at the portal's
// host: the installation's own where it hosts the portal, else the
// organisation's sibling the section is written into, so a sibling's plan
// reads what the host's plan rendered; a file under the installation's own
// name is not the section's. Where no portal hosts the section, the portal
// files are not on record and nothing is read back from them.
func TestAgentPlatformReadsBackThePortalFilesAtTheHost(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	fragment := "data:\n  app-config.agent-platform.yaml: |\n    agentPlatform:\n      skills:\n        repositories:\n          - acme/skills\n"
	own := "acme/mcs:management-clusters/" + readBackName + "/extras/backstage/agent-platform/app-config.yaml"
	sibling := "acme/mcs:management-clusters/" + readBackSibling + "/extras/backstage/agent-platform/app-config.yaml"
	skills := map[string]any{inputAIChatEnabled: false, inputHiveEnabled: false, "skills.repositories": []any{"acme/skills"}}
	for _, tc := range []struct {
		name  string
		host  string
		files map[string]string
		want  map[string]any
	}{
		{"its own portal", readBackName, map[string]string{own: fragment}, skills},
		{"the sibling's portal", readBackSibling, map[string]string{sibling: fragment}, skills},
		{"the sibling's portal, a file under its own name", readBackSibling, map[string]string{own: fragment}, map[string]any{}},
		{"no portal", "", map[string]string{own: fragment, sibling: fragment}, map[string]any{}},
	} {
		got, err := def.ReadBack(context.Background(), files(tc.files), readBackInstallation, tc.host, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: read back %v, want %v", tc.name, got, tc.want)
		}
	}
}

// dollar is the character the fleet's app-configs double in front of a
// variable the chart's environment supplies.
const dollar = "$"

// The agent-platform definition reads the serving choice back from the
// configmap patch on record.
func TestAgentPlatformReadsBackModelServing(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	read := files(map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): "components:\n  modelServing:\n    enabled: true\n"})
	got, err := def.ReadBack(context.Background(), read, readBackInstallation, readBackName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"modelServing.enabled": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// The agent-platform definition reads the singletons' placement back from
// the configmap patch on record.
func TestAgentPlatformReadsBackSingletonsCapacity(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	read := files(map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): "scheduling:\n  singletons:\n    nodeSelector:\n      karpenter.sh/capacity-type: on-demand\n"})
	got, err := def.ReadBack(context.Background(), read, readBackInstallation, readBackName, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"scheduling.singletonsCapacity": "on-demand"}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
}

// The agent-platform definition reads the cluster-manager's commit mode back
// from the configmap patch on record: on where the manager's values carry it,
// nothing where they do not, so the default stands.
func TestAgentPlatformReadsBackClusterManagerCommit(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	for _, tc := range []struct {
		name  string
		patch string
		want  map[string]any
	}{
		{"agent-manager boot Secret minted from the App", "agent-manager:\n  skills:\n    github:\n      app:\n        secretName: acme-skills-app\n    mintGitAuthSecret: true\n", map[string]any{"agentManager.skills.appSecretName": "acme-skills-app", "agentManager.skills.mintGitAuthSecret": true}}, //nolint:gosec // a Secret's name in a fixture, no credential
		{"commit mode on", "cluster-manager:\n  installation:\n    name: rowan\n  github:\n    enabled: true\n", map[string]any{"clusterManager.github.enabled": true}},
		{"commit mode not on record", "cluster-manager:\n  installation:\n    name: rowan\n", map[string]any{}},
		{"model-manager and agent-manager commit mode on", "agent-manager:\n  github:\n    enabled: true\nmodel-manager:\n  github:\n    enabled: true\n", map[string]any{"modelManager.github.enabled": true, "agentManager.github.enabled": true}},
		{"agent-manager skill catalog with private repositories", "agent-manager:\n  skills:\n    repositories:\n      - https://github.com/acme/skills\n    github:\n      app:\n        secretName: acme-skills-app\n    gitAuthSecretName: acme-skills-token\n", map[string]any{"agentManager.skills.repositories": []any{"https://github.com/acme/skills"}, "agentManager.skills.appSecretName": "acme-skills-app", "agentManager.skills.gitAuthSecretName": "acme-skills-token"}}, //nolint:gosec // a Secret's name in a fixture, no credential
	} {
		read := files(map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): tc.patch})
		got, err := def.ReadBack(context.Background(), read, readBackInstallation, readBackName, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: read back %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The customer-portal definition reads the portal's choices back from its
// tree on record: the app-config is YAML text in its ConfigMap, the chart
// line the value of the OCIRepository patch in the kustomization, the
// sign-in installation the provider's name without its prefix, the token
// broker the installation whose muster the token URL names, the support
// URL the resources entry with the support link's icon, the Grafana host
// the plugin's one host and the plugin wired or not by its proxy entry, the
// other plugins present or absent by their sections, the tunnel by its
// file.
func TestCustomerPortalReadsBackThePortal(t *testing.T) {
	def, _ := FindCapability(CustomerPortal)
	dir := "management-clusters/rowan/extras/backstage/backstage/"
	appConfig := "app:\n  title: ACME Portal\n  baseUrl: https://portal.rowan.acme.test\norganization:\n  name: ACME\ngrafana:\n  hosts:\n    - id: grafana-net\n      domain: https://grafana.acme.test\n" +
		"proxy:\n  endpoints:\n    /grafana/api:\n      target: https://grafana.acme.test/\n      headers:\n        Authorization: Bearer $${GRAFANA_TOKEN}\n" +
		"gs:\n  authProvider: oidc-birch\n  clusterTokenBroker:\n    clientId: $${AUTH_DEX_MUSTER_BROKER_CLIENT_ID}\n    tokenUrl: https://muster.birch.acme.test/oauth/token\n" +
		"  homepage:\n    resources:\n      - $include: shared-config.yaml#homepageResources.gsDocs\n      - label: Support\n        icon: LiveHelp\n        url: https://support.acme.test/\n  friendlyLabels:\n    - selector: team\n      key: Team\n"
	read := files(map[string]string{
		"acme/mcs:" + dir + "app-config.yaml":                 "apiVersion: v1\nkind: ConfigMap\ndata:\n  values: |\n    backstage:\n      appConfig: |\n" + indent(appConfig, "        "),
		"acme/mcs:" + dir + "kustomization.yaml":              "patches:\n  - patch: |\n      - op: remove\n        path: /spec/ref/tag\n      - op: add\n        path: /spec/ref/semver\n        value: '>=2.1.0 <3.0.0'\n    target: {kind: OCIRepository}\n",
		"acme/mcs:" + dir + "tunnelport-spiffe-bundle.yaml":   "apiVersion: v1\nkind: ServiceAccount\n---\napiVersion: v1\nkind: Secret\n",
		"acme/mcs:" + dir + "github-app-credentials.enc.yaml": "stringData:\n  values: ENC[AES256_GCM,data:abc,type:str]\n",
	})
	got, err := def.ReadBack(context.Background(), read, readBackInstallation, "", readBackRegistry)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"portal.domain": readBackPortal, "portal.title": "ACME Portal", "portal.organization": "ACME",
		inputSupportURL:         "https://support.acme.test/",
		"portal.friendlyLabels": []any{map[string]any{"selector": "team", "key": "Team"}},
		inputSignIn:             readBackSibling, inputTokenBroker: readBackSibling,
		"chart.line":              ">=2.1.0 <3.0.0",
		"plugins.github.enabled":  false,
		"plugins.grafana.enabled": true, inputGrafanaDomain: readBackGrafana,
		"plugins.flux.enabled":   false,
		"plugins.sentry.enabled": false,
		inputTunnelEnabled:       true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}

	// Without the tree: the tunnel is the one answer, its file not on record.
	got, err = def.ReadBack(context.Background(), files(nil), readBackInstallation, "", readBackRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{inputTunnelEnabled: false}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v from no tree, want %v", got, want)
	}

	// A provider not of the oidc-<installation> form reads back nothing; a
	// portal that brokers on its own muster reads back its own
	// installation; the deprecated grafana.domain still answers.
	read = files(map[string]string{"acme/mcs:" + dir + "app-config.yaml": "data:\n  values: |\n    backstage:\n      appConfig: |\n        gs:\n          authProvider: github\n          clusterTokenBroker:\n            tokenUrl: https://muster.rowan.acme.test/oauth/token\n        grafana:\n          domain: https://grafana.acme.test\n"})
	got, err = def.ReadBack(context.Background(), read, readBackInstallation, "", readBackRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[inputSignIn]; ok {
		t.Errorf("read back %v from a provider without the prefix", got)
	}
	if got[inputTokenBroker] != readBackName || got[inputGrafanaDomain] != readBackGrafana {
		t.Errorf("read back %v, want the own broker and the deprecated grafana domain", got)
	}
}

// The agent-platform definition reads the version holds back from the record:
// a component's versionRange from the configmap patch, the meta chart's from
// the OCIRepository patch of the extras kustomization, found by its target
// whatever its place among the patches — and each hold's reason, the comment
// a person wrote above it, line by line; the generic comment the definition
// writes on a hold without one reads back as no reason.
// The agent-platform definition reads the context bots back from the
// configmap patch on record, each entry with its comment, and the comment a
// person wrote above the list; the generic comment reads back as none.
func TestAgentPlatformReadsBackContextBots(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	for _, c := range []struct {
		name, comment string
		want          map[string]any
	}{
		{"a person's comment", "    # The alerting bots of the alert channel.\n", map[string]any{"klausGateway.slack.contextBotIDsComment": "The alerting bots of the alert channel."}},
		{"the generic comment", "    # The bots whose posts reach an agent's thread context: the input\n    # klausGateway.slack.contextBotIDs, read back so a reconcile keeps them.\n", map[string]any{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			patch := "klausGateway:\n  slack:\n    enabled: true\n" + c.comment + "    contextBotIDs:\n      - U0000000001 # Alerting EU\n      - U0000000002\n"
			got, err := def.ReadBack(context.Background(), files(map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): patch}), readBackInstallation, readBackName, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"klausGateway.slack.contextBotIDs": []any{
				map[string]any{"value": "U0000000001", "comment": "Alerting EU"}, map[string]any{"value": "U0000000002", "comment": ""}}}
			for k, v := range c.want {
				want[k] = v
			}
			for k := range got {
				if !strings.HasPrefix(k, "klausGateway.") {
					delete(got, k)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("read back %v, want %v", got, want)
			}
		})
	}
}

func TestAgentPlatformReadsBackVersionHolds(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	patch := "components:\n  agent-manager:\n    enabled: true\n    # Held on the running release.\n    versionRange: \"1.9.2\"\n  klaus-gateway:\n    enabled: true\n    # Held by the input versions.components.klaus-gateway: a reconcile keeps it; --input versions.components.klaus-gateway=<version> moves it, an empty value lifts it.\n    versionRange: \"4.0.0\"\n"
	const heldPatch = "      # Held on the running release until the next line lands here as one window\n      # (acme/platform#12); back to the range in that PR.\n      - op: replace\n        path: /spec/ref/semver\n        value: \"4.114.1\"\n"
	kustomization := "patches:\n  - patch: |-\n      apiVersion: v1\n      kind: Secret\n    target:\n      kind: Secret\n      name: muster-credentials-revision\n" +
		"  - patch: |-\n" + heldPatch + "    target:\n      kind: OCIRepository\n      name: agent-platform\n"
	read := files(map[string]string{
		"acme/configs:" + def.EnabledMarker("rowan"):                                  patch,
		"acme/mcs:management-clusters/rowan/extras/agent-platform/kustomization.yaml": kustomization,
	})
	got, err := def.ReadBack(context.Background(), read, readBackInstallation, readBackName, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"versions.chart": "4.114.1", "versions.components.agent-manager": "1.9.2", "versions.components.klaus-gateway": "4.0.0",
		"versions.reasons.chart":                    "Held on the running release until the next line lands here as one window\n(acme/platform#12); back to the range in that PR.",
		"versions.reasons.components.agent-manager": "Held on the running release."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}

	// The chart line's own range, release candidates or not, is no hold.
	for _, line := range []string{">=4.0.0 <5.0.0", ">=4.0.0-0 <5.0.0-0"} {
		linePatch := "      - op: replace\n        path: /spec/ref/semver\n        value: \"" + line + "\"\n"
		read = files(map[string]string{"acme/mcs:management-clusters/rowan/extras/agent-platform/kustomization.yaml": strings.ReplaceAll(kustomization, heldPatch, linePatch)})
		got, err = def.ReadBack(context.Background(), read, readBackInstallation, readBackName, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("read back %v from the line's range %s, want nothing", got, line)
		}
	}
}
