package installations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	// releaseChart is the chart a release-based cluster HelmRelease takes from
	// its OCIRepository.
	releaseChart = "oci://gsoci.azurecr.io/charts/giantswarm/release-aws"
	// clusterChart is a provider cluster chart a HelmRelease may take
	// directly.
	clusterChart = "oci://gsoci.azurecr.io/charts/giantswarm/cluster-aws"
)

// helmReleaseManifest renders a cluster deployed by Flux as the
// management-clusters repositories would keep it: the ConfigMap with its
// values under the key values, the OCIRepository of url at ref (a ref:
// block's lines), and the HelmRelease taking its chart from it with the
// ConfigMap's values and spec.values below, if any.
func helmReleaseManifest(url, ref, values, specValues string) string {
	m := "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: maple-userconfig\n  namespace: org-fleet\ndata:\n  values: |\n" + indent(values, "    ") +
		"---\napiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: maple\n  namespace: org-fleet\nspec:\n  interval: 10m\n  url: " + url + "\n  ref:\n" + indent(ref, "    ") +
		"---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: maple\n  namespace: org-fleet\nspec:\n  chartRef:\n    kind: OCIRepository\n    name: maple\n  releaseName: maple\n  valuesFrom:\n    - kind: ConfigMap\n      name: maple-userconfig\n      valuesKey: values\n"
	if specValues != "" {
		m += "  values:\n" + indent(specValues, "    ")
	}
	return m
}

// chartConfig renders a chart's config (its Chart.yaml as JSON) depending on
// the cluster subchart at version; no dependency for an empty version.
func chartConfig(chart, version, cluster string) string {
	deps := `{"name":"cluster-shared","version":"0.7.1"}`
	if cluster != "" {
		deps = `{"name":"cluster","version":"` + cluster + `"},` + deps
	}
	return `{"apiVersion":"v2","name":"` + chart + `","version":"` + version + `","dependencies":[` + deps + `]}`
}

// fakeCharts is a fake of the chart registry: "<oci URL> <ref>" → the chart's
// config, counting every read.
type fakeCharts map[string]string

func (f fakeCharts) config(reads map[string]int) ChartConfig {
	return func(_ context.Context, ociURL, ref string) ([]byte, error) {
		key := ociURL + " " + ref
		reads[key]++
		config, ok := f[key]
		if !ok {
			return nil, fmt.Errorf("%s:%s: MANIFEST_UNKNOWN", ociURL, ref)
		}
		return []byte(config), nil
	}
}

var charts = fakeCharts{
	releaseChart + " 36.0.0":            chartConfig("release-aws", "36.0.0", "8.3.1"),
	releaseChart + " 34.0.0":            chartConfig("release-aws", "34.0.0", "5.1.2"),
	releaseChart + " sha256:" + zeroHex: chartConfig("release-aws", "36.2.0", "9.4.0"),
	clusterChart + " 10.3.0":            chartConfig("cluster-aws", "10.3.0", "8.3.0"),
	clusterChart + " 10.2.0":            chartConfig("cluster-aws", "10.2.0", "8.2.0"),
	releaseChart + " 99.0.0":            chartConfig("release-aws", "99.0.0", ""),
}

var zeroHex = strings.Repeat("0", 64)

// readHelmReleaseFact reads maple's fact from manifest, with the chart
// registry holding charts, and answers the fact, the charts read and the
// error.
func readHelmReleaseFact(t *testing.T, manifest string) (bool, map[string]int, error) {
	t.Helper()
	repos := fakeRepos{testMCs: {testManifest: manifest}}
	reads := map[string]int{}
	got, err := readPodCertificateRequest(context.Background(), repos.reader(map[string]int{}), charts.config(reads), mapleInst)
	return got, reads, err
}

