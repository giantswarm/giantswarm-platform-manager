package customerportal

import (
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

var update = flag.Bool("update", false, "rewrite the golden filesets from the current render")

// shapes are the portal shapes with a golden fileset under testdata/<shape>/.
var shapes = []string{"customer-portal", "giantswarm-owned-with-platform", "federated-portal", "hand-kept-portal"}

// enabledKey is the on/off switch of every plugin section; domainKey the
// portal's and the Grafana host's domain leaf; agentPlatformKey the agent
// platform's key: an installation's fact and the portal's section.
const (
	enabledKey       = "enabled"
	domainKey        = "domain"
	agentPlatformKey = "agentPlatform"
)

func loadInput(t *testing.T, shape string) (map[string]any, map[string]string) {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(filepath.Join("testdata", shape)), "input.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input   map[string]any    `yaml:"input"`
		Secrets map[string]string `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Input, doc.Secrets
}

func TestGolden(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			result, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			got := result.Tree()
			dir := filepath.Join("testdata", shape, "golden")
			if *update {
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				for name, content := range got {
					if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o750); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			want := map[string][]byte{}
			golden := os.DirFS(dir)
			err = fs.WalkDir(golden, ".", func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				content, err := fs.ReadFile(golden, path)
				if err != nil {
					return err
				}
				want[filepath.FromSlash(path)] = content
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for name, content := range want {
				if !bytes.Equal(got[name], content) {
					t.Errorf("%s differs from the golden (run with -update to accept):\n--- want\n%s\n--- got\n%s", name, content, got[name])
				}
			}
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("%s rendered but not in the golden (run with -update to accept)", name)
				}
			}
		})
	}
}

func TestSecretFilesCarryNoValues(t *testing.T) {
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		for repo, files := range result.Files {
			for path, f := range files {
				if !strings.Contains(path, "secret") && !strings.Contains(path, "credential") {
					if len(f.Generated) > 0 {
						t.Errorf("%s: %s carries generated placeholders but is not named as a secret file", repo, path)
					}
					continue
				}
				if !bytes.Contains(f.Content, []byte("kind: Secret")) {
					continue
				}
				for _, g := range f.Generated {
					if !bytes.Contains(f.Content, []byte(g.Placeholder)) {
						t.Errorf("%s: %s lists %s but does not carry its placeholder", repo, path, g.Name)
					}
				}
			}
		}
	}
}

func TestOwnedPathsOnly(t *testing.T) {
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		name := input["installation"].(map[string]any)["name"].(string)
		for repo, files := range result.Files {
			for path := range files {
				if !strings.Contains(path, "/"+name+"/") {
					t.Errorf("%s: %s: %s is outside the installation's own directories", shape, repo, path)
				}
				for _, inc := range result.Includes {
					if inc.Repository == repo && inc.Path == path {
						t.Errorf("%s: %s: %s is rendered and also an include of a shared file", shape, repo, path)
					}
				}
			}
		}
		if result.Len() == 0 {
			t.Fatal("an enabled installation renders no files")
		}
	}
}

// TestDexClientOwnership holds the ownership rule: without the platform this
// definition renders the dex-app patch with the portal's client; with it, the
// patch is the agent-platform definition's and carries the identical entry.
func TestDexClientOwnership(t *testing.T) {
	input, secrets := loadInput(t, "customer-portal")
	result, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	patch, ok := result.Files["giantswarm/acme-configs"]["installations/maple/apps/dex-app/configmap-values.yaml.patch"]
	if !ok {
		t.Fatal("no dex patch without the platform")
	}
	if got, want := portalEntry(t, patch.Content), roundTrip(t, render.PortalDexClient("portal.maple.acme.example.test", "maple")); !reflect.DeepEqual(got, want) {
		t.Errorf("the dex patch carries %v, want %v", got, want)
	}

	input, secrets = loadInput(t, "giantswarm-owned-with-platform")
	result, err = Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Files["giantswarm/giantswarm-configs"]; ok {
		t.Error("a dex patch rendered with the platform enabled; the agent-platform definition owns it")
	}
	// The agent-platform definition, given the portal's domain and the chart line this definition wrote, carries the same entry.
	apInput, apSecrets := agentPlatformInput(t, "public-customer")
	apInput["installation"].(map[string]any)["name"] = "hazel"
	apInput["installation"].(map[string]any)["portals"] = []any{map[string]any{"installation": "hazel", "customer": "oakridge", domainKey: "portal.hazel.example.test", "clientId": render.PortalDexClientID, "chartLine": input["chart"].(map[string]any)["line"]}}
	apResult, err := agentplatform.Render(apInput, apSecrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	var apPatch []byte
	for _, files := range apResult.Files {
		for path, f := range files {
			if strings.HasSuffix(path, "dex-app/configmap-values.yaml.patch") {
				apPatch = f.Content
			}
		}
	}
	if got, want := portalEntry(t, apPatch), roundTrip(t, render.PortalDexClient("portal.hazel.example.test", "hazel")); !reflect.DeepEqual(got, want) {
		t.Errorf("the agent-platform definition's dex patch carries %v, want %v", got, want)
	}
	// And the Component entry this definition lists is the one the agent-platform definition asks for.
	extras := result.Files["giantswarm/giantswarm-management-clusters"]["management-clusters/hazel/extras/backstage/kustomization.yaml"]
	var asked string
	for _, inc := range apResult.Includes {
		if inc.Component {
			asked = inc.Resource
		}
	}
	if asked == "" || !bytes.Contains(extras.Content, []byte("- "+asked+"\n")) {
		t.Errorf("the extras kustomization does not list the Component %q the agent-platform definition asks for:\n%s", asked, extras.Content)
	}
}

// TestExtraEnvVarsOwnership holds the rule for the portal's environment:
// backstage.extraEnvVars is one list Helm replaces wholesale across the
// HelmRelease's values sources, so the portal's user-values own it whole —
// the avatars source listing the host of every installation the portal shows
// that runs the platform (its own first, then the federated ones in list
// order, space-separated), NODE_EXTRA_CA_CERTS with the tunnel, no list
// without either — and the agent-platform definition's Component sets none,
// on a portal it owns the other lists of included.
func TestExtraEnvVarsOwnership(t *testing.T) {
	avatars := envEntry(avatarsEnv, "https://avatars.hazel.example.test")
	sibling := envEntry(avatarsEnv, "https://avatars.alder.acme.example.test")
	ownAndSibling := envEntry(avatarsEnv, "https://avatars.birch.acme.example.test https://avatars.alder.acme.example.test")
	ca := envEntry(tunnelCAEnv, tunnelBundleMount+"/"+tunnelBundleFile)
	tunnelOnly, portalSecrets := loadInput(t, "customer-portal")
	tunnelOnly["tunnel"] = map[string]any{enabledKey: true}
	// The federated shape's own installation runs the platform as well as
	// the sibling that signs people in.
	bothPlatforms, federatedSecrets := loadInput(t, "federated-portal")
	bothPlatforms["installation"].(map[string]any)[agentPlatformKey] = true
	for _, c := range []struct {
		name    string
		shape   string
		input   map[string]any
		secrets map[string]string
		want    []map[string]any
	}{
		{"platform and tunnel", "giantswarm-owned-with-platform", nil, nil, []map[string]any{avatars, ca}},
		{"neither", "customer-portal", nil, nil, nil},
		{"a sibling's platform alone", "federated-portal", nil, nil, []map[string]any{sibling}},
		{"tunnel alone", "", tunnelOnly, portalSecrets, []map[string]any{ca}},
		{"own and a sibling's platform", "", bothPlatforms, federatedSecrets, []map[string]any{ownAndSibling}},
	} {
		input, secrets := c.input, c.secrets
		if c.shape != "" {
			input, secrets = loadInput(t, c.shape)
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := extraEnvVars(t, result); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: extraEnvVars %v, want %v", c.name, got, c.want)
		}
	}
	apInput, apSecrets := agentPlatformInput(t, "public-customer")
	apResult, err := agentplatform.Render(apInput, apSecrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	component := false
	for repo, files := range apResult.Files {
		for path, f := range files {
			if strings.Contains(path, "/extras/backstage/") {
				component = true
			}
			if bytes.Contains(f.Content, []byte("extraEnvVars")) {
				t.Errorf("%s: %s sets extraEnvVars; the portal's user-values own the list", repo, path)
			}
		}
	}
	if !component {
		t.Fatal("the agent-platform shape renders no portal Component to hold the rule against")
	}
}

// envEntry is one extraEnvVars entry as the decoder returns it.
func envEntry(name, value string) map[string]any { return map[string]any{"name": name, "value": value} }

// extraEnvVars is backstage.extraEnvVars of the rendered user-values, decoded;
// nil where the key is absent.
func extraEnvVars(t *testing.T, result *render.Result) []map[string]any {
	t.Helper()
	for _, files := range result.Files {
		for path, f := range files {
			if !strings.HasSuffix(path, "/"+userValuesFile) {
				continue
			}
			var cm struct {
				Data struct {
					Values string `yaml:"values"`
				} `yaml:"data"`
			}
			if err := yaml.Unmarshal(f.Content, &cm); err != nil {
				t.Fatal(err)
			}
			var values struct {
				Backstage struct {
					ExtraEnvVars []map[string]any `yaml:"extraEnvVars"`
				} `yaml:"backstage"`
			}
			if err := yaml.Unmarshal([]byte(cm.Data.Values), &values); err != nil {
				t.Fatal(err)
			}
			return values.Backstage.ExtraEnvVars
		}
	}
	t.Fatal("no user-values rendered")
	return nil
}

// portalEntry is the portal's client in a rendered dex patch, decoded.
func portalEntry(t *testing.T, patch []byte) any {
	t.Helper()
	var doc struct {
		OIDC struct {
			Extra []map[string]any `yaml:"extraStaticClients"`
		} `yaml:"oidc"`
	}
	if err := yaml.Unmarshal(patch, &doc); err != nil {
		t.Fatal(err)
	}
	for _, c := range doc.OIDC.Extra {
		if c["id"] == render.PortalDexClientID {
			return c
		}
	}
	return nil
}

// roundTrip is v as the same decoder returns it.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal(render.MustYAML(v), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestNoFileOrObjectIsRenderedByTwoDefinitions holds the two definitions to
// disjoint filesets on an installation that hosts a portal and runs the
// platform: no repository path and no Kubernetes object (kind, namespace and
// name) is rendered by both, so a reconcile of both capabilities writes every
// file and declares every object once. The portal's fixture is the Giant
// Swarm-owned portal with the platform; the platform's is the Giant
// Swarm-owned shape over the same installation, hosting that portal — the
// two filesets meet in the portal's tree, where the platform's Component
// lands next to the portal's directory, and in Dex's namespace, where the
// portal's client Secret had two owners.
func TestNoFileOrObjectIsRenderedByTwoDefinitions(t *testing.T) {
	input, secrets := loadInput(t, "giantswarm-owned-with-platform")
	portal, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	installation := input["installation"].(map[string]any)
	name, customer := installation["name"].(string), installation["customer"].(string)
	apInput, apSecrets := agentPlatformInput(t, "giantswarm-owned")
	apInstallation := apInput["installation"].(map[string]any)
	if apInstallation["customer"] != customer {
		t.Fatalf("the fixtures are of two organisations, %s and %s, and never meet in one repository", apInstallation["customer"], customer)
	}
	apInstallation["name"], apInstallation["baseDomain"] = name, installation["baseDomain"]
	apInstallation["portals"] = []any{map[string]any{"installation": name, "customer": customer, domainKey: input["portal"].(map[string]any)[domainKey],
		"clientId": render.PortalDexClientID, "chartLine": input["chart"].(map[string]any)["line"]}}
	platform, err := agentplatform.Render(apInput, apSecrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}

	var met bool
	for repo, files := range portal.Files {
		for path := range files {
			if _, both := platform.Files[repo][path]; both {
				t.Errorf("%s: %s is rendered by both definitions", repo, path)
			}
			met = met || strings.Contains(path, "/"+name+"/") && len(platform.Files[repo]) > 0
		}
	}
	if !met {
		t.Fatal("the fixtures render into different repositories; the test proves nothing")
	}
	portalObjects, platformObjects := renderedObjects(t, portal), renderedObjects(t, platform)
	for id, file := range portalObjects {
		if other, both := platformObjects[id]; both {
			t.Errorf("%s is declared by both definitions: %s and %s", id, file, other)
		}
	}
	if len(portalObjects) == 0 || len(platformObjects) == 0 {
		t.Fatalf("objects rendered: portal %d, platform %d", len(portalObjects), len(platformObjects))
	}
}

// renderedObjects lists the Kubernetes objects a result declares — every YAML
// document of every file with a kind and a metadata.name — as "<kind>
// <namespace>/<name>" to the file that declares it.
func renderedObjects(t *testing.T, result *render.Result) map[string]string {
	t.Helper()
	objects := map[string]string{}
	for repo, files := range result.Files {
		for path, f := range files {
			dec := yaml.NewDecoder(bytes.NewReader(f.Content))
			for {
				var doc struct {
					Kind     string `yaml:"kind"`
					Metadata struct {
						Name      string `yaml:"name"`
						Namespace string `yaml:"namespace"`
					} `yaml:"metadata"`
				}
				if err := dec.Decode(&doc); err != nil {
					if errors.Is(err, io.EOF) {
						break
					}
					t.Fatalf("%s: %s: %v", repo, path, err)
				}
				if doc.Kind == "" || doc.Metadata.Name == "" {
					continue
				}
				objects[doc.Kind+" "+doc.Metadata.Namespace+"/"+doc.Metadata.Name] = string(repo) + ":" + path
			}
		}
	}
	return objects
}

// agentPlatformInput is one of the agent-platform definition's golden shapes.
func agentPlatformInput(t *testing.T, shape string) (map[string]any, map[string]string) {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(filepath.Join("..", "agentplatform", "testdata", shape)), "input.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input   map[string]any    `yaml:"input"`
		Secrets map[string]string `yaml:"secrets"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Input, doc.Secrets
}

// TestProbesAreLiveDimensions holds the probes to features.yaml: every probe
// observes a kind: live dimension of the feature it names, and every live
// dimension has at least one probe on one of the shapes.
func TestProbesAreLiveDimensions(t *testing.T) {
	raw, err := definitions.FS.ReadFile("customer-portal/features.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Features map[string]struct {
			Dimensions []struct {
				ID   string `yaml:"id"`
				Kind string `yaml:"kind"`
			} `yaml:"dimensions"`
		} `yaml:"features"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	live := map[string]string{} // dimension id -> feature
	for feature, f := range doc.Features {
		for _, d := range f.Dimensions {
			if d.Kind == definitions.KindLive {
				live[d.ID] = feature
			}
		}
	}
	if len(live) == 0 {
		t.Fatal("features.yaml has no live dimension")
	}
	probed := map[string]bool{}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Probes) == 0 {
			t.Errorf("%s: no probes", shape)
		}
		if len(result.Actions) != 0 {
			t.Errorf("%s: the portal has no customer actions, got %+v", shape, result.Actions)
		}
		for _, p := range result.Probes {
			feature, ok := live[p.ID]
			if !ok {
				t.Errorf("%s: probe %s is not a live dimension of features.yaml", shape, p.ID)
				continue
			}
			if feature != p.Feature {
				t.Errorf("%s: probe %s names feature %s, features.yaml has it under %s", shape, p.ID, p.Feature, feature)
			}
			probed[p.ID] = true
		}
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !probed[id] {
			t.Errorf("live dimension %s of feature %s has no probe on any shape", id, live[id])
		}
	}
}

// A required person input no layer of the document holds is a choice not
// on record: a comparison renders its Missing marker and names it, never
// refusing; a commit refuses it by field. The rule is the input layer's,
// not a field's: the portal's domain, organisation and chart line of a
// portal not on record follow it alike. A registry fact the document lacks
// refuses either way, and the caller's document is left as it is.
func TestMissingChoicesCompareNeverRefuses(t *testing.T) {
	base, secrets := loadInput(t, "customer-portal")
	clone := func(mutate func(map[string]any)) map[string]any {
		var c map[string]any
		b, _ := yaml.Marshal(base)
		_ = yaml.Unmarshal(b, &c)
		mutate(c)
		return c
	}
	notOnRecord := clone(func(m map[string]any) {
		delete(m, "chart")
		delete(m["portal"].(map[string]any), domainKey)
		delete(m["portal"].(map[string]any), "organization")
	})
	oneChoice := clone(func(m map[string]any) { delete(m["portal"].(map[string]any), "organization") })
	for _, c := range []struct {
		name    string
		input   map[string]any
		missing []string
		refused string
	}{
		{"a portal not on record", notOnRecord, []string{"chart.line", "portal.domain", "portal.organization"},
			"the semver range the portal's OCIRepository follows (chart.line), the portal's hostname (portal.domain) and the organisation's name as the portal shows it (portal.organization) are not on record; supply them under Apply changes"},
		{"one choice not on record", oneChoice, []string{"portal.organization"},
			"the organisation's name as the portal shows it (portal.organization) is not on record; supply it under Apply changes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			before, _ := yaml.Marshal(c.input)
			in, err := Parse(c.input)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := in.MissingInputs(); !reflect.DeepEqual(got, c.missing) {
				t.Fatalf("missing %v, want %v", got, c.missing)
			}
			result, err := Render(c.input, secrets, render.ModeCompare)
			if err != nil {
				t.Fatalf("a comparison refused: %v", err)
			}
			tree := result.Tree()
			var rendered strings.Builder
			for _, content := range tree {
				rendered.Write(content)
			}
			for _, field := range c.missing {
				if !strings.Contains(rendered.String(), render.Missing(field)) {
					t.Errorf("no file carries %s", render.Missing(field))
				}
			}
			if after, _ := yaml.Marshal(c.input); !bytes.Equal(before, after) {
				t.Errorf("the caller's document changed:\n%s", after)
			}
			// A commit's refusal is one sentence for a person: what each
			// choice is, its key, and that Apply changes supplies it; the
			// error carries the definition's prefix for the logs.
			_, err = Render(c.input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) {
				t.Fatalf("a commit: got %v, want %v", err, ErrInput)
			}
			if got := render.Reason(err); got != c.refused {
				t.Errorf("the commit's refusal reads %q, want %q", got, c.refused)
			}
			if want := ErrInput.Error() + ": " + c.refused; err.Error() != want {
				t.Errorf("the commit's error reads %q, want %q", err, want)
			}
		})
	}
	noName := clone(func(m map[string]any) { delete(m["installation"].(map[string]any), "name") })
	for _, mode := range []render.Mode{render.ModeCompare, render.ModeCommit} {
		if _, err := Render(noName, secrets, mode); !errors.Is(err, ErrInput) || !strings.Contains(err.Error(), "name") {
			t.Errorf("mode %d: a missing registry fact: got %v", mode, err)
		}
	}
}

// fileOf is the path of the portal's file of that name in a rendered tree.
func fileOf(t *testing.T, tree map[string][]byte, file string) string {
	t.Helper()
	for name := range tree {
		if strings.HasSuffix(name, "/"+file) {
			return name
		}
	}
	t.Fatalf("no %s in the tree", file)
	return ""
}

// appConfigDoc is the portal's app-config of a rendered tree, decoded: the
// YAML text of backstage.appConfig in the ConfigMap's values.
func appConfigDoc(t *testing.T, tree map[string][]byte) map[string]any {
	t.Helper()
	var cm struct {
		Data struct {
			Values string `yaml:"values"`
		} `yaml:"data"`
	}
	if err := yaml.Unmarshal(tree[fileOf(t, tree, appConfigFile)], &cm); err != nil {
		t.Fatal(err)
	}
	var values struct {
		Backstage struct {
			AppConfig string `yaml:"appConfig"`
		} `yaml:"backstage"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data.Values), &values); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(values.Backstage.AppConfig), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// The Grafana section is never dropped — the plugin's config schema requires
