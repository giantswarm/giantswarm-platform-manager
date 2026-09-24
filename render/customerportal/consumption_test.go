package customerportal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// chartPins is where the agent-platform definition's render-consumption test
// pins the charts it renders; the portal's probes are held to the backstage
// chart at the same pin, so one Renovate bump moves both tests.
const chartPins = "../agentplatform/testdata/consumption/charts.yaml"

// TestRenderConsumption renders the backstage chart at its pin with the chart's
// own defaults — the definition sets none of the values the pod labels and
// the Deployment's name come from — and holds podSelector and deploymentName
// to what it renders: the portal's Deployment, named deploymentName, labels
// its pods so the selector matches them, and no other workload's pods match,
// so live-pods-running reads the portal's pods and only them and
// live-deployment-available the portal's Deployment. A chart that relabels
// its pods or renames the Deployment fails here, not as "no pod matches" on
// every installation. The test needs helm and the network and runs only with
// RENDER_CONSUMPTION=1.
func TestRenderConsumption(t *testing.T) {
	chart, version := pullBackstage(t)
	selector, err := labels.Parse(podSelector)
	if err != nil {
		t.Fatalf("podSelector %q: %v", podSelector, err)
	}
	rendered := helmRun(t, "template", releaseName, chart, "--namespace", backstageNamespace)

	var matched, workloads []string
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var o map[string]any
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decoding the rendered backstage chart: %v", err)
		}
		podLabels, ok := pathValue(o, "spec", "template", "metadata", "labels").(map[string]any)
		if !ok {
			continue
		}
		set := labels.Set{}
		for k, v := range podLabels {
			set[k], _ = v.(string)
		}
		workload := pathValue(o, "kind").(string) + " " + pathValue(o, "metadata", "name").(string)
		workloads = append(workloads, workload+": "+set.String())
		if selector.Matches(set) {
			matched = append(matched, workload)
		}
	}
	if len(workloads) == 0 {
		t.Fatalf("backstage %s renders no workload with a pod template", version)
	}
	if len(matched) != 1 || matched[0] != "Deployment "+deploymentName {
		t.Fatalf("backstage %s: podSelector %q must match the portal's Deployment %s and nothing else, matched %v; the chart labels its pods:\n  %s",
			version, podSelector, deploymentName, matched, strings.Join(workloads, "\n  "))
	}
	t.Logf("backstage %s: %s carries %s", version, matched[0], podSelector)
}

// TestRenderConsumptionSecrets renders the backstage chart at its pin with the
// values every shape's commit writes — the HelmRelease's values sources the
// definition renders, in their order, each generated value filled in as the
// commit step fills it (in the encoding its placeholder declares) and every
// supplied value as supplied — and holds every key of the Secrets the chart
// writes under data: to what the pod reads through envFrom: base64 that
// Kubernetes decodes to valid UTF-8, the value generated, supplied or, for
// the portal's own Dex client id, the definition's. A leaf the chart copies
// under data: that the render leaves unencoded reaches the pod as the bytes
// of a second decode and fails here, not as a container that never starts;
// a key the chart writes that the test does not name fails too, so a value
// the definition starts to render there is held to the contract.
func TestRenderConsumptionSecrets(t *testing.T) {
	chart, version := pullBackstage(t)
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			in, err := Parse(input)
			if err != nil {
				t.Fatal(err)
			}
			result, err := Render(input, secrets, render.ModeCommit)
			if err != nil {
				t.Fatal(err)
			}
			generatedValues := map[string]string{}
			args := []string{"template", releaseName, chart, "--namespace", backstageNamespace}
			for _, file := range []string{appConfigFile, userValuesFile, userSecretsFile, githubAppFile, pluginKeysFile} {
				if f, ok := portalFile(result, file); ok {
					args = append(args, "--values", writeValues(t, file, valuesOf(t, file, commitFill(f, generatedValues))))
				}
			}
			rendered := helmRun(t, args...)

			want := in.chartData(secrets, generatedValues)
			got := dataSecrets(t, rendered)
			for _, name := range sortedKeys(got) {
				expected, ok := want[name]
				if !ok {
					t.Errorf("backstage %s renders Secret %s with data %v, which the definition's values do not account for", version, name, sortedKeys(got[name]))
					continue
				}
				for _, key := range sortedKeys(got[name]) {
					value, ok := expected[key]
					if !ok {
						t.Errorf("backstage %s: %s.%s is not a key the definition renders a value for", version, name, key)
						continue
					}
					decoded, err := base64.StdEncoding.DecodeString(got[name][key])
					switch {
					case err != nil:
						t.Errorf("backstage %s: %s.%s is not base64, the Secret does not apply: %v", version, name, key, err)
					case !utf8.Valid(decoded):
						t.Errorf("backstage %s: %s.%s decodes to bytes that are not UTF-8: the container runtime refuses the pod's environment", version, name, key)
					case string(decoded) != value:
						t.Errorf("backstage %s: %s.%s decodes to %q, want %q", version, name, key, decoded, value)
					}
				}
				for _, key := range sortedKeys(expected) {
					if _, ok := got[name][key]; !ok {
						t.Errorf("backstage %s: %s lacks %s", version, name, key)
					}
				}
			}
			for _, name := range sortedKeys(want) {
				if _, ok := got[name]; !ok {
					t.Errorf("backstage %s renders no Secret %s", version, name)
				}
			}
		})
	}
}