// A management cluster deployed by a Flux HelmRelease is read as a cluster App
// is: the values on record — the valuesFrom ConfigMaps in order, spec.values
// on top — enable the gates on every component, or a component sets no list
// and the chart's default decides. The chart is the one the OCIRepository
// points at, read from its registry, and the cluster subchart it depends on
// decides from 8.3.0, whatever the chart is called: no releases repository,
// no table of provider charts. A record that sets every list reads no chart.
func TestPodCertificateRequestFromTheHelmRelease(t *testing.T) {
	all := gates(substrateGates, "")
	tag := func(v string) string { return "tag: " + v + "\n" }
	cases := []struct {
		name     string
		manifest string
		want     bool
		read     string // the chart read, empty for none
	}{
		{"the gates on every component: no chart read", helmReleaseManifest(releaseChart, tag("34.0.0"), values(all, all, all), ""), true, ""},
		{"a list without the gates: no whatever the chart", helmReleaseManifest(releaseChart, tag("36.0.0"), values(gates([]string{otherGate}, ""), "", ""), ""), false, ""},
		{"no list, a release chart on the cluster subchart's default", helmReleaseManifest(releaseChart, tag("36.0.0"), values("", "", ""), ""), true, releaseChart + " 36.0.0"},
		{"no list, a release chart before it", helmReleaseManifest(releaseChart, tag("34.0.0"), values("", "", ""), ""), false, releaseChart + " 34.0.0"},
		{"no list, a provider chart taken directly", helmReleaseManifest(clusterChart, tag("10.3.0"), values("", "", ""), ""), true, clusterChart + " 10.3.0"},
		{"no list, a provider chart before the default", helmReleaseManifest(clusterChart, tag("10.2.0"), values("", "", ""), ""), false, clusterChart + " 10.2.0"},
		{"a chart without the cluster subchart", helmReleaseManifest(releaseChart, tag("99.0.0"), values("", "", ""), ""), false, releaseChart + " 99.0.0"},
		{"a digest wins over the tag", helmReleaseManifest(releaseChart, tag("34.0.0")+"digest: sha256:"+zeroHex+"\n", values("", "", ""), ""), true, releaseChart + " sha256:" + zeroHex},
		{"spec.values replaces a ConfigMap's list", helmReleaseManifest(releaseChart, tag("34.0.0"), values(all, all, all), values(gates([]string{otherGate}, ""), "", "")), false, ""},
		{"spec.values completes the ConfigMap's lists", helmReleaseManifest(releaseChart, tag("34.0.0"), values(all, all, ""), values("", "", all)), true, ""},
	}
	for _, c := range cases {
		got, reads, err := readHelmReleaseFact(t, c.manifest)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		switch {
		case c.read == "" && len(reads) != 0:
			t.Errorf("%s: read %v, wanted no chart read", c.name, reads)
		case c.read != "" && (len(reads) != 1 || reads[c.read] != 1):
			t.Errorf("%s: read %v, want %s once", c.name, reads, c.read)
		}
	}
}

// The values are merged as Flux merges them: valuesFrom in order, the key
// values.yaml where valuesKey names none; a ConfigMap the manifest does not
// hold, a Secret and a targetPath entry are not read. A cluster App on record
// wins over a HelmRelease, and a HelmRelease whose chart does not come from an
// OCIRepository is no cluster on record.
func TestHelmReleaseValuesOnRecord(t *testing.T) {
	all := gates(substrateGates, "")
	base := helmReleaseManifest(releaseChart, "tag: 34.0.0\n", values(all, all, all), "")
	withValuesFrom := func(manifest, entries string) string {
		return strings.Replace(manifest, "  valuesFrom:\n    - kind: ConfigMap\n      name: maple-userconfig\n      valuesKey: values\n", "  valuesFrom:\n    - kind: ConfigMap\n      name: maple-userconfig\n      valuesKey: values\n"+entries, 1)
	}
	configMap := func(name, key, values string) string {
		return "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n  namespace: org-fleet\ndata:\n  " + key + ": |\n" + indent(values, "    ")
	}
	without := values(gates([]string{otherGate}, ""), "", "")
	cases := []struct {
		name     string
		manifest string
		want     bool
	}{
		{"a later ConfigMap replaces the list", withValuesFrom(base, "    - kind: ConfigMap\n      name: maple-override\n") + configMap("maple-override", "values.yaml", without), false},
		{"a later ConfigMap's other key is not read", withValuesFrom(base, "    - kind: ConfigMap\n      name: maple-override\n") + configMap("maple-override", "other", without), true},
		{"a ConfigMap the manifest does not hold", withValuesFrom(base, "    - kind: ConfigMap\n      name: elsewhere\n"), true},
		{"a Secret is not read", withValuesFrom(base, "    - kind: Secret\n      name: maple-override\n"), true},
		{"a targetPath entry is not read", withValuesFrom(base, "    - kind: ConfigMap\n      name: maple-override\n      targetPath: cluster.internal\n") + configMap("maple-override", "values.yaml", without), true},
		{"a cluster App wins over a HelmRelease", base + "---\napiVersion: application.giantswarm.io/v1alpha1\nkind: App\nmetadata:\n  name: maple-app\n  namespace: org-fleet\nspec:\n  name: cluster-aws\n  version: 10.2.0\n  userConfig:\n    configMap:\n      name: maple-userconfig\n", true},
		{"a HelmRelease with a HelmChart template", strings.Replace(base, "kind: OCIRepository\n    name: maple", "kind: HelmChart\n    name: maple", 1), false},
	}
	for _, c := range cases {
		got, reads, err := readHelmReleaseFact(t, c.manifest)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if len(reads) != 0 {
			t.Errorf("%s: read %v", c.name, reads)
		}
	}
}