// it, and a portal without it does not start — and names one host: the
// installation's own Grafana, under the installation's name. Wired, the proxy
// entry the dashboards card reads through targets it with the token's
// environment variable, the user secrets carry the token as the chart reads
// it, and the extension list the app-config includes is the shared one with
// the dashboards card switched on; not wired, none of the three: the list is
// the baseline, and the card stays disabled in the app.
func TestGrafanaIsTheInstallationsOwn(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			result, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			tree := result.Tree()
			appConfig := appConfigDoc(t, tree)
			inst := input["installation"].(map[string]any)
			name, grafana := inst["name"].(string), "https://grafana."+inst["baseDomain"].(string)
			section, _ := appConfig["grafana"].(map[string]any)
			if want := []any{map[string]any{"id": name, domainKey: grafana}}; !reflect.DeepEqual(section["hosts"], want) {
				t.Errorf("grafana section %v, want hosts %v", section, want)
			}
			wired := input["plugins"].(map[string]any)["grafana"].(map[string]any)[enabledKey] == true
			proxy, hasProxy := appConfig["proxy"]
			userSecrets := string(tree[fileOf(t, tree, userSecretsFile)])
			extensions, _ := appConfig["app"].(map[string]any)["extensions"].(map[string]any)
			anchor, _ := extensions["$include"].(string)
			if !strings.HasPrefix(anchor, "shared-config.yaml#extensions") || strings.HasSuffix(anchor, "GrafanaDashboards") != wired {
				t.Errorf("wired %v: app.extensions %v, want the include of a shared list with the dashboards card exactly where wired", wired, extensions)
			}
			if !wired {
				if hasProxy || strings.Contains(userSecrets, "grafana") {
					t.Errorf("not wired, yet the app-config carries %v and the user secrets:\n%s", proxy, userSecrets)
				}
				return
			}
			endpoint, _ := proxy.(map[string]any)["endpoints"].(map[string]any)[grafanaProxy].(map[string]any)
			headers, _ := endpoint["headers"].(map[string]any)
			if endpoint["target"] != grafana+"/" || headers["Authorization"] != "Bearer "+envVar(grafanaTokenVar) {
				t.Errorf("proxy entry %v", proxy)
			}
			if token := base64.StdEncoding.EncodeToString([]byte(secrets[fieldGrafanaToken])); !strings.Contains(userSecrets, "grafana:\n      apiToken: "+token+"\n") {
				t.Errorf("the user secrets do not carry the token as the chart reads it:\n%s", userSecrets)
			}
		})
	}
}

