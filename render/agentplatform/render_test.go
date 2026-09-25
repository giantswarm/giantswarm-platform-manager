package agentplatform

import (
	"bytes"
	"errors"
	"flag"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

var update = flag.Bool("update", false, "rewrite the golden filesets from the current render")

// The installation shapes with a golden fileset under testdata/<shape>/.
const (
	shapePublicCustomer         = "public-customer"
	shapeGiantswarmOwned        = "giantswarm-owned"
	shapeGiantswarmSlackApp     = "giantswarm-slack-app"
	shapeGiantswarmSlackAppPub  = "giantswarm-slack-app-public"
	shapeHubPrivateTarget       = "hub-private-target"
	shapeMultiClusterAggregator = "multi-cluster-aggregator"
	shapeSecondHub              = "second-hub"
	shapeHandKeptPortal         = "hand-kept-portal"
	shapeRegisteredServers      = "registered-servers"
)

// The keys of the choices' documents the refusal cases build.
const (
	keyEnabled  = "enabled"
	keyModel    = "model"
	keyProvider = "provider"
	keyGoogle   = "google"
	keyCapacity = "singletonsCapacity"
)

// shapes are the installation shapes, in the order the goldens are rendered.
var shapes = []string{shapePublicCustomer, shapeGiantswarmOwned, shapeGiantswarmSlackApp, shapeGiantswarmSlackAppPub, shapeHubPrivateTarget, shapeMultiClusterAggregator, shapeSecondHub, shapeHandKeptPortal, shapeRegisteredServers}

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

// TestGoldensCoverTheChat holds the golden shapes to the chat's shapes: a
// rendered portal with the chat, whose fragment includes the shared list
// with the chat and carries the aiChat block and the credentials Secret
// listed by the Component; a hand-kept portal with a hand-kept chat, whose
// fragment carries the block, no list and no Secret (the portal's
// environment supplies the credential); a portal without the chat, whose
// fragment carries none of it and no Secret; and both providers among the
// chats.
func TestGoldensCoverTheChat(t *testing.T) {
	covered := map[string]bool{}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		host := in.portalHost()
		if host == "" {
			continue
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		tree := result.Tree()
		dir := "giantswarm/" + in.Installation.Customer + "-management-clusters/management-clusters/" + host + "/extras/backstage/" + portalDir + "/"
		fragment, kustomization := string(tree[dir+"app-config.yaml"]), string(tree[dir+"kustomization.yaml"])
		_, secret := tree[dir+portalCredentialsFile]
		chat, include := strings.Contains(fragment, "\n    aiChat:\n"), strings.Contains(fragment, "#extensionsAgentPlatformAiChat")
		listed := strings.Contains(kustomization, "- "+portalCredentialsFile+"\n") && strings.Contains(kustomization, "name: "+portalCredentialsSecret+"\n")
		if in.aiChatVertex() {
			values := string(tree[dir+"values.yaml"])
			if !strings.Contains(fragment, "        provider: "+providerVertex+"\n") || !strings.Contains(fragment, "        keyFilename: "+googleCredentialsPath+"\n") || strings.Contains(fragment, "apiKey") || !strings.Contains(values, "\n    google:\n") || !strings.Contains(string(tree[dir+portalCredentialsFile]), "credentialsJson: ") {
				t.Errorf("%s: the chat on Vertex: the fragment, the values and the Secret do not carry the provider's keys\n%s\n%s", shape, fragment, values)
			}
			covered[providerVertex] = true
		} else if chat {
			if !strings.Contains(fragment, "        apiKey: "+anthropicKeyEnv+"\n") || strings.Contains(fragment, "google") {
				t.Errorf("%s: the chat on Anthropic's API: the fragment does not carry the key's variable, or carries Google keys\n%s", shape, fragment)
			}
			covered[providerAnthropic] = true
		}
		switch {
		case !in.aiChat():
			if chat || include || secret || listed {
				t.Errorf("%s: the chat off, the fragment carries it: block %v include %v secret %v listed %v", shape, chat, include, secret, listed)
			}
			covered["off"] = true
		case in.portalOwnsLists():
			if !chat || !include || !secret || !listed {
				t.Errorf("%s: the chat on a rendered portal: block %v include %v secret %v listed %v", shape, chat, include, secret, listed)
			}
			covered["rendered"] = true
		default:
			if !chat || include || secret || listed || in.chatKeyIsComponents() {
				t.Errorf("%s: the chat by hand on a hand-kept portal: block %v include %v secret %v listed %v", shape, chat, include, secret, listed)
			}
			covered["hand-kept"] = true
		}
	}
	for _, shape := range []string{"off", "rendered", "hand-kept", providerAnthropic, providerVertex} {
		if !covered[shape] {
			t.Errorf("no golden shape renders the chat %s", shape)
		}
	}
}

// TestGoldensCoverBothLines holds the golden shapes to the meta chart's lines:
// at least one shape renders on each line; a 3-line shape's configmap patch
// carries no key of a 4-line-only component (the 3 line's schema refuses
// them); a 4-line shape the policy grants the cluster-manager carries its
// toggle and values.
func TestGoldensCoverBothLines(t *testing.T) {
	rendered := map[string]bool{}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		line := in.Installation.ChartLine
		rendered[line] = true
		patch := string(result.Files[render.Repository("giantswarm/"+in.Installation.Customer+"-configs")]["installations/"+in.Installation.Name+"/apps/agent-platform/configmap-values.yaml.patch"].Content)
		for _, c := range lineFourComponents {
			if named := strings.Contains(patch, c+":"); named != (line == lineFour && in.Components[c]) {
				t.Errorf("%s (line %s, %s granted %v): the configmap patch names %s: %v", shape, line, c, in.Components[c], c, named)
			}
		}
	}
	for _, line := range []string{lineThree, lineFour} {
		if !rendered[line] {
			t.Errorf("no golden shape renders on the %s line", line)
		}
	}
}