// chartData is what the pod reads from each Secret the backstage chart
// writes under data:, by Secret and key, for the values the definition
// renders with the supplied secrets and the generated values by name: the
// session secret and the telemetry salt (and with the plugins wired the
// Sentry values and the Grafana token) in <name>-secrets, the Dex client
// credentials by installation in <name>-dex-auth-credentials-secret.
func (in *Input) chartData(secrets, generatedValues map[string]string) map[string]map[string]string {
	own := in.Installation.Name
	values := map[string]string{
		"AUTH_SESSION_SECRET": generatedValues[generatedSessionSecret],
		"TELEMETRYDECK_SALT":  generatedValues[generatedTelemetrySalt],
	}
	if in.Plugins.Sentry.Enabled {
		values["SENTRY_DSN_APP"] = secrets[fieldSentryAppDSN]
		values["SENTRY_DSN_BACKEND"] = secrets[fieldSentryBackendDSN]
		values["SENTRY_REPORT_URI"] = secrets[fieldSentryReportURI]
	}
	if in.Plugins.Grafana.Enabled {
		values["GRAFANA_TOKEN"] = secrets[fieldGrafanaToken]
	}
	dex := map[string]string{}
	add := func(key, clientID, clientSecret string) {
		prefix := "AUTH_DEX_" + strings.ToUpper(snakeCase(key))
		dex[prefix+"_CLIENT_ID"], dex[prefix+"_CLIENT_SECRET"] = clientID, clientSecret
	}
	add(own, render.PortalDexClientID, generatedValues[own+"-"+generatedDexClientSecret])
	for _, inst := range in.providerInstallations() {
		if inst.Name != own {
			add(inst.Name, secrets[federationField(inst.Name, suffixClientID)], secrets[federationField(inst.Name, suffixClientSecret)])
		}
	}
	if in.tokenBroker() != "" {
		add(brokerCredentials, secrets[fieldTokenBroker+suffixClientID], secrets[fieldTokenBroker+suffixClientSecret])
	}
	return map[string]map[string]string{releaseName + "-secrets": values, releaseName + "-dex-auth-credentials-secret": dex}
}

// snakeCase is the chart's snakecase of a dexAuthCredentials key (sprig's,
// for the names an installation and the broker's key take): an underscore
// before an upper-case letter that follows a lower-case one or a digit, and
// for a dash.
func snakeCase(s string) string {
	var b strings.Builder
	prev := rune(0)
	for _, r := range s {
		switch {
		case r == '-':
			b.WriteRune('_')
		case unicode.IsUpper(r) && (unicode.IsLower(prev) || unicode.IsDigit(prev)):
			b.WriteRune('_')
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(unicode.ToLower(r))
		}
		prev = r
	}
	return b.String()
}

// portalFile is the rendered file of that name in the portal's directory.
func portalFile(result *render.Result, name string) (render.File, bool) {
	for _, files := range result.Files {
		for path, f := range files {
			if strings.HasSuffix(path, "/"+portalDir+"/"+portalDir+"/"+name) {
				return f, true
			}
		}
	}
	return render.File{}, false
}