func TestRefusals(t *testing.T) {
	base, secrets := loadInput(t, "customer-portal")
	clone := func(mutate func(map[string]any)) map[string]any {
		var c map[string]any
		b, _ := yaml.Marshal(base)
		_ = yaml.Unmarshal(b, &c)
		mutate(c)
		return c
	}
	// withoutSupplied switches off every plugin that takes a supplied value.
	withoutSupplied := clone(func(m map[string]any) {
		m["plugins"].(map[string]any)["github"] = map[string]any{enabledKey: false}
		m["plugins"].(map[string]any)["grafana"] = map[string]any{enabledKey: false}
	})
	// federation is a federation input over the installations listed, each
	// with its facts on record, and the fields given (signInInstallation).
	federation := func(fields map[string]any, names ...string) map[string]any {
		var insts []any
		for _, name := range names {
			insts = append(insts, map[string]any{"name": name, "baseDomain": name + ".acme.example.test", "providers": []any{"capa"}, "pipeline": "stable", agentPlatformKey: false})
		}
		fields["installations"] = insts
		return fields
	}
	cases := []struct {
		name    string
		input   map[string]any
		secrets map[string]string
		err     error
		names   string
	}{
		{"unknown top-level key", clone(func(m map[string]any) { m["colourScheme"] = "dark" }), secrets, ErrInput, "colourScheme"},
		{"unknown nested key", clone(func(m map[string]any) { m["portal"].(map[string]any)["replicas"] = 3 }), secrets, ErrInput, "replicas"},
		{"unknown plugin", clone(func(m map[string]any) { m["plugins"].(map[string]any)["jenkins"] = map[string]any{enabledKey: true} }), secrets, ErrInput, "jenkins"},
		{"chart line not a range", clone(func(m map[string]any) { m["chart"].(map[string]any)["line"] = "2.1.0" }), secrets, ErrInput, "line"},
		{"github with the app id as an input", clone(func(m map[string]any) { m["plugins"].(map[string]any)["github"].(map[string]any)["appId"] = 123456 }), secrets, ErrInput, fieldGitHubAppID},
		{"github without its app id", base, without(secrets, fieldGitHubAppID), ErrEmptySecret, fieldGitHubAppID},
		{"app id not a number", base, with(secrets, fieldGitHubAppID, "one"), ErrInput, fieldGitHubAppID},
		{"app id not positive", base, with(secrets, fieldGitHubAppID, "0"), ErrInput, fieldGitHubAppID},
		{"grafana with the host as an input", clone(func(m map[string]any) {
			m["plugins"].(map[string]any)["grafana"] = map[string]any{enabledKey: true, domainKey: "https://grafana.example.test"}
		}), secrets, ErrInput, inputGrafanaDomain},
		{"grafana wired without its token", base, without(secrets, fieldGrafanaToken), ErrEmptySecret, fieldGrafanaToken},
		{"missing supplied secret", base, map[string]string{fieldGitHubAppID: "1", fieldGitHubClientID: "x"}, ErrEmptySecret, fieldGitHubClientSecret},
		{"unknown secret value", withoutSupplied, map[string]string{fieldGitHubClientID: "x"}, ErrUnknownSecret, fieldGitHubClientID},
		{"sentry without its values", clone(func(m map[string]any) { m["plugins"].(map[string]any)["sentry"] = map[string]any{enabledKey: true} }), secrets, ErrEmptySecret, fieldSentryAppDSN},
		{"providers without the installation's own", clone(func(m map[string]any) { m["installation"].(map[string]any)["providers"] = []any{"capv"} }), secrets, ErrInput, "installation.providers"},
		{"federation lists the portal's own installation", clone(func(m map[string]any) { m["federation"] = federation(map[string]any{}, "maple") }), secrets, ErrInput, "federation.installations"},
		{"sign-in through an installation the portal does not show", clone(func(m map[string]any) {
			m["federation"] = federation(map[string]any{"signInInstallation": "elm"}, "alder")
		}), secrets, ErrInput, "federation.signInInstallation"},
		{"federation without its credentials", clone(func(m map[string]any) { m["federation"] = federation(map[string]any{}, "alder") }), secrets, ErrEmptySecret, "federation.alder.clientId"},
		{"federated installation without its platform fact", clone(func(m map[string]any) {
			m["federation"] = federation(map[string]any{}, "alder")
			delete(m["federation"].(map[string]any)["installations"].([]any)[0].(map[string]any), agentPlatformKey)
		}), secrets, ErrInput, agentPlatformKey},
	}
	// Every refusal reads as one sentence: what the input is, its key, what
	// is wrong and what supplies it.
	reasons := map[string]string{
		inputGrafanaDomain:         "the installation's own Grafana (plugins.grafana.domain) is no input; it is derived from installation.baseDomain",
		"installation.providers":   "the providers the installation's entry lists (installation.providers) do not include the installation's own provider capz; the installations registry supplies them",
		"federation.installations": "the other installations the portal shows (federation.installations) name maple, the portal's own installation or one listed twice; list each other installation once under Apply changes",
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Render(c.input, c.secrets, render.ModeCommit)
			if !errors.Is(err, c.err) {
				t.Fatalf("got %v, want %v", err, c.err)
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Fatalf("%q does not name %q", err, c.names)
			}
			if want := reasons[c.names]; want != "" && render.Reason(err) != want {
				t.Errorf("reads %q, want %q", render.Reason(err), want)
			}
		})
	}
}