// The probe of the API Agent Substrate needs renders where kagent runs on the
// 4 line and nowhere else: a 4-line shape the plan lets through has the
// record saying yes, and the probe reads the apiserver for it; a 3-line
// shape renders no such probe whatever its record says. The chart table
// answers by chart and version, a version below the first or a chart it
// does not know being no.
func TestPodCertificateRequestProbeAndChartTable(t *testing.T) {
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		var probed int
		for _, p := range result.Probes {
			if p.ID == podCertificateRequestDimension {
				probed++
				if p.Kind != render.APIServed || p.Feature != featureRuntime || p.Resource != PodCertificateRequestResource || p.Expect.Version != PodCertificateRequestVersion || p.Expect.Note == "" {
					t.Errorf("%s: the probe %+v", shape, p)
				}
			}
		}
		if want := in.kagent() && in.Installation.ChartLine == lineFour; (probed == 1) != want || probed > 1 {
			t.Errorf("%s (line %s, kagent %v): %d probe(s) of the API", shape, in.Installation.ChartLine, in.kagent(), probed)
		}
	}
	type at struct {
		version string
		want    bool
	}
	for chart, versions := range map[string][]at{
		"cluster-aws":            {{"10.3.0", true}, {"v10.4.2", true}, {"10.2.9", false}, {"latest", false}},
		"cluster-azure":          {{"9.3.0", true}, {"9.2.0", false}},
		"cluster-cloud-director": {{"7.3.0", true}, {"7.2.0", false}},
		"cluster-vsphere":        {{"9.2.0", false}},
		"cluster-eks":            {{"3.0.0", false}},
		"":                       {{"", false}},
	} {
		for _, c := range versions {
			if got := ClusterChartHasPodCertificateRequest(chart, c.version); got != c.want {
				t.Errorf("%s %s: %v, want %v", chart, c.version, got, c.want)
			}
		}
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

// Two installations of one organisation land in the same repositories, a wave
// commits them into one pull request per repository, and the commit step draws
// one value per generated name across a pull request: every generated name
// carries its installation and no name is generated by two installations, so
// no client secret is ever shared between installations or organisations. The
// one name two renders share is a hub's client in a target's Dex — one client,
// its secret in the hub's credentials and the target's Dex, named after the
// client, which names the pair.
// pairNames are the generated names a hub and a target may share: the
// client secret of the hub's client in the target's Dex, either way round,
// the hub the registry's or not.
func pairNames(a, b string) []string {
	var names []string
	for _, registryHub := range []bool{true, false} {
		names = append(names, exchangeSecretName(tokenExchangeClient(a, b, registryHub)), exchangeSecretName(tokenExchangeClient(b, a, registryHub)))
	}
	return names
}

func TestGeneratedValuesArePerInstallation(t *testing.T) {
	generatedBy := map[string]string{}
	for _, shape := range []string{shapePublicCustomer, shapeMultiClusterAggregator} {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		name, _ := input["installation"].(map[string]any)["name"].(string)
		for repo, files := range result.Files {
			for path, f := range files {
				for _, g := range f.Generated {
					if !strings.Contains("-"+g.Name+"-", "-"+name+"-") {
						t.Errorf("%s: %s: %s does not carry the installation %s", repo, path, g.Name, name)
					}
					other, seen := generatedBy[g.Name]
					if seen && other != name && !slices.Contains(pairNames(other, name), g.Name) {
						t.Errorf("%s is generated by %s and %s: one value would serve two installations", g.Name, other, name)
					}
					generatedBy[g.Name] = name
				}
			}
		}
	}
}

func TestOwnedPathsOnly(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) { testOwnedPathsOnly(t, shape) })
	}
}