// A chart that cannot be read is ErrChart: the fact false, the reason naming
// the manifest, its repository and the HelmRelease, the installation still
// readable.
func TestHelmReleaseChartNotRead(t *testing.T) {
	cases := []struct{ name, manifest, names string }{
		{"the OCIRepository is not on record", strings.Replace(helmReleaseManifest(releaseChart, "tag: 36.0.0\n", values("", "", ""), ""), "    name: maple\n  releaseName", "    name: elsewhere\n  releaseName", 1), "OCIRepository org-fleet/elsewhere, which the manifest does not hold"},
		{"a semver range", helmReleaseManifest(releaseChart, "semver: '>=36.0.0'\n", values("", "", ""), ""), `follows the semver range ">=36.0.0"`},
		{"no tag or digest", helmReleaseManifest(releaseChart, "{}\n", values("", "", ""), ""), "names no tag or digest"},
		{"a tag the registry does not have", helmReleaseManifest(releaseChart, "tag: 35.9.9\n", values("", "", ""), ""), "MANIFEST_UNKNOWN"},
	}
	for _, c := range cases {
		got, _, err := readHelmReleaseFact(t, c.manifest)
		if !errors.Is(err, ErrChart) || got {
			t.Errorf("%s: %v, %v; want ErrChart and no", c.name, got, err)
			continue
		}
		for _, want := range []string{testManifest, testMCs, "HelmRelease org-fleet/maple", c.names} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %q does not name %q", c.name, err, want)
			}
		}
	}
}

// RegistryChartConfig reads a chart's config through the distribution API: the
// manifest at the tag or digest, then the config blob it names.
func TestRegistryChartConfig(t *testing.T) {
	config := []byte(chartConfig("release-aws", "36.0.0", "8.3.1"))
	sum := sha256.Sum256(config)
	configDigest := "sha256:" + hex.EncodeToString(sum[:])
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.cncf.helm.config.v1+json","digest":"` + configDigest + `","size":` + fmt.Sprint(len(config)) + `},"layers":[]}`)
	msum := sha256.Sum256(manifest)
	manifestDigest := "sha256:" + hex.EncodeToString(msum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/":
			w.WriteHeader(http.StatusOK)
		case "/v2/charts/release-aws/manifests/36.0.0", "/v2/charts/release-aws/manifests/" + manifestDigest:
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", manifestDigest)
			_, _ = w.Write(manifest)
		case "/v2/charts/release-aws/blobs/" + configDigest:
			_, _ = w.Write(config)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	read := registryChartConfig(srv.Client(), true)
	url := "oci://" + strings.TrimPrefix(srv.URL, "http://") + "/charts/release-aws"
	for _, ref := range []string{"36.0.0", manifestDigest} {
		got, err := read(context.Background(), url, ref)
		if err != nil || string(got) != string(config) {
			t.Errorf("%s: %s, %v", ref, got, err)
		}
	}
	if _, err := read(context.Background(), url, "35.9.9"); err == nil {
		t.Error("a tag the registry does not have is an error")
	}
	if _, err := read(context.Background(), "https://example.com/release-aws", "36.0.0"); err == nil || !strings.Contains(err.Error(), "no oci://") {
		t.Errorf("a URL that is not oci://: %v", err)
	}
}