// TestSuppliedMarkers renders a dry run: a marker per supplied field, no value.
func TestSuppliedMarkers(t *testing.T) {
	input, _ := loadInput(t, "giantswarm-owned-with-platform")
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	fields := in.SuppliedSecretFields()
	if len(fields) != 8 {
		t.Fatalf("supplied fields: %v", fields)
	}
	result, err := Render(input, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fields {
		found := false
		for _, files := range result.Files {
			for _, file := range files {
				found = found || bytes.Contains(file.Content, []byte(Supplied(f)))
			}
		}
		if !found {
			t.Errorf("marker for %s not rendered", f)
		}
	}
	// The app id lives only in the encrypted file: the marker lands there, as
	// the value of appId.
	for _, files := range result.Files {
		for path, file := range files {
			if strings.HasSuffix(path, githubAppFile) && !bytes.Contains(file.Content, []byte("appId: "+Supplied(fieldGitHubAppID)+"\n")) {
				t.Errorf("%s does not carry the app id's marker:\n%s", path, file.Content)
			}
		}
	}
}

// without is secrets without field; with is secrets with field set to value.
func without(secrets map[string]string, field string) map[string]string {
	out := maps.Clone(secrets)
	delete(out, field)
	return out
}

func with(secrets map[string]string, field, value string) map[string]string {
	out := maps.Clone(secrets)
	out[field] = value
	return out
}

// TestDexAuthCredentialsAreBase64ForTheChart holds the dexAuthCredentials
// leaves of user-secrets-backstage to what the backstage chart reads: it
// copies clientID and clientSecret under its Secret's data: as they are, so
// every leaf is base64 — the portal's own id decodes to the Dex client id,
// a supplied credential to the value supplied — and the portal's own client
// secret is the base64 declaration of the value the Dex client's Secret
// carries raw, so the two sides agree on the secret.
func TestDexAuthCredentialsAreBase64ForTheChart(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			result, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			name := input["installation"].(map[string]any)["name"].(string)
			var userSecrets, dexClient render.File
			for _, files := range result.Files {
				for path, f := range files {
					switch filepath.Base(path) {
					case userSecretsFile:
						userSecrets = f
					case dexClientFile:
						dexClient = f
					}
				}
			}
			var values struct {
				Credentials map[string]struct {
					ClientID     string `yaml:"clientID"`
					ClientSecret string `yaml:"clientSecret"`
				} `yaml:"dexAuthCredentials"`
			}
			if err := yaml.Unmarshal([]byte(stringData(t, userSecrets, "values")), &values); err != nil {
				t.Fatal(err)
			}
			dexSide := stringData(t, dexClient, render.DexSecretKey)

			own := values.Credentials[name]
			if got := decodeBase64(t, own.ClientID); got != render.PortalDexClientID {
				t.Errorf("dexAuthCredentials.%s.clientID decodes to %q, want %q", name, got, render.PortalDexClientID)
			}
			encoded := declarationOf(t, userSecrets, own.ClientSecret)
			if encoded.Encoding != render.EncodedBase64 {
				t.Errorf("dexAuthCredentials.%s.clientSecret is declared with encoding %q, want %q", name, encoded.Encoding, render.EncodedBase64)
			}
			rawDecl := declarationOf(t, dexClient, dexSide)
			if rawDecl.Encoding != "" {
				t.Errorf("%s's %s is declared with encoding %q, want the raw value", dexClientFile, render.DexSecretKey, rawDecl.Encoding)
			}
			if encoded.Name != rawDecl.Name || own.ClientSecret == dexSide {
				t.Errorf("the portal's client secret %q and the Dex client's %q must be one value at two placeholders", own.ClientSecret, dexSide)
			}
			for key, c := range values.Credentials {
				if key == name {
					continue
				}
				field := "federation." + key
				if key == brokerCredentials {
					field = fieldTokenBroker
				}
				if got := decodeBase64(t, c.ClientID); got != secrets[field+suffixClientID] {
					t.Errorf("dexAuthCredentials.%s.clientID decodes to %q, want the supplied %s", key, got, field+suffixClientID)
				}
				if got := decodeBase64(t, c.ClientSecret); got != secrets[field+suffixClientSecret] {
					t.Errorf("dexAuthCredentials.%s.clientSecret decodes to %q, want the supplied %s", key, got, field+suffixClientSecret)
				}
			}
		})
	}
}

