package customerportal

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
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
var shapes = []string{"customer-portal", "giantswarm-owned-with-platform", "federated-portal"}

// enabledKey is the on/off switch of every plugin section.
const enabledKey = "enabled"

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
	apInput, apSecrets := agentPlatformInput(t)
	apInput["installation"].(map[string]any)["name"] = "hazel"
	apInput["installation"].(map[string]any)["portals"] = []any{map[string]any{"installation": "hazel", "customer": "oakridge", "domain": "portal.hazel.example.test", "clientId": render.PortalDexClientID, "chartLine": input["chart"].(map[string]any)["line"]}}
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

// agentPlatformInput is the agent-platform definition's public-customer shape.
func agentPlatformInput(t *testing.T) (map[string]any, map[string]string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "agentplatform", "testdata", "public-customer", "input.yaml"))
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
// not a field's: the grafana domain when the plugin is on, and the
// portal's domain, organisation and chart line of a portal not on record
// follow it alike. A registry fact the document lacks refuses either way,
// and the caller's document is left as it is.
func TestMissingChoicesCompareNeverRefuses(t *testing.T) {
	base, secrets := loadInput(t, "customer-portal")
	clone := func(mutate func(map[string]any)) map[string]any {
		var c map[string]any
		b, _ := yaml.Marshal(base)
		_ = yaml.Unmarshal(b, &c)
		mutate(c)
		return c
	}
	const grafanaDomain = "plugins.grafana.domain"
	grafanaOn := clone(func(m map[string]any) { m["plugins"].(map[string]any)["grafana"] = map[string]any{enabledKey: true} })
	notOnRecord := clone(func(m map[string]any) {
		delete(m, "chart")
		delete(m["portal"].(map[string]any), "domain")
		delete(m["portal"].(map[string]any), "organization")
	})
	for _, c := range []struct {
		name    string
		input   map[string]any
		missing []string
	}{
		{"grafana on without its domain", grafanaOn, []string{grafanaDomain}},
		{"a portal not on record", notOnRecord, []string{"chart.line", "portal.domain", "portal.organization"}},
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
			if appConfig := string(tree[appConfigOf(t, tree)]); c.missing[0] == grafanaDomain && !strings.Contains(appConfig, "domain: "+render.Missing(grafanaDomain)) {
				t.Errorf("the app-config's grafana section carries no marker:\n%s", appConfig)
			}
			if after, _ := yaml.Marshal(c.input); !bytes.Equal(before, after) {
				t.Errorf("the caller's document changed:\n%s", after)
			}
			_, err = Render(c.input, secrets, render.ModeCommit)
			if !errors.Is(err, ErrInput) {
				t.Fatalf("a commit: got %v, want %v", err, ErrInput)
			}
			for _, field := range c.missing {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("the commit's refusal %q does not name %s", err, field)
				}
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

// appConfigOf is the app-config's path in a rendered tree.
func appConfigOf(t *testing.T, tree map[string][]byte) string {
	t.Helper()
	for name := range tree {
		if strings.HasSuffix(name, "/"+appConfigFile) {
			return name
		}
	}
	t.Fatal("no app-config in the tree")
	return ""
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
	withoutGitHub := clone(func(m map[string]any) { m["plugins"].(map[string]any)["github"] = map[string]any{enabledKey: false} })
	// federation is a federation input over the installations listed, each
	// with its facts on record, and the fields given (signInInstallation).
	federation := func(fields map[string]any, names ...string) map[string]any {
		var insts []any
		for _, name := range names {
			insts = append(insts, map[string]any{"name": name, "baseDomain": name + ".acme.example.test", "providers": []any{"capa"}, "pipeline": "stable"})
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
		{"grafana without a domain", clone(func(m map[string]any) { m["plugins"].(map[string]any)["grafana"] = map[string]any{enabledKey: true} }), secrets, ErrInput, "plugins.grafana.domain"},
		{"missing supplied secret", base, map[string]string{fieldGitHubAppID: "1", fieldGitHubClientID: "x"}, ErrEmptySecret, fieldGitHubClientSecret},
		{"unknown secret value", withoutGitHub, map[string]string{fieldGitHubClientID: "x"}, ErrUnknownSecret, fieldGitHubClientID},
		{"sentry without its values", clone(func(m map[string]any) { m["plugins"].(map[string]any)["sentry"] = map[string]any{enabledKey: true} }), secrets, ErrEmptySecret, fieldSentryAppDSN},
		{"providers without the installation's own", clone(func(m map[string]any) { m["installation"].(map[string]any)["providers"] = []any{"capv"} }), secrets, ErrInput, "installation.providers"},
		{"federation lists the portal's own installation", clone(func(m map[string]any) { m["federation"] = federation(map[string]any{}, "maple") }), secrets, ErrInput, "federation.installations"},
		{"sign-in through an installation the portal does not show", clone(func(m map[string]any) {
			m["federation"] = federation(map[string]any{"signInInstallation": "elm"}, "alder")
		}), secrets, ErrInput, "federation.signInInstallation"},
		{"federation without its credentials", clone(func(m map[string]any) { m["federation"] = federation(map[string]any{}, "alder") }), secrets, ErrEmptySecret, "federation.alder.clientId"},
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