func testOwnedPathsOnly(t *testing.T, shape string) {
	input, secrets := loadInput(t, shape)
	result, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	name := in.Installation.Name
	// The platform's portal section lives in the organisation's own portal, which
	// another installation of the organisation may host.
	portal := "management-clusters/" + in.portalHost() + "/extras/backstage/" + portalDir + "/"
	for repo, files := range result.Files {
		for path := range files {
			// teleport-fleet's tunnelport values are another owner's file: the plan edits the hub's
			// entries into it and never writes it whole.
			theirs := repo == teleportFleet && path == tunnelportValues
			if !theirs && !strings.Contains(path, "/"+name+"/") && !strings.HasPrefix(path, portal) {
				t.Errorf("%s: %s is outside the installation's own directories", repo, path)
			}
			for _, inc := range result.Includes {
				if inc.Repository == repo && inc.Path == path {
					t.Errorf("%s: %s is rendered and also an include of a shared file", repo, path)
				}
			}
		}
	}
	// Enable then disable: the disabled fileset is empty by construction, so the
	// delta is exactly the rendered paths, and every one of them may be deleted.
	if result.Len() == 0 {
		t.Fatal("an enabled installation renders no files")
	}
}

// TestProbesAreLiveDimensions holds the probes to features.yaml: every probe
// observes a kind: live dimension of the feature it names, every action names
// a feature, and every live dimension has at least one probe on the
// public-customer shape.
func TestProbesAreLiveDimensions(t *testing.T) {
	raw, err := definitions.FS.ReadFile("agent-platform/features.yaml")
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
			if d.Kind == "live" {
				live[d.ID] = feature
			}
		}
	}
	if len(live) == 0 {
		t.Fatal("features.yaml has no live dimension")
	}
	// probed is, per dimension, the shape that probes it: the public-customer
	// shape stands for every dimension but the 4 line's own, which a 4-line
	// shape (giantswarm-owned) stands for, and the registered servers', which
	// the shape that registers some stands for.
	probed := map[string]bool{}
	standsFor := func(shape, id string) bool {
		switch id {
		case podCertificateRequestDimension:
			return shape == shapeGiantswarmOwned
		case registeredServersDimension:
			return shape == shapeRegisteredServers
		}
		return shape == shapePublicCustomer
	}
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Probes) == 0 {
			t.Errorf("%s: no probes", shape)
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
			if standsFor(shape, p.ID) {
				probed[p.ID] = true
			}
		}
		for _, a := range result.Actions {
			if _, ok := doc.Features[a.Feature]; !ok {
				t.Errorf("%s: action %s names feature %s, which features.yaml does not have", shape, a.ID, a.Feature)
			}
		}
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !probed[id] {
			t.Errorf("live dimension %s of feature %s has no probe on the shape that stands for it", id, live[id])
		}
	}
}