// TestGeneratedChartDataIsEncodedOnce holds every generated value of
// user-secrets-backstage to the encoding the backstage chart needs: the chart
// copies every leaf of the file under the data: of its Secrets, so each
// placeholder is declared base64 — the commit step fills it with the value's
// base64, which Kubernetes decodes back to the value — and a leaf carries the
// placeholder alone, never base64 of it. The session secret and the salt keep
// their names, so a rotation by name reaches them.
// TestRenderConsumptionSecrets holds the same to the chart.
func TestGeneratedChartDataIsEncodedOnce(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			result, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			userSecrets, ok := portalFile(result, userSecretsFile)
			if !ok {
				t.Fatalf("no %s rendered", userSecretsFile)
			}
			var values struct {
				Session   string `yaml:"authSessionSecret"`
				Telemetry struct {
					Salt string `yaml:"salt"`
				} `yaml:"telemetrydeck"`
			}
			if err := yaml.Unmarshal([]byte(stringData(t, userSecrets, "values")), &values); err != nil {
				t.Fatal(err)
			}
			for leaf, want := range map[string]struct {
				placeholder, name string
			}{
				"authSessionSecret":  {values.Session, generatedSessionSecret},
				"telemetrydeck.salt": {values.Telemetry.Salt, generatedTelemetrySalt},
			} {
				if g := declarationOf(t, userSecrets, want.placeholder); g.Name != want.name {
					t.Errorf("%s carries the placeholder of %q, want %q", leaf, g.Name, want.name)
				}
			}
			for _, g := range userSecrets.Generated {
				if g.Encoding != render.EncodedBase64 {
					t.Errorf("%s declares %q with encoding %q, want %q: the chart copies the leaf under data:", userSecretsFile, g.Name, g.Encoding, render.EncodedBase64)
				}
			}
		})
	}
}