// commitFill is f's content with every generated placeholder filled in as the
// commit step fills it: one value per name across the files of a commit
// (generatedValues holds the values drawn so far, by name, as the pod reads
// them), each placeholder receiving it in the encoding it declares. The
// values are fixed per name rather than random: a base64 value's bytes are
// not UTF-8, as random bytes almost never are, so a second decode shows.
func commitFill(f render.File, generatedValues map[string]string) []byte {
	content := f.Content
	for _, g := range f.Generated {
		value := testValue(g)
		if g.Kind != render.KeyPairES256 {
			if drawn, ok := generatedValues[g.Name]; ok {
				value = drawn
			}
			generatedValues[g.Name] = value
		}
		if g.Encoding == render.EncodedBase64 {
			value = base64.StdEncoding.EncodeToString([]byte(value))
		}
		content = bytes.ReplaceAll(content, []byte(g.Placeholder), []byte(value))
	}
	return content
}

// testValue is the value of g's shape a test commit draws for its name: the
// base64 of bytes that are not UTF-8, alphanumeric characters, or a key
// pair's half as a quoted YAML scalar.
func testValue(g render.Generated) string {
	sum := sha256.Sum256([]byte(g.Name))
	switch g.Kind {
	case render.Base64:
		raw := bytes.Repeat(append([]byte{0xff}, sum[:]...), g.Length/len(sum)+1)[:g.Length]
		return base64.StdEncoding.EncodeToString(raw)
	case render.Alphanumeric:
		return strings.Repeat(hex.EncodeToString(sum[:]), g.Length/(2*len(sum))+1)[:g.Length]
	}
	return `"-----BEGIN TEST ` + strings.ToUpper(string(g.Half)) + ` KEY-----"`
}

// valuesOf is the chart values a filled values source carries: a ConfigMap's
// data.values, a Secret's stringData.values.
func valuesOf(t *testing.T, name string, content []byte) string {
	t.Helper()
	var source struct {
		Data       map[string]string `yaml:"data"`
		StringData map[string]string `yaml:"stringData"`
	}
	if err := yaml.Unmarshal(content, &source); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if v, ok := source.StringData["values"]; ok {
		return v
	}
	if v, ok := source.Data["values"]; ok {
		return v
	}
	t.Fatalf("%s carries no values", name)
	return ""
}

// writeValues writes a values file for helm and answers its path.
func writeValues(t *testing.T, name, values string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(values), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// dataSecrets are the data: of every Secret in the rendered chart that has
// one, by name and key, the values as rendered.
func dataSecrets(t *testing.T, rendered []byte) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var o struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data map[string]string `yaml:"data"`
		}
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("decoding the rendered backstage chart: %v", err)
		}
		if o.Kind == "Secret" && len(o.Data) > 0 {
			out[o.Metadata.Name] = o.Data
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// pullBackstage pulls the backstage chart at its pin into a temporary
// directory and answers the archive and the version; it skips the test
// without RENDER_CONSUMPTION=1.
func pullBackstage(t *testing.T) (chart, version string) {
	t.Helper()
	if os.Getenv("RENDER_CONSUMPTION") != "1" {
		t.Skip("set RENDER_CONSUMPTION=1 to render the backstage chart (needs helm and the network)")
	}
	registry, version := backstagePin(t)
	dir := t.TempDir()
	helmRun(t, "pull", "oci://"+registry, "--version", version, "-d", dir)
	return filepath.Join(dir, "backstage-"+version+".tgz"), version
}

// backstagePin reads the backstage chart's registry and version from chartPins.
func backstagePin(t *testing.T) (registry, version string) {
	t.Helper()
	raw, err := os.ReadFile(chartPins)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Charts []struct {
			Name     string `yaml:"name"`
			Registry string `yaml:"registry"`
			Version  string `yaml:"version"`
		} `yaml:"charts"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Charts {
		if c.Name == "backstage" {
			return c.Registry, c.Version
		}
	}
	t.Fatalf("%s pins no backstage chart", chartPins)
	return "", ""
}

// helmRun executes helm with test-controlled arguments and fails the test with
// its stderr when it does not exit 0.
func helmRun(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("helm", args...) // #nosec G204 -- fixed binary, arguments are test-controlled paths and pins
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return out
}

// pathValue walks a decoded document by keys; nil where the path does not exist.
func pathValue(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}
