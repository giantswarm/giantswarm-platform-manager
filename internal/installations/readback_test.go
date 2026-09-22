package installations

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

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
      "enabled": {"type": "boolean", "default": false, "x-readback": {"file": "values", "key": "components.serving.enabled"}}
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

// The inputs the tests read back or find unset, by dotted input key.
const (
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
// the service label is stripped (the installation read for here); a
// [key=value] step selects a list's entry, a list of keys answers from the
// first on record (the deprecated form here, the current one absent); a key
// or file not on record yields nothing.
func TestReadBackKinds(t *testing.T) {
	read := files(map[string]string{
		readBackValues: "components:\n  serving:\n    enabled: true\ntunnel:\n  port: 8443\napp:\n  baseUrl: https://portal.rowan.acme.test/\n" +
			"broker:\n  tokenUrl: https://muster.rowan.acme.test/oauth/token\nresources:\n  - $include: shared.yaml#docs\n  - label: Support\n    icon: LiveHelp\n    url: https://support.acme.test/\ngrafana:\n  domain: https://grafana.acme.test\n",
	})
	got, err := readBack(context.Background(), read, readBackInstallation, readBackRegistry, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"serving.enabled": true, inputTunnelEnabled: true, "portal.domain": readBackPortal,
		inputSupportURL: "https://support.acme.test/", "portal.grafana": readBackGrafana, inputBroker: readBackName,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
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
	got, err := readBack(context.Background(), read, readBackInstallation, readBackRegistry, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{inputTunnelEnabled: false, inputBroker: readBackSibling, "portal.grafana": readBackGrafana}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
	}
	for _, tokenURL := range []string{"https://muster.cedar.acme.test/oauth/token", "https://dex.birch.acme.test/oauth/token", "not a url"} {
		read = files(map[string]string{values: "broker:\n  tokenUrl: " + tokenURL + "\n"})
		got, err = readBack(context.Background(), read, readBackInstallation, readBackRegistry, readBackFixture(t))
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
	got, err := readBack(context.Background(), files(nil), readBackInstallation, readBackRegistry, readBackFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("read back %v from no file", got)
	}
	read := files(map[string]string{readBackValues: "components: {}\napp:\n  baseUrl: not a url\n"})
	got, err = readBack(context.Background(), read, readBackInstallation, readBackRegistry, readBackFixture(t))
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
	if _, err := readBack(context.Background(), read, readBackInstallation, readBackRegistry, readBackFixture(t)); !errors.Is(err, gh.ErrForbidden) {
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
	values["plugins"] = map[string]any{"grafana": map[string]any{"enabled": true}}
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
	if want := map[string]any{"modelServing": map[string]any{"enabled": false}}; !reflect.DeepEqual(got, want) {
		t.Errorf("defaults %v, want %v", got, want)
	}
}

// The agent-platform definition reads the serving choice back from the
// configmap patch on record.
func TestAgentPlatformReadsBackModelServing(t *testing.T) {
	def, _ := FindCapability(AgentPlatform)
	read := files(map[string]string{"acme/configs:" + def.EnabledMarker("rowan"): "components:\n  modelServing:\n    enabled: true\n"})
	got, err := def.ReadBack(context.Background(), read, readBackInstallation, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"modelServing.enabled": true}; !reflect.DeepEqual(got, want) {
		t.Errorf("read back %v, want %v", got, want)
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
	got, err := def.ReadBack(context.Background(), read, readBackInstallation, readBackRegistry)
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
	got, err = def.ReadBack(context.Background(), files(nil), readBackInstallation, readBackRegistry)
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
	got, err = def.ReadBack(context.Background(), read, readBackInstallation, readBackRegistry)
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