// TestSentryLeavesAreBase64ForTheChart holds the sentry leaves of
// user-secrets-backstage to what the backstage chart reads: it copies
// sentry.app.dsn, sentry.backend.dsn and sentry.reportURI under its Secret's
// data: as they are and the pod loads that Secret with envFrom, so with sentry
// on each leaf is the base64 of the value the person supplied, a dry run's
// marker stays a marker, and with sentry off the values carry no sentry key.
func TestSentryLeavesAreBase64ForTheChart(t *testing.T) {
	type sentryValues struct {
		Sentry *struct {
			App struct {
				DSN string `yaml:"dsn"`
			} `yaml:"app"`
			Backend struct {
				DSN string `yaml:"dsn"`
			} `yaml:"backend"`
			ReportURI string `yaml:"reportURI"`
		} `yaml:"sentry"`
	}
	leaves := func(t *testing.T, input map[string]any, secrets map[string]string, mode render.Mode) *sentryValues {
		t.Helper()
		result, err := Render(input, secrets, mode)
		if err != nil {
			t.Fatal(err)
		}
		var values sentryValues
		if err := yaml.Unmarshal([]byte(stringData(t, fileNamed(t, result, userSecretsFile), "values")), &values); err != nil {
			t.Fatal(err)
		}
		return &values
	}
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			enabled, _ := input["plugins"].(map[string]any)["sentry"].(map[string]any)[enabledKey].(bool)
			values := leaves(t, input, secrets, render.ModeCommit)
			if !enabled {
				if values.Sentry != nil {
					t.Fatalf("sentry is off but user-secrets carry sentry values: %+v", *values.Sentry)
				}
				return
			}
			if values.Sentry == nil {
				t.Fatal("sentry is on but user-secrets carry no sentry values")
			}
			for _, leaf := range []struct{ name, got, field string }{
				{"sentry.app.dsn", values.Sentry.App.DSN, fieldSentryAppDSN},
				{"sentry.backend.dsn", values.Sentry.Backend.DSN, fieldSentryBackendDSN},
				{"sentry.reportURI", values.Sentry.ReportURI, fieldSentryReportURI},
			} {
				if got := decodeBase64(t, leaf.got); got != secrets[leaf.field] {
					t.Errorf("%s decodes to %q, want the supplied %s %q", leaf.name, got, leaf.field, secrets[leaf.field])
				}
			}
			in, err := Parse(input)
			if err != nil {
				t.Fatal(err)
			}
			markers := leaves(t, input, in.SuppliedMarkers(), render.ModeCompare)
			for _, leaf := range []struct{ name, got, field string }{
				{"sentry.app.dsn", markers.Sentry.App.DSN, fieldSentryAppDSN},
				{"sentry.backend.dsn", markers.Sentry.Backend.DSN, fieldSentryBackendDSN},
				{"sentry.reportURI", markers.Sentry.ReportURI, fieldSentryReportURI},
			} {
				if leaf.got != Supplied(leaf.field) {
					t.Errorf("a dry run's %s is %q, want the marker %q", leaf.name, leaf.got, Supplied(leaf.field))
				}
			}
		})
	}
}