func TestRefusals(t *testing.T) {
	base, secrets := loadInput(t, shapePublicCustomer)
	slackApp, slackSecrets := loadInput(t, shapeGiantswarmSlackApp)
	// The private installation's Slack credentials without the app-level token
	// its socket mode needs.
	withoutAppToken := map[string]string{}
	for k, v := range slackSecrets {
		if k != fieldSlack+"app-token" {
			withoutAppToken[k] = v
		}
	}
	slackAppPublic, slackPublicSecrets := loadInput(t, shapeGiantswarmSlackAppPub)
	slackPublicSecrets[fieldSlack+"app-token"] = "x"
	// A Giant Swarm-owned installation — the policy grants it the cluster-manager —
	// with the capability on record whose record has no agentPlatform.kagentApiV2
	// and so selects the 3 line: adoption keeps the record's line, so the line-4
	// component is refused (a fresh enable selects 4 instead: TestFreshEnableSelectsTheFourLine).
	lineThreeOwned, _ := loadInput(t, shapeGiantswarmOwned)
	lineThreeOwned["installation"].(map[string]any)["chartLine"] = lineThree
	lineThreeOwned["installation"].(map[string]any)["agentPlatform"] = true
	delete(lineThreeOwned, "modelServing")
	// A 4-line record whose cluster App does not say the cluster serves
	// PodCertificateRequest: no gates on record, a chart before the default.
	noPodCertificateRequest, _ := loadInput(t, shapePublicCustomer)
	noPodCertificateRequest["installation"].(map[string]any)["chartLine"] = lineFour
	target := func(private bool) map[string]any {
		return map[string]any{"installation": "x", "baseDomain": "x.example", "private": private}
	}
	clone := func(mutate func(map[string]any)) map[string]any {
		var c map[string]any
		b, _ := yaml.Marshal(base)
		_ = yaml.Unmarshal(b, &c)
		mutate(c)
		return c
	}
	federation := func(m map[string]any) map[string]any {
		return m["installation"].(map[string]any)["federation"].(map[string]any)
	}
	// The shape's supplied values with one more, or one fewer: the public
	// customer's chat runs on Vertex, so its credentials are among them.
	with := func(field, value string) map[string]string {
		out := maps.Clone(secrets)
		out[field] = value
		return out
	}
	chatOff := clone(func(m map[string]any) { delete(m, "aiChat") })
	cases := []struct {
		name    string
		input   map[string]any
		secrets map[string]string
		err     error
		names   string
	}{
		{"unknown top-level key", clone(func(m map[string]any) { m["colourScheme"] = "dark" }), secrets, ErrInput, "colourScheme"},
		{"a former input is unknown", clone(func(m map[string]any) { m["kagent"] = map[string]any{keyEnabled: true} }), secrets, ErrInput, "kagent"},
		{"unknown record key", clone(func(m map[string]any) { m["installation"].(map[string]any)["replicas"] = 3 }), secrets, ErrInput, "replicas"},
		{"empty Slack credential where the gateway runs", slackAppPublic, nil, ErrEmptySecret, fieldSlack + "bot-token"},
		{"no app-level token on a private installation", slackApp, withoutAppToken, ErrEmptySecret, fieldSlack + "app-token"},
		{"an app-level token on a public installation", slackAppPublic, slackPublicSecrets, ErrUnknownSecret, fieldSlack + "app-token"},
		{"a Slack credential where no gateway runs", base, with(fieldSlack+"bot-token", "x"), ErrUnknownSecret, fieldSlack + "bot-token"},
		{"the model key is never supplied", base, with("kagent.modelKey", "x"), ErrUnknownSecret, "kagent.modelKey"},
		{"on-demand singletons without Karpenter", clone(func(m map[string]any) {
			m["installation"].(map[string]any)["provider"] = "capz"
			m["scheduling"] = map[string]any{keyCapacity: capacityOnDemand}
		}), secrets, ErrInput, "scheduling." + keyCapacity},
		{"a capacity Karpenter does not name", clone(func(m map[string]any) { m["scheduling"] = map[string]any{keyCapacity: "spot"} }), secrets, ErrInput, keyCapacity},
		{"serving on the 3 line", clone(func(m map[string]any) { m["modelServing"] = map[string]any{keyEnabled: true} }), secrets, ErrInput, "modelServing.enabled"},
		{"the chat without a portal to carry it", clone(func(m map[string]any) {
			m["installation"].(map[string]any)["portals"] = []any{}
			m["aiChat"] = map[string]any{keyEnabled: true, keyModel: "x"}
		}), secrets, ErrInput, "aiChat.enabled"},
		{"the chat without a model", clone(func(m map[string]any) { m["aiChat"] = map[string]any{keyEnabled: true} }), secrets, ErrInput, "aiChat.model"},
		{"the chat's key not supplied", clone(func(m map[string]any) { m["aiChat"] = map[string]any{keyEnabled: true, keyModel: "x"} }), nil, ErrEmptySecret, fieldAnthropicKey},
		{"the chat's key where no chat runs", chatOff, map[string]string{fieldAnthropicKey: "x"}, ErrUnknownSecret, fieldAnthropicKey},
		{"a Vertex chat's credentials where no chat runs", chatOff, secrets, ErrUnknownSecret, fieldGoogleCredentials},
		{"a Vertex chat without its project", clone(func(m map[string]any) {
			m["aiChat"] = map[string]any{keyEnabled: true, keyModel: "x", keyProvider: providerVertex, keyGoogle: map[string]any{"location": "eu"}}
		}), secrets, ErrInput, "aiChat.google.project"},
		{"a Vertex chat without its location", clone(func(m map[string]any) {
			m["aiChat"] = map[string]any{keyEnabled: true, keyModel: "x", keyProvider: providerVertex, keyGoogle: map[string]any{"project": "p"}}
		}), secrets, ErrInput, "aiChat.google.location"},
		{"a Vertex chat's credentials not supplied", base, nil, ErrEmptySecret, fieldGoogleCredentials},
		{"an API key for a Vertex chat", base, with(fieldAnthropicKey, "k"), ErrUnknownSecret, fieldAnthropicKey},
		{"a provider the chat does not run on", clone(func(m map[string]any) {
			m["aiChat"] = map[string]any{keyEnabled: true, keyModel: "x", keyProvider: "openai"}
		}), secrets, ErrInput, "provider"},
		{"a component of the 4 line on a record that selects the 3 line", lineThreeOwned, nil, ErrInput, "installation.chartLine selects the 3 line, and cluster-manager needs the platform's 4 chart line; agentPlatform.kagentApiV2: true in installations/gopher/config.yaml.patch selects 4"},
		{"kagent on the 4 line where the record does not say the cluster serves PodCertificateRequest", noPodCertificateRequest, secrets, ErrInput, "installation.podCertificateRequest does not say this cluster serves certificates.k8s.io/v1beta1 podcertificaterequests, which kagent's Agent Substrate on the 4 chart line needs; enable the feature gates PodCertificateRequest, ClusterTrustBundle, ClusterTrustBundleProjection under cluster.internal.advancedConfiguration.{controlPlane.apiServer,controlPlane.controllerManager,kubelet}.featureGates in the cluster App's values (management-clusters/" + noPodCertificateRequest["installation"].(map[string]any)["name"].(string) + "/cluster-app-manifests.yaml), or run a cluster App chart that enables them by default (cluster-aws 10.3.0, cluster-azure 9.3.0, cluster-cloud-director 7.3.0 and later)"},
		{"targets without a broker client", clone(func(m map[string]any) {
			federation(m)["targets"] = []any{target(false)}
		}), secrets, ErrInput, "federation.brokerClientId"},
		{"private target on a hub without a published issuer", clone(func(m map[string]any) {
			m["installation"].(map[string]any)["provider"] = "capz"
			f := federation(m)
			f["brokerClientId"], f["targets"] = "b", []any{target(true)}
		}), secrets, ErrInput, "federation.targets"},
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

// TestGatewayPerInstallation resolves the gateway's shape per installation:
// the entry's own default agent, the policy's where the entry names none, and
// the shape untouched for an installation without a Slack app.
func TestGatewayPerInstallation(t *testing.T) {
	var pol policy
	if err := yaml.Unmarshal([]byte(`
klausGateway:
  installations:
    own: {defaultAgent: sre-agent}
    bare: {}
    empty:
  a2a:
    enabled: true
    defaultAgent: swarmgeist
`), &pol); err != nil {
		t.Fatal(err)
	}
	const fleet = "swarmgeist"
	for name, want := range map[string]string{"own": "sre-agent", "bare": fleet, "empty": fleet, "none": fleet} {
		g := pol.gateway(Installation{Name: name})
		if g.A2A.DefaultAgent != want {
			t.Errorf("%s: default agent %q, want %q", name, g.A2A.DefaultAgent, want)
		}
		if _, slackApp := g.Installations[name]; slackApp == (name == "none") {
			t.Errorf("%s: a Slack app is named: %v", name, slackApp)
		}
	}
	if pol.KlausGateway.A2A.DefaultAgent != fleet {
		t.Errorf("the policy's default agent changed to %q", pol.KlausGateway.A2A.DefaultAgent)
	}
}

// TestPortalAudiences holds the set the platform trusts for the portals to
// what the definition knows, in order: each portal's client id where its
// host's Dex patch carries it, then backstage where its Secret is on record —
// and none of them twice; an installation nobody lists trusts none. What the installation trusts besides
// is the plan's to keep, not the render's.
func TestPortalAudiences(t *testing.T) {
	const opaqueA, opaqueB = "opaque-a", "opaque-b"
	portal := func(domain, clientID string) PortalRef {
		return PortalRef{Installation: portalCaseOwn, Customer: "giantswarm", Domain: domain, ClientID: clientID}
	}
	cases := []struct {
		name      string
		portals   []PortalRef
		audiences []string
	}{
		{"nobody lists the installation", nil, nil},
		{"a portal whose client the Dex patch carries", []PortalRef{portal("a.example", opaqueA)}, []string{opaqueA, render.PortalDexClientID}},
		{"a portal whose client the Dex patch does not carry", []PortalRef{portal("a.example", "")}, []string{render.PortalDexClientID}},
		{"the same id from two portals, once", []PortalRef{portal("a.example", opaqueA), portal("b.example", opaqueA)}, []string{opaqueA, render.PortalDexClientID}},
		{"a portal that signs in through the definition's client", []PortalRef{portal("a.example", render.PortalDexClientID), portal("b.example", opaqueB)}, []string{render.PortalDexClientID, opaqueB}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := &Input{Installation: Installation{Name: portalCaseOwn, Portals: c.portals, PortalClientSecret: true}}
			if got := in.portalAudiences(); !slices.Equal(got, c.audiences) {
				t.Fatalf("got %v, want %v", got, c.audiences)
			}
		})
	}
	noSecret := &Input{Installation: Installation{Name: portalCaseOwn, Portals: []PortalRef{portal("a.example", opaqueA), portal("b.example", "")}}}
	if got := noSecret.portalAudiences(); !slices.Equal(got, []string{opaqueA}) {
		t.Errorf("no Secret on record for the definition's client: got %v, want [%s]", got, opaqueA)
	}
}

// A fresh enable — the capability not on record — of an organisation the
// policy grants a component the 4 line alone carries selects the 4 line where
// the record selects 3: the parsed input runs the 4 line, Selected answers the
// line for the plan's effective inputs, and the render writes the selection
// into the record as one more file of the configs repository. An organisation
// without such a component keeps its record's line and writes no record; an
// installation on record keeps its line (TestRefusals).
func TestFreshEnableSelectsTheFourLine(t *testing.T) {
	input, secrets := loadInput(t, shapeGiantswarmOwned)
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if in.Installation.ChartLine != lineFour || !in.selectsLine {
		t.Fatalf("a fresh enable of a cluster-manager organisation on a 3-line record runs the 4 line: %s, selected %v", in.Installation.ChartLine, in.selectsLine)
	}
	if sel := in.Selected(); sel["installation"].(map[string]any)["chartLine"] != lineFour {
		t.Fatalf("selected: %v", sel)
	}
	result, err := Render(input, secrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	record := result.Files[render.Repository("giantswarm/giantswarm-configs")][render.RecordPath("gopher")]
	if !strings.Contains(string(record.Content), "agentPlatform:\n  kagentApiV2: true\n") || !strings.Contains(string(record.Content), "cluster-manager") || len(record.Generated) != 0 {
		t.Fatalf("the record fragment: %q", record.Content)
	}
	// On the 4 line already, nothing is selected and no record is written.
	onFour, _ := loadInput(t, shapeGiantswarmSlackApp)
	in, err = Parse(onFour)
	if err != nil {
		t.Fatal(err)
	}
	if in.selectsLine || in.Selected() != nil {
		t.Fatal("a record on the 4 line selects nothing")
	}
	// A customer without a line-4 component keeps the 3 line and writes no record.
	customer, customerSecrets := loadInput(t, shapePublicCustomer)
	in, err = Parse(customer)
	if err != nil {
		t.Fatal(err)
	}
	if in.Installation.ChartLine != lineThree || in.selectsLine || in.Selected() != nil {
		t.Fatalf("a customer's fresh enable stays on its record's line: %s, selected %v", in.Installation.ChartLine, in.selectsLine)
	}
	result, err = Render(customer, customerSecrets, render.ModeCommit)
	if err != nil {
		t.Fatal(err)
	}
	for repo, files := range result.Files {
		for path := range files {
			if strings.HasSuffix(path, "/"+render.RecordFile) {
				t.Errorf("%s: %s written for an organisation without a line-4 component", repo, path)
			}
		}
	}
}

// Revisions names, for every value of a component's credentials Secrets, the
// revision its consumers roll on — muster's on the 4 line (none on the 3
// line, which renders no revision), each MCP server's own — and holds the
// mapping to the Secrets: every other value of a file holding a revision maps
// to that revision, so a key added to a credentials Secret is covered, and
// every name mapped is one the render generates.
func TestRevisionsCoverTheCredentialsSecrets(t *testing.T) {
	for _, shape := range shapes {
		input, secrets := loadInput(t, shape)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		name := in.Installation.Name
		generated, revisions := map[string]bool{}, map[string]bool{}
		for _, r := range result.Revisions {
			revisions[r] = true
		}
		for _, files := range result.Files {
			for path, f := range files {
				var held []string
				for _, g := range f.Generated {
					generated[g.Name] = true
					if revisions[g.Name] {
						held = append(held, g.Name)
					}
				}
				for _, r := range held {
					for _, g := range f.Generated {
						if g.Name != r && result.Revisions[g.Name] != r {
							t.Errorf("%s: %s holds the revision %s and %s, which maps to %q", shape, path, r, g.Name, result.Revisions[g.Name])
						}
					}
				}
			}
		}
		for value := range result.Revisions {
			if !generated[value] {
				t.Errorf("%s: %s is mapped to a revision and not generated", shape, value)
			}
		}
		want := map[string]string{}
		for _, s := range servers {
			for _, v := range []string{"-dex-client-secret", "-oauth-encryption-key", "-valkey-password"} {
				want[name+"-"+s.name+v] = name + "-" + s.name + "-credentials-revision"
			}
		}
		muster := []string{"-muster-dex-client-secret", "-muster-registration-token", "-muster-oauth-encryption-key", "-muster-valkey-password"}
		for _, v := range muster {
			if in.musterRevision() {
				want[name+v] = name + "-muster-credentials-revision"
			} else if r, ok := result.Revisions[name+v]; ok {
				t.Errorf("%s on the %s line: %s rolls with %s, and the line renders no revision", shape, in.Installation.ChartLine, name+v, r)
			}
		}
		for value, revision := range want {
			if got := result.Revisions[value]; got != revision {
				t.Errorf("%s: %s rolls with %q, want %s", shape, value, got, revision)
			}
		}
	}
}
