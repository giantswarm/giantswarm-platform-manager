package verify

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The patches of an invented installation the planned keys are tried on.
const (
	testPlatformPatch = "installations/x/apps/agent-platform/configmap-values.yaml.patch"
	testDexPatch      = "installations/x/apps/dex-app/configmap-values.yaml.patch"
	// testDexSecretPatch is the dex-app's secret patch, testComponentAppConfig
	// the platform Component's app-config fragment in a portal's tree.
	testDexSecretPatch     = "installations/x/apps/dex-app/secret-values.yaml.patch"
	testComponentAppConfig = "management-clusters/x/extras/backstage/agent-platform/app-config.yaml"
	testDomain             = "x.example.test"
)

// Every key of a capability's migrations names a path some golden render of
// its definition emits — a leaf, or an entry of a scalar the plan merges as
// a set — a key nothing renders any more is stale and fails here, not on the
// fleet by marking nothing.
func TestEveryMigrationKeyIsRendered(t *testing.T) {
	for capability, pkg := range map[string]string{installations.AgentPlatform: "agentplatform", installations.CustomerPortal: "customerportal"} {
		t.Run(capability, func(t *testing.T) {
			ms, err := definitions.Migrations(capability)
			if err != nil {
				t.Fatal(err)
			}
			migs := readMigrations(ms, nil)
			if len(migs) != len(ms) {
				t.Fatalf("%d of %d migration keys name a file kind the comparison observes", len(migs), len(ms))
			}
			covered := make([]bool, len(migs))
			shapes, err := filepath.Glob(filepath.Join("..", "..", "render", pkg, "testdata", "*", "golden"))
			if err != nil || len(shapes) == 0 {
				t.Fatalf("golden shapes: %v (%d)", err, len(shapes))
			}
			for _, golden := range shapes {
				fsys := os.DirFS(golden)
				err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return err
					}
					content, err := fs.ReadFile(fsys, p)
					if err != nil {
						return err
					}
					fd := &fileDiff{path: p, kind: kindOf(p), documents: flattenLines(string(content)).documents}
					for i, k := range migs {
						if covered[i] {
							continue
						}
						for leaf, value := range flattenYAML(string(content)) {
							if slices.ContainsFunc(leafPaths(p, leaf, value), func(lp string) bool { return k.covers(fd, lp) }) {
								covered[i] = true
								break
							}
						}
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			for i, ok := range covered {
				if !ok {
					t.Errorf("migrations.yaml: %q (%s) names nothing the goldens render", ms[i].Key, ms[i].Reason)
				}
			}
		})
	}
}

// The additions the fleet lacks are planned under the definitions' own
// migrations: an installation enabled by hand before the definition rendered
// them reads the hub's token-exchange client and trusted peer in its Dex
// (M32, the hub named), the portal's Dex client Secret, user secrets and
// plugin keys with their kustomization entries and the type every rendered
// Secret states (M33) and the platform's
// Component in the portal's kustomization (M3) as planned with their reasons;
// what stands beside them — another client, the portal's own files, another
// Component — is no migration's, and the removals come first as everywhere.
func TestFleetAdditionsArePlanned(t *testing.T) {
	const m3, m5, m30, m32, m33 = "M3", "M5", "M30", "M32", "M33"
	keys := func(capability string) (plannedKeys, plannedKeys) {
		rs, err := definitions.Removals(capability)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := definitions.Migrations(capability)
		if err != nil {
			t.Fatal(err)
		}
		return readRemovals(rs, facts{factDomain: testDomain}), readMigrations(ms, facts{factDomain: testDomain})
	}
	platformRms, platformMigs := keys(installations.AgentPlatform)
	portalRms, portalMigs := keys(installations.CustomerPortal)
	dexCM := &fileDiff{path: testDexPatch, kind: definitions.KindDexConfigMap}
	portalFile := func(p string) *fileDiff {
		return &fileDiff{path: "management-clusters/x/extras/backstage/" + p, kind: definitions.KindBackstage}
	}
	absent := func(p string) *Difference { return &Difference{Path: p, Rendered: "x", absent: true} }
	for _, tc := range []struct {
		name      string
		rms, migs plannedKeys
		fd        *fileDiff
		d         *Difference
		says, tag string // the reason names says and carries the tag; a tag "" is no planned change
	}{
		{"the hub's client in a spoke's Dex", platformRms, platformMigs, dexCM, absent("oidc.extraStaticClients[muster-token-exchange-x].id"), "the client muster-token-exchange-x, a hub's token-exchange client", m32},
		{"the client's secret reference is the client's", platformRms, platformMigs, dexCM, absent("oidc.extraStaticClients[muster-token-exchange-x].secretRef.name"), "the client muster-token-exchange-x", m32},
		{"the hub's client as a trusted peer", platformRms, platformMigs, dexCM, absent("oidc.staticClients.dexK8SAuthenticator.trustedPeers[muster-token-exchange-x]"), "trusts the hub's token-exchange client muster-token-exchange-x", m32},
		{"a further hub's client carries the hub's name", platformRms, platformMigs, dexCM, absent("oidc.extraStaticClients[muster-token-exchange-x-gazelle].id"), "the client muster-token-exchange-x-gazelle", m32},
		{"the former id is nobody's migration", platformRms, platformMigs, dexCM, absent("oidc.extraStaticClients[gazelle-token-exchange].id"), "", ""},
		{"another client is its own migration's", platformRms, platformMigs, dexCM, absent("oidc.extraStaticClients[kagent].id"), "a client named kagent", m5},
		{"another peer is its own migration's", platformRms, platformMigs, dexCM, absent("oidc.staticClients.dexK8SAuthenticator.trustedPeers[backstage]"), "trusts the backstage client", m30},
		{"a client beside them", platformRms, platformMigs, dexCM, absent("oidc.extraStaticClients[other].id"), "", ""},
		{"the portal's Dex client Secret", portalRms, portalMigs, portalFile("backstage/dex-client-backstage-secret.enc.yaml"), absent(""), "the Secret dex-client-backstage", m33},
		{"a leaf of the user secrets", portalRms, portalMigs, portalFile("backstage/user-secrets.enc.yaml"), absent("stringData.values"), "Secret user-secrets-backstage", m33},
		{"a leaf of the plugin keys", portalRms, portalMigs, portalFile("backstage/plugin-keys-secret.enc.yaml"), absent("apiVersion"), "Secret plugin-keys-backstage", m33},
		{"the Dex client Secret's kustomization entry", portalRms, portalMigs, portalFile("backstage/kustomization.yaml"), absent("resources[dex-client-backstage-secret.enc.yaml]"), "lists the Secret dex-client-backstage", m33},
		{"the user secrets' kustomization entry", portalRms, portalMigs, portalFile("backstage/kustomization.yaml"), absent("resources[user-secrets.enc.yaml]"), "lists the Secret user-secrets-backstage", m33},
		{"the plugin keys' kustomization entry", portalRms, portalMigs, portalFile("backstage/kustomization.yaml"), absent("resources[plugin-keys-secret.enc.yaml]"), "lists the Secret plugin-keys-backstage", m33},
		{"the portal's own entry beside them", portalRms, portalMigs, portalFile("backstage/kustomization.yaml"), absent("resources[app-config.yaml]"), "", ""},
		{"a Secret on record without its type", portalRms, portalMigs, portalFile("backstage/user-secrets.enc.yaml"), absent("type"), "states its type (Opaque)", m33},
		{"the GitHub App's Secret states its type too", portalRms, portalMigs, portalFile("backstage/github-app-credentials.enc.yaml"), absent("type"), "states its type (Opaque)", m33},
		{"the GitHub App's Secret itself is no migration", portalRms, portalMigs, portalFile("backstage/github-app-credentials.enc.yaml"), absent(""), "", ""},
		{"the portal's own file", portalRms, portalMigs, portalFile("backstage/app-config.yaml"), absent(""), "", ""},
		{"the platform's Component in the portal's kustomization", portalRms, portalMigs, portalFile("kustomization.yaml"), absent("components[./agent-platform/]"), "the agent-platform definition's Component ./agent-platform/", m3},
		{"another Component", portalRms, portalMigs, portalFile("kustomization.yaml"), absent("components[./other/]"), "", ""},
		{"a leaf on record with another value", portalRms, portalMigs, portalFile("backstage/user-secrets.enc.yaml"), &Difference{Path: "metadata.namespace", Rendered: "x", Current: "y"}, "", ""},
	} {
		got := planned(tc.fd, tc.d, tc.rms, tc.migs)
		switch {
		case tc.tag == "" && got != "":
			t.Errorf("%s: %s#%s planned %q, want none", tc.name, tc.fd.path, tc.d.Path, got)
		case tc.tag != "" && (!strings.Contains(got, tc.says) || !strings.HasSuffix(got, "· "+tc.tag)):
			t.Errorf("%s: %s#%s planned %q, want it to say %q and carry %s", tc.name, tc.fd.path, tc.d.Path, got, tc.says, tc.tag)
		}
	}
}

// leafPaths are the paths a rendered leaf is named by: its own and, for a
// scalar the plan merges as a comma-separated set, each entry's.
func leafPaths(filePath, yamlPath, value string) []string {
	out := []string{yamlPath}
	if plan.JoinedList(filePath, yamlPath) {
		for _, id := range plan.SplitJoined(value) {
			out = append(out, entryPath(yamlPath, id))
		}
	}
	return out
}

// A scalar the plan merges as a comma-separated set — the kagent UI's
// oidc-extra-audience — is planned when the record's entries are all
// rendered and every entry the render adds is named, as an entry of the
// scalar, by a migration key: the reasons joined, each once. An added entry
// no key names keeps the difference (by input or drift); a removed entry
// keeps it as well; the scalar the record lacks is the scalar's own key's.
// A file the migrations name as a whole — the mcp-* Valkey Secret — is
// planned at every leaf, the kustomization entry and the oauth Secret's
// password with it.
func TestJoinedScalarsAndTheValkeySecretArePlanned(t *testing.T) {
	const m5, m5auth, m9, m30 = "M5 — kagent", "M5 — authenticator", "M9 — valkey", "M30 — backstage"
	const list = plan.ListExtraAudience
	migs := readMigrations([]definitions.Migration{
		{Key: "configmap:" + list + "[kagent]", Reason: m5},
		{Key: "configmap:" + list, Reason: m5},
		{Key: "configmap:" + list + "[dex-k8s-authenticator]", Reason: m5auth},
		{Key: "configmap:" + list + "[backstage]", Reason: m30},
		{Key: "extras:mcp-<name>/valkey-credentials.enc.yaml", Reason: m9},
		{Key: "extras:mcp-<name>/kustomization.yaml resources[valkey-credentials.enc.yaml]", Reason: m9},
		{Key: "extras:mcp-<name>/oauth-credentials.enc.yaml stringData.VALKEY_PASSWORD", Reason: m9},
		{Key: "extras:mcp-<name>/oauth-credentials.enc.yaml type", Reason: m9},
	}, nil)
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	dexPatch := &fileDiff{path: testDexPatch, kind: definitions.KindDexConfigMap}
	valkey := &fileDiff{path: "management-clusters/x/extras/mcp-capi/valkey-credentials.enc.yaml", kind: definitions.KindExtras}
	kust := &fileDiff{path: "management-clusters/x/extras/mcp-capi/kustomization.yaml", kind: definitions.KindExtras}
	oauth := &fileDiff{path: "management-clusters/x/extras/mcp-capi/oauth-credentials.enc.yaml", kind: definitions.KindExtras}
	scalar := func(rendered, current string) *Difference {
		return &Difference{Path: list, Rendered: rendered, Current: current, Input: "portal"}
	}
	absent := func(p string) *Difference { return &Difference{Path: p, Rendered: Redacted, absent: true} }
	for _, tc := range []struct {
		name string
		fd   *fileDiff
		d    *Difference
		want string
	}{
		{"only planned additions, the reasons joined once each", patch, scalar("dex-k8s-authenticator,kagent,portal,backstage", "portal"), m5auth + "; " + m5 + "; " + m30},
		{"one planned addition", patch, scalar("kagent,portal", "portal"), m5},
		{"the record's own entries stay", patch, scalar("kagent,portal,own", "portal,own"), m5},
		{"an addition no key names", patch, scalar("kagent,portal,other", "portal"), ""},
		{"a removed entry", patch, scalar("kagent,portal", "portal,gone"), ""},
		{"the same set in another order", patch, scalar("kagent,portal", "portal,kagent"), ""},
		{"the scalar's key does not name an entry", patch, scalar("kagent,portal,other", "kagent,portal"), ""},
		{"the scalar the record lacks", patch, &Difference{Path: list, Rendered: "kagent,portal,other", absent: true}, m5},
		{"a scalar of another file", dexPatch, scalar("kagent,portal", "portal"), ""},
		{"the Valkey Secret created", valkey, absent(""), m9},
		{"every leaf of the Valkey Secret", valkey, absent("type"), m9},
		{"its password", valkey, absent("stringData.default"), m9},
		{"its kustomization entry", kust, absent("resources[valkey-credentials.enc.yaml]"), m9},
		{"the oauth Secret's password", oauth, absent("stringData.VALKEY_PASSWORD"), m9},
		{"the oauth Secret's type, added with the password", oauth, &Difference{Path: "type", Rendered: "Opaque", absent: true}, m9},
		{"the oauth Secret's other leaves", oauth, absent("stringData.DEX_CLIENT_SECRET"), ""},
	} {
		if got := planned(tc.fd, tc.d, nil, migs); got != tc.want {
			t.Errorf("%s: %s#%s planned %q, want %q", tc.name, tc.fd.path, tc.d.Path, got, tc.want)
		}
	}
}

// The installation's own three MCP servers on record (mcp-kubernetes,
// mcp-prometheus, mcp-capi) repeat what the shared defaults register — in
// their in-cluster form, or on the installation's own base domain: their
// removal names the server. Any other entry of the list, and the list
// itself, is no removal's — a registered server's home is its MCPServer
// object under extras, a hub's target entries are rendered — so a foreign
// server, a private target's tunnel host and a target's public URL a hub
// still carries by hand are drift. Without a base domain on record no public
// entry is the installation's own.
func TestOwnMCPServersAreTheOnlyPlannedEntries(t *testing.T) {
	rs, err := definitions.Removals(installations.AgentPlatform)
	if err != nil {
		t.Fatal(err)
	}
	rms, noDomain := readRemovals(rs, facts{factDomain: testDomain}), readRemovals(rs, nil)
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	const list = "agent-platform-mcps.mcpServers"
	for _, tc := range []struct {
		name, path, names string
		keys              plannedKeys
	}{
		{"own mcp-kubernetes on the installation's domain", list + "[https://mcp-kubernetes." + testDomain + "/mcp].url", "this mcp-kubernetes entry at mcp-kubernetes." + testDomain, rms},
		{"own mcp-prometheus in the cluster", list + "[http://mcp-prometheus.mcp-prometheus.svc:8080/mcp].timeout", "this mcp-prometheus entry repeats the in-cluster one", rms},
		{"own mcp-capi's auth mode", list + "[https://mcp-capi." + testDomain + "/mcp].auth.mode", "this mcp-capi entry at mcp-capi." + testDomain, rms},
		{"a target's public URL on a hub", list + "[https://mcp-kubernetes.y.example.test/mcp].url", "", rms},
		{"a foreign server", list + "[http://mcp-foo.mcp-foo.svc:8080/mcp].url", "", rms},
		{"a private target's tunnel host", list + "[https://mcp-kubernetes-y.agent-platform.svc.cluster.local:8443/mcp].url", "", rms},
		{"the list itself", list, "", rms},
		{"the keep switch", "agent-platform-mcps.defaults.keep", "", rms},
		{"no base domain on record: a public entry is not own", list + "[https://mcp-kubernetes." + testDomain + "/mcp].url", "", noDomain},
		{"no base domain on record: the in-cluster entry still is", list + "[http://mcp-capi.mcp-capi.svc:8080/mcp].url", "this mcp-capi entry repeats the in-cluster one", noDomain},
	} {
		got := tc.keys.reason(patch, tc.path)
		switch {
		case tc.names == "" && got != "":
			t.Errorf("%s: %s is planned %q, want drift", tc.name, tc.path, got)
		case tc.names != "" && !strings.Contains(got, tc.names):
			t.Errorf("%s: %s planned %q, want it to say %q", tc.name, tc.path, got, tc.names)
		}
	}
}

// A portal that lists its extensions one by one — the hub's Dev Portal,
// graveler's — has the list replaced by the shared include: the include is
// the definition's planned change and every entry the record lists beside
// it is the same change, never drift beside it. The other includes the
// definition renders are not named by the row.
func TestPortalInlineExtensionsArePlanned(t *testing.T) {
	rs, err := definitions.Removals(installations.CustomerPortal)
	if err != nil {
		t.Fatal(err)
	}
	rms := readRemovals(rs, facts{factDomain: testDomain})
	appConfig := &fileDiff{path: "management-clusters/y/extras/backstage/backstage/app-config.yaml", kind: definitions.KindBackstage, documents: appConfigDocuments}
	const doc = "data.values:backstage.appConfig:"
	include := rms.reason(appConfig, doc+"app.extensions.$include")
	if !strings.HasPrefix(include, "Changed: app.extensions includes") {
		t.Fatalf("the include reads %q", include)
	}
	for _, path := range []string{"app.extensions[0]", "app.extensions[3]", "app.extensions[14].entity-card:catalog/labels", "app.extensions[29].page:scaffolder.config.title"} {
		if got := rms.reason(appConfig, doc+path); !strings.HasPrefix(got, "Changed: the extensions are listed one by one") {
			t.Errorf("%s reads %q, want the inline entries' planned change", path, got)
		}
	}
	for _, path := range []string{"app.routes.$include", "app.extensionsX", "gs.adminGroups.$include"} {
		if got := rms.reason(appConfig, doc+path); got != "" {
			t.Errorf("%s reads %q, want no planned change", path, got)
		}
	}
}

// A key's placeholders are filled into its reason from the path they
// matched: the mcp-* server's name from its extras directory, a Dex
// client's from the map key or the Secret's file name, a ReferenceGrant's
// namespace from the object's identity, a fact's placeholder with the fact.
// A placeholder the key declares twice stands for one value, so a Secret
// of one server in another's directory is nobody's; a <x> the key does not
// declare stays as it is.
func TestPlaceholdersFillTheReason(t *testing.T) {
	migs := readMigrations([]definitions.Migration{
		{Key: "extras:mcp-<name>/valkey-credentials.enc.yaml", Reason: "the mcp-<name> server's Valkey"},
		{Key: "extras:mcp-<name>/kustomization.yaml resources[dex-client-mcp-<name>-secret.yaml]", Reason: "the mcp-<name> server's Dex client Secret"},
		{Key: "extras:agent-platform/secrets/dex-client-<name>-secret.yaml", Reason: "the <name> Dex client's secret"},
		{Key: "dex-configmap:oidc.staticClients.<name>.clientSecretRef", Reason: "the Dex client <name>"},
		{Key: "configmap:extraObjects[ReferenceGrant/<namespace>/agentgateway-jwks-dex]", Reason: "a ReferenceGrant in <namespace>"},
		{Key: "configmap:muster.muster.oauth.server.tokenExchangeBroker.targets.<name>.clientCredentialsSecretRef", Reason: "the target <name> (<other> stays)"},
	}, nil)
	rms := readRemovals([]definitions.Removal{
		{Key: "configmap:agent-platform-mcps.mcpServers[<scheme>://mcp-capi.<domain>/mcp]", Reason: "mcp-capi at mcp-capi.<domain> over <scheme>"},
	}, facts{factDomain: testDomain})
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	dexCM := &fileDiff{path: testDexPatch, kind: definitions.KindDexConfigMap}
	valkey := &fileDiff{path: "management-clusters/x/extras/mcp-prometheus/valkey-credentials.enc.yaml", kind: definitions.KindExtras}
	mcpKust := &fileDiff{path: "management-clusters/x/extras/mcp-prometheus/kustomization.yaml", kind: definitions.KindExtras}
	secret := &fileDiff{path: "management-clusters/x/extras/agent-platform/secrets/dex-client-muster-secret.yaml", kind: definitions.KindExtras}
	absent := func(p string) *Difference { return &Difference{Path: p, Rendered: "x", absent: true} }
	for _, tc := range []struct {
		name string
		fd   *fileDiff
		d    *Difference
		want string
	}{
		{"the server from its directory", valkey, absent("stringData.default"), "the mcp-prometheus server's Valkey"},
		{"the server from the directory and the entry", mcpKust, absent("resources[dex-client-mcp-prometheus-secret.yaml]"), "the mcp-prometheus server's Dex client Secret"},
		{"another server's Secret in the directory is nobody's", mcpKust, absent("resources[dex-client-mcp-kubernetes-secret.yaml]"), ""},
		{"the client from the file name", secret, absent(""), "the muster Dex client's secret"},
		{"the client from the map key", dexCM, absent("oidc.staticClients.mcpCapi.clientSecretRef.name"), "the Dex client mcpCapi"},
		{"the namespace from the identity", patch, absent("extraObjects[ReferenceGrant/dex/agentgateway-jwks-dex].kind"), "a ReferenceGrant in dex"},
		{"an undeclared placeholder stays", patch, absent("muster.muster.oauth.server.tokenExchangeBroker.targets.y.clientCredentialsSecretRef"), "the target y (<other> stays)"},
		{"a fact and a part of the identity", patch, &Difference{Path: "agent-platform-mcps.mcpServers[https://mcp-capi." + testDomain + "/mcp].url", Current: "x"}, "mcp-capi at mcp-capi." + testDomain + " over https"},
	} {
		if got := planned(tc.fd, tc.d, rms, migs); got != tc.want {
			t.Errorf("%s: %s#%s planned %q, want %q", tc.name, tc.fd.path, tc.d.Path, got, tc.want)
		}
	}
}

// Every placeholder a reason references is one its key declares, so the
// comparison fills it: a <x> left in a reason would reach the Dev Portal as
// it is. The keys are read with a base domain, so one with <domain> is
// parsed too.
func TestEveryReasonPlaceholderIsDeclared(t *testing.T) {
	ref := regexp.MustCompile(`<([^<>\s]+)>`)
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range caps {
		rs, err := definitions.Removals(capability)
		if err != nil {
			t.Fatal(err)
		}
		ms, err := definitions.Migrations(capability)
		if err != nil {
			t.Fatal(err)
		}
		keys := map[string]string{}
		for _, r := range rs {
			keys[r.Key] = r.Reason
		}
		for _, m := range ms {
			keys[m.Key] = m.Reason
		}
		for key, reason := range keys {
			k, ok := parseKey(key, reason, facts{factDomain: testDomain})
			if !ok {
				continue
			}
			for _, m := range ref.FindAllStringSubmatch(reason, -1) {
				if !slices.Contains(k.names, m[1]) {
					t.Errorf("%s: %q references <%s>, which its key %q does not declare", capability, reason, m[1], key)
				}
			}
		}
	}
}

// The Vertex chat's leftovers a portal on record still carries — the
// google-credentials.enc.yaml entry of its kustomization's resources and
// the file itself — are the customer-portal definition's removals, planned
// as a move to the Agent Platform Component, never drift. A kustomization
// entry is a scalar the comparison keys by its value (resources[<name>]),
// and the key names it that way; an entry beside it is nobody's.
func TestPortalVertexLeftoversArePlanned(t *testing.T) {
	rs, err := definitions.Removals(installations.CustomerPortal)
	if err != nil {
		t.Fatal(err)
	}
	rms := readRemovals(rs, facts{factDomain: testDomain})
	const dir = "management-clusters/x/extras/backstage/backstage/"
	kust := &fileDiff{path: dir + "kustomization.yaml", kind: definitions.KindBackstage}
	file := &fileDiff{path: dir + "google-credentials.enc.yaml", kind: definitions.KindBackstage}
	for _, tc := range []struct {
		name string
		fd   *fileDiff
		d    *Difference
		says string // the reason opens with says; "" is no planned change
	}{
		{"the file's kustomization entry", kust, &Difference{Path: "resources[google-credentials.enc.yaml]", Current: "google-credentials.enc.yaml"}, "Moved: the google-credentials.enc.yaml resource entry"},
		{"the file", file, &Difference{Path: "stringData.values", Current: Redacted}, "Moved: google-credentials.enc.yaml"},
		{"an entry beside it", kust, &Difference{Path: "resources[other.enc.yaml]", Current: "other.enc.yaml"}, ""},
	} {
		got := planned(tc.fd, tc.d, rms, nil)
		if !strings.HasPrefix(got, tc.says) || tc.says == "" && got != "" {
			t.Errorf("%s: %s#%s planned %q, want %q", tc.name, tc.fd.path, tc.d.Path, got, tc.says)
		}
	}
}

// Every removal key of every capability parses — its prefix names a file
// kind the comparison observes — and neither the file nor a segment it
// names carries a space: the parser splits a key's path on dots and
// brackets alone, so a key with a space in it is read as segments no path
// has, names nothing, and the leaf it meant to plan reads as drift on the
// fleet. A migration key is held to the goldens by
// TestEveryMigrationKeyIsRendered.
func TestEveryRemovalKeyNamesAPath(t *testing.T) {
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	for _, capability := range caps {
		rs, err := definitions.Removals(capability)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rs {
			k, ok := parseKey(r.Key, r.Reason, facts{factDomain: testDomain})
			if !ok {
				t.Errorf("%s: removal %q names no file kind the comparison observes", capability, r.Key)
				continue
			}
			for _, re := range append([]*regexp.Regexp{k.file}, k.path...) {
				if strings.Contains(re.String(), " ") {
					t.Errorf("%s: removal %q carries a space in %s: a segment no path has", capability, r.Key, re)
				}
			}
		}
	}
}

// What the registrations on record render — muster's exchange client allowed
// a private address, the public-registration allowlist — and the keep switch
// of the move are no removal's: rendered where a registration asks for it,
// drift where it is written by hand without one. muster's mcpClient
// token-exchange block holds nothing else, so no migration names it.
func TestRegistrationLeavesAreNotPlanned(t *testing.T) {
	rs, err := definitions.Removals(installations.AgentPlatform)
	if err != nil {
		t.Fatal(err)
	}
	keys := readRemovals(rs, facts{factDomain: testDomain})
	patch := &fileDiff{path: testPlatformPatch, kind: definitions.KindConfigMap}
	for _, path := range []string{
		"agent-platform-mcps.defaults.keep",
		"muster.muster.oauth.mcpClient.tokenExchange.allowPrivateIP",
		"muster.muster.oauth.server.trustedPublicRegistrationRedirectURIs[0]",
	} {
		if got := keys.reason(patch, path); got != "" {
			t.Errorf("%s: planned %q, want drift", path, got)
		}
	}
}