// fileNamed is the one rendered file whose base name is name.
func fileNamed(t *testing.T, result *render.Result, name string) render.File {
	t.Helper()
	for _, files := range result.Files {
		for path, f := range files {
			if filepath.Base(path) == name {
				return f
			}
		}
	}
	t.Fatalf("no rendered file is named %s", name)
	return render.File{}
}

// stringData is the value of key under the stringData of the Secret f renders.
func stringData(t *testing.T, f render.File, key string) string {
	t.Helper()
	var secret struct {
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal(f.Content, &secret); err != nil {
		t.Fatal(err)
	}
	return secret.StringData[key]
}

// declarationOf is the generated declaration of f whose placeholder is value.
func declarationOf(t *testing.T, f render.File, value string) render.Generated {
	t.Helper()
	for _, g := range f.Generated {
		if g.Placeholder == value {
			return g
		}
	}
	t.Fatalf("%q is no generated placeholder of the file", value)
	return render.Generated{}
}

func decodeBase64(t *testing.T, value string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("%q is not base64: %v", value, err)
	}
	return string(b)
}

// The portal's app-config keeps the agent-platform section until the
// Component's fragment on record owns the portal's lists: on a hand-kept
// portal moving onto the definition it includes the shared list with the
// platform's section — the chat's entries where the portal runs the chat,
// the dashboards card where the plugin is wired — and carries the section as
// the record does (the muster registry, the kagent installation, the skill
// repositories, the chat's blocks), so the running portal keeps them. Once
// the fragment carries the lists, the list is the one without the section
// and the section is the fragment's alone; without the platform there is no
// Component, and the app-config carries neither.
func TestPlatformSectionStaysUntilTheFragmentOwnsTheLists(t *testing.T) {
	input, secrets := loadInput(t, "hand-kept-portal")
	section := input["platformSection"].(map[string]any)
	kept := section["appConfig"].(map[string]any)
	for _, tc := range []struct {
		name                 string
		componentLists, chat bool
		agentPlatform, keeps bool
		anchor               string
	}{
		{name: "hand-kept, the fragment in the hand-kept shape", chat: true, agentPlatform: true, keeps: true, anchor: "extensionsAgentPlatformAiChatGrafanaDashboards"},
		{name: "the chat off", agentPlatform: true, keeps: true, anchor: "extensionsAgentPlatformGrafanaDashboards"},
		{name: "the fragment owns the lists", componentLists: true, chat: true, agentPlatform: true, anchor: "extensionsGrafanaDashboards"},
		{name: "no platform", chat: true, anchor: "extensionsGrafanaDashboards"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			section["componentLists"], section["aiChat"] = tc.componentLists, tc.chat
			input["installation"].(map[string]any)[agentPlatformKey] = tc.agentPlatform
			supplied := secrets
			if !tc.chat || !tc.agentPlatform {
				supplied = without(secrets, fieldChatKey)
			}
			result, err := Render(input, supplied, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			appConfig := appConfigDoc(t, result.Tree())
			if got := appConfig["app"].(map[string]any)["extensions"]; !reflect.DeepEqual(got, map[string]any{"$include": render.PortalSharedInclude(tc.anchor)}) {
				t.Errorf("app.extensions %v, want the include of %s", got, tc.anchor)
			}
			backend, _ := appConfig["backend"].(map[string]any)
			for _, key := range []string{"muster", agentPlatformKey, "aiChat", "mcpActions"} {
				if got, has := appConfig[key]; has != tc.keeps || tc.keeps && !reflect.DeepEqual(roundTrip(t, got), roundTrip(t, kept[key])) {
					t.Errorf("%s: %v (kept %v), want the record's %v", key, got, tc.keeps, kept[key])
				}
			}
			if actions, has := backend["actions"]; has != tc.keeps || tc.keeps && !reflect.DeepEqual(roundTrip(t, actions), roundTrip(t, kept["backend"].(map[string]any)["actions"])) {
				t.Errorf("backend.actions: %v (kept %v)", actions, tc.keeps)
			}
			if backend["baseUrl"] != "https://portal.gopher.example.io" {
				t.Errorf("the rendered backend is not the definition's under the kept actions: %v", backend)
			}
		})
	}
}

// The AI chat's credential is the portal's own until the agent-platform
// Component's credentials Secret is on record: where the platform runs and
// the record carries the chat, a commit asks for it by the field the
// agent-platform definition names it with and writes it into user-secrets —
// the Anthropic API key as anthropic.apiKey, base64-encoded once for the
// chart's ANTHROPIC_API_KEY, which the chat's kept block references; a
// Vertex chat's service-account JSON as google.credentialsJson, as supplied —
// so a hand-kept portal's first commit keeps its chat answering. A dry run
// renders the marker; a commit without the value is refused. Once the
// Component's Secret is on record, or without the chat or the platform,
// user-secrets carry none and nothing is asked.
func TestChatCredentialStaysThePortalsUntilTheComponentCarriesIt(t *testing.T) {
	type chatValues struct {
		Anthropic *struct {
			APIKey string `yaml:"apiKey"`
		} `yaml:"anthropic"`
		Google *struct {
			CredentialsJSON string `yaml:"credentialsJson"`
		} `yaml:"google"`
	}
	const googleJSON = `{"project_id": "placeholder"}`
	for _, tc := range []struct {
		name, provider, field           string
		credentials, noChat, noPlatform bool
	}{
		{name: "hand-kept, on Anthropic's API", field: fieldChatKey},
		{name: "hand-kept, on Vertex AI", provider: chatProviderVertex, field: fieldChatGoogleCredentials},
		{name: "the Component's Secret on record", credentials: true},
		{name: "no chat", noChat: true},
		{name: "no platform", noPlatform: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, secrets := loadInput(t, "hand-kept-portal")
			section := input["platformSection"].(map[string]any)
			section["chatCredentials"], section["aiChat"] = tc.credentials, !tc.noChat
			if tc.provider != "" {
				section["chatProvider"] = tc.provider
			}
			input["installation"].(map[string]any)[agentPlatformKey] = !tc.noPlatform
			secrets = without(secrets, fieldChatKey)
			in, err := Parse(input)
			if err != nil {
				t.Fatal(err)
			}
			fields := in.SuppliedSecretFields()
			for _, f := range []string{fieldChatKey, fieldChatGoogleCredentials} {
				if asked := slices.Contains(fields, f); asked != (f == tc.field) {
					t.Errorf("%s asked %v, want %v: %v", f, asked, f == tc.field, fields)
				}
			}
			values := func(t *testing.T, secrets map[string]string, mode render.Mode) chatValues {
				t.Helper()
				result, err := Render(input, secrets, mode)
				if err != nil {
					t.Fatal(err)
				}
				var v chatValues
				if err := yaml.Unmarshal([]byte(stringData(t, fileNamed(t, result, userSecretsFile), "values")), &v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			switch tc.field {
			case "":
				if v := values(t, secrets, render.ModeCommit); v.Anthropic != nil || v.Google != nil {
					t.Errorf("user-secrets carry the chat's credential: %+v", v)
				}
				return
			case fieldChatKey:
				const key = "placeholder-anthropic-key"
				if got := values(t, with(secrets, fieldChatKey, key), render.ModeCommit); got.Anthropic == nil || decodeBase64(t, got.Anthropic.APIKey) != key || got.Google != nil {
					t.Errorf("user-secrets carry %+v, want anthropic.apiKey the base64 of the supplied key", got)
				}
				if got := values(t, in.SuppliedMarkers(), render.ModeCompare); got.Anthropic == nil || got.Anthropic.APIKey != Supplied(fieldChatKey) {
					t.Errorf("a dry run's anthropic.apiKey is %+v, want the marker", got.Anthropic)
				}
			case fieldChatGoogleCredentials:
				if got := values(t, with(secrets, fieldChatGoogleCredentials, googleJSON), render.ModeCommit); got.Google == nil || got.Google.CredentialsJSON != googleJSON || got.Anthropic != nil {
					t.Errorf("user-secrets carry %+v, want google.credentialsJson as supplied", got)
				}
			}
			if _, err := Render(input, secrets, render.ModeCommit); !errors.Is(err, ErrEmptySecret) {
				t.Errorf("a commit without %s: %v, want ErrEmptySecret", tc.field, err)
			}
		})
	}
}

// Every key this definition hands to the agent-platform definition — its
// removals of kind other-definition, the changes a dry run names Moved or
// Changed into the Component — is one that definition renders: an
// app-config key in the Component's fragment, a user-values key in its
// values, a Vertex chat's credentials (the file, its resource entry, its
// values source) as the Component's own credentials Secret and the patch
// that appends it to the portal's HelmRelease. The agent-platform render is
// the shape that carries all of them: a rendered portal on a chart before
// backstage 1.1.0 with kagent, the chat on Vertex, and skill repositories.
func TestHandedKeysAreRenderedByTheAgentPlatform(t *testing.T) {
	input, secrets := agentPlatformInput(t, "public-customer")
	input["skills"] = map[string]any{"repositories": []any{"https://github.com/example/agent-skills"}}
	result, err := agentplatform.Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	tree := result.Tree()
	component := "extras/backstage/" + render.PortalPlatformDir + "/"
	fragment := configMapData(t, tree[fileOf(t, tree, component+"app-config.yaml")])
	values := configMapData(t, tree[fileOf(t, tree, component+"values.yaml")])
	kustomization := string(tree[fileOf(t, tree, component+"kustomization.yaml")])
	credentials := string(tree[fileOf(t, tree, component+"ai-chat-credentials.enc.yaml")])
	removals, err := definitions.Removals("customer-portal")
	if err != nil {
		t.Fatal(err)
	}
	var handed int
	for _, r := range removals {
		if r.Kind != definitions.RemovalOtherDefinition {
			continue
		}
		handed++
		fileset, path, _ := strings.Cut(strings.TrimPrefix(r.Key, definitions.KindBackstage+":"), ":")
		switch fileset {
		case "app-config":
			if !carries(fragment, path) {
				t.Errorf("%s is handed to the Component, whose fragment does not carry %s:\n%s", r.Key, path, tree[fileOf(t, tree, component+"app-config.yaml")])
			}
		case "user-values":
			if !carries(values, path) {
				t.Errorf("%s is handed to the Component, whose values do not carry %s: %v", r.Key, path, values)
			}
		case "kustomization", "file":
			if !strings.Contains(credentials, "credentialsJson:") || !strings.Contains(kustomization, "name: agent-platform-ai-chat-credentials-backstage") {
				t.Errorf("%s is handed to the Component, which carries no credentials Secret for the chat on Vertex appended to the HelmRelease:\n%s\n%s", r.Key, kustomization, credentials)
			}
		default:
			t.Errorf("%s: a handed key of a file the Component has no counterpart of", r.Key)
		}
	}
	if handed == 0 {
		t.Fatal("removals.yaml hands no key to the agent-platform definition; the test proves nothing")
	}
}

// configMapData is the one data key of a rendered ConfigMap, decoded: the
// YAML text it carries.
func configMapData(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(content, &cm); err != nil || len(cm.Data) != 1 {
		t.Fatalf("a ConfigMap with one data key: %v\n%s", err, content)
	}
	var doc map[string]any
	for _, text := range cm.Data {
		if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
			t.Fatal(err)
		}
	}
	return doc
}

// carries says whether doc holds the key path of a removal, cut at its first
// list step or $include: the list or the include is the key.
func carries(doc map[string]any, path string) bool {
	if i := strings.IndexAny(path, "[$"); i >= 0 {
		path = strings.TrimSuffix(path[:i], ".")
	}
	var cur any = doc
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	return true
}
