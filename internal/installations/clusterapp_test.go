package installations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// clusterAppManifest renders a cluster App manifest as the management-clusters
// repositories keep it: the App of a cluster chart pinned to version and the
// ConfigMap with its values, the ConfigMap first (document order does not
// matter).
func clusterAppManifest(chart, version, values string) string {
	return clusterAppManifestOf(chart, "  version: "+version+"\n", values)
}

// releaseClusterAppManifest renders a release-based cluster App: no chart
// version on the App, the release in the values (global.release.version).
func releaseClusterAppManifest(chart, release, values string) string {
	return clusterAppManifestOf(chart, "", "global:\n  release:\n    version: "+release+"\n"+values)
}

func clusterAppManifestOf(chart, versionLine, values string) string {
	return "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: maple-userconfig\n  namespace: org-fleet\ndata:\n  values: |\n" + indent(values, "    ") +
		"---\napiVersion: application.giantswarm.io/v1alpha1\nkind: App\nmetadata:\n  name: maple\n  namespace: org-fleet\nspec:\n  catalog: cluster\n  name: " + chart + "\n" + versionLine + "  userConfig:\n    configMap:\n      name: maple-userconfig\n      namespace: org-fleet\n"
}

// releaseManifest renders a release.yaml of giantswarm/releases that ships
// chart at version among other components.
func releaseManifest(chart, version string) string {
	return "apiVersion: release.giantswarm.io/v1alpha1\nkind: Release\nmetadata:\n  name: v35.1.0\nspec:\n  components:\n    - name: " + chart + "\n      version: " + version + "\n    - name: kubernetes\n      version: 1.35.8\n  state: active\n"
}

// gates renders a component's featureGates list with the gates named, each
// enabled unless disabled names it.
func gates(names []string, disabled string) string {
	var b strings.Builder
	b.WriteString("featureGates:\n")
	for _, n := range names {
		b.WriteString("  - name: " + n + "\n    enabled: " + boolOf(n != disabled) + "\n    minKubernetesVersion: 1.33.0\n")
	}
	return b.String()
}

func boolOf(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

var substrateGates = []string{"PodCertificateRequest", "ClusterTrustBundle", "ClusterTrustBundleProjection"}

// values renders the cluster App's values with a featureGates list per
// component given (apiServer, controllerManager, kubelet); a component left
// out sets none and runs the chart's default list.
func values(apiServer, controllerManager, kubelet string) string {
	var b strings.Builder
	b.WriteString("cluster:\n  internal:\n    advancedConfiguration:\n")
	if apiServer != "" || controllerManager != "" {
		b.WriteString("      controlPlane:\n")
		if apiServer != "" {
			b.WriteString("        apiServer:\n" + indent(apiServer, "          "))
		}
		if controllerManager != "" {
			b.WriteString("        controllerManager:\n" + indent(controllerManager, "          "))
		}
	}
	if kubelet != "" {
		b.WriteString("      kubelet:\n" + indent(kubelet, "        "))
	}
	return b.String()
}

// files is a fake of the repositories as the person reads them: repository →
// path → content, counting every read.
type fakeRepos map[string]map[string]string

func (f fakeRepos) reader(reads map[string]int) Reader {
	return func(_ context.Context, repository, path string) (string, error) {
		reads[repository+":"+path]++
		content, ok := f[repository][path]
		if !ok {
			return "", gh.ErrNotFound
		}
		return content, nil
	}
}

const (
	testMCs      = "fleet/fleet-management-clusters"
	testManifest = "management-clusters/maple/cluster-app-manifests.yaml"
)

var mapleInst = Installation{Name: "maple", Repositories: Repositories{ManagementClusters: testMCs}}

// readFact reads maple's fact from manifest with the releases repository
// holding releases, and answers the fact, the error and the reads made.
func readFact(t *testing.T, manifest string, releases map[string]string) (bool, error, map[string]int) {
	t.Helper()
	repos := fakeRepos{testMCs: {testManifest: manifest}, ReleasesRepository: releases}
	reads := map[string]int{}
	got, err := readPodCertificateRequest(context.Background(), repos.reader(reads), mapleInst)
	return got, err, reads
}

// The record says the cluster serves PodCertificateRequest when every one of
// the three components enables the three gates in its featureGates list, or
// sets no list and the cluster App's chart enables them by default; a list on
// record replaces the chart's default, so a component whose list lacks a gate
// says no whatever the chart. A chart the table does not know, a manifest
// without a cluster App, or none at all says no.
func TestPodCertificateRequestFromTheClusterApp(t *testing.T) {
	all := gates(substrateGates, "")
	cases := []struct {
		name     string
		manifest string
		want     bool
	}{
		{"the three gates on all three components, an older chart", clusterAppManifest("cluster-aws", "10.2.0", values(all, all, all)), true},
		{"the gates on the control plane only", clusterAppManifest("cluster-aws", "10.2.0", values(all, all, "")), false},
		{"one gate disabled on the kubelet", clusterAppManifest("cluster-aws", "10.2.0", values(all, all, gates(substrateGates, "ClusterTrustBundleProjection"))), false},
		{"a list with other gates only", clusterAppManifest("cluster-aws", "10.2.0", values(all, all, gates([]string{"MutableCSINodeAllocatableCount"}, ""))), false},
		{"no list anywhere, the chart carries them by default", clusterAppManifest("cluster-aws", "10.3.0", values("", "", "")), true},
		{"no list anywhere, a later chart", clusterAppManifest("cluster-azure", "v9.4.1", values("", "", "")), true},
		{"no list anywhere, the chart before the default", clusterAppManifest("cluster-azure", "9.2.0", values("", "", "")), false},
		{"a list without the gates replaces the chart's default", clusterAppManifest("cluster-aws", "10.3.0", values(gates([]string{"MutableCSINodeAllocatableCount"}, ""), "", "")), false},
		{"a list on one component with the gates, the chart's default on the rest", clusterAppManifest("cluster-aws", "10.3.0", values(all, "", "")), true},
		{"a chart the table does not know", clusterAppManifest("cluster-vsphere", "9.2.0", values("", "", "")), false},
		{"a version that is no semantic version", clusterAppManifest("cluster-aws", "latest", values("", "", "")), false},
		{"no cluster App in the manifest", "---\nkind: ConfigMap\nmetadata:\n  name: x\ndata:\n  values: |\n    cluster: {}\n", false},
		{"an empty manifest", "", false},
	}
	for _, c := range cases {
		got, err, reads := readFact(t, c.manifest, nil)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		for k := range reads {
			if strings.HasPrefix(k, ReleasesRepository) {
				t.Errorf("%s: an App pinned to a chart version reads no release: %s", c.name, k)
			}
		}
	}
	if _, err, _ := readFact(t, "---\nkind: App\nspec: [not a map]\n", nil); err == nil {
		t.Error("a manifest that does not decode is an error, not a no")
	}
	repos := fakeRepos{}
	if got, err := readPodCertificateRequest(context.Background(), repos.reader(map[string]int{}), mapleInst); err != nil || got {
		t.Errorf("an installation without the manifest says no: %v, %v", got, err)
	}
	if ClusterAppManifestPath("maple") != "management-clusters/maple/cluster-app-manifests.yaml" {
		t.Errorf("path: %s", ClusterAppManifestPath("maple"))
	}
}

// A release-based cluster App — every CAPA and CAPZ management cluster —
// carries no chart version: the version is the one the release its values
// name lists for the chart in giantswarm/releases, read as the person, and
// the chart table answers for that version. A record that sets every
// component's list is read without the release. A release that cannot be
// read or lists no such chart is ErrRelease: the fact false, the reason to
// report, the installation still readable.
func TestPodCertificateRequestThroughTheRelease(t *testing.T) {
	all := gates(substrateGates, "")
	releases := map[string]string{
		"capa/v35.1.0/release.yaml":  releaseManifest("cluster-aws", "10.3.0"),
		"capa/v35.0.1/release.yaml":  releaseManifest("cluster-aws", "10.0.1"),
		"azure/v35.0.1/release.yaml": releaseManifest("cluster-azure", "9.3.0"),
		"capa/v36.0.0/release.yaml":  releaseManifest("cluster-eks", "7.1.0"),
	}
	cases := []struct {
		name     string
		manifest string
		want     bool
		read     string // the release read, empty for none
	}{
		{"the release ships the chart at the default", releaseClusterAppManifest("cluster-aws", "35.1.0", values("", "", "")), true, "capa/v35.1.0/release.yaml"},
		{"a v-prefixed release", releaseClusterAppManifest("cluster-aws", "v35.1.0", values("", "", "")), true, "capa/v35.1.0/release.yaml"},
		{"the release ships the chart before the default", releaseClusterAppManifest("cluster-aws", "35.0.1", values("", "", "")), false, "capa/v35.0.1/release.yaml"},
		{"another provider's directory", releaseClusterAppManifest("cluster-azure", "35.0.1", values("", "", "")), true, "azure/v35.0.1/release.yaml"},
		{"the gates on every component: the release is not read", releaseClusterAppManifest("cluster-aws", "35.0.1", values(all, all, all)), true, ""},
		{"a list without the gates on one component: no whatever the release", releaseClusterAppManifest("cluster-aws", "35.1.0", values(gates([]string{"MutableCSINodeAllocatableCount"}, ""), "", "")), false, ""},
	}
	for _, c := range cases {
		got, err, reads := readFact(t, c.manifest, releases)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		var releaseReads []string
		for k, n := range reads {
			if strings.HasPrefix(k, ReleasesRepository+":") {
				releaseReads = append(releaseReads, strings.TrimPrefix(k, ReleasesRepository+":"))
				if n != 1 {
					t.Errorf("%s: %s read %d times", c.name, k, n)
				}
			}
		}
		switch {
		case c.read == "" && len(releaseReads) != 0:
			t.Errorf("%s: read %v, wanted no release read", c.name, releaseReads)
		case c.read != "" && (len(releaseReads) != 1 || releaseReads[0] != c.read):
			t.Errorf("%s: read %v, want %s", c.name, releaseReads, c.read)
		}
	}
	for _, c := range []struct{ name, manifest, names string }{
		{"a release not in the repository", releaseClusterAppManifest("cluster-aws", "34.9.9", values("", "", "")), "capa/v34.9.9/release.yaml is not in giantswarm/releases"},
		{"a release that lists no such chart", releaseClusterAppManifest("cluster-aws", "36.0.0", values("", "", "")), "lists no component cluster-aws"},
		{"a chart without a releases directory", releaseClusterAppManifest("cluster-mars", "35.1.0", values("", "", "")), "no releases directory is known for the chart cluster-mars"},
	} {
		got, err, _ := readFact(t, c.manifest, releases)
		if !errors.Is(err, ErrRelease) || got {
			t.Errorf("%s: %v, %v; want ErrRelease and no", c.name, got, err)
			continue
		}
		for _, want := range []string{testManifest, testMCs, c.names} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %q does not name %q", c.name, err, want)
			}
		}
	}
	if p, ok := ReleasePath("cluster-cloud-director", "v31.0.0"); !ok || p != "cloud-director/v31.0.0/release.yaml" {
		t.Errorf("release path: %s, %v", p, ok)
	}
	if _, ok := ReleasePath("cluster-mars", "1.0.0"); ok {
		t.Error("a chart without a releases directory has no release path")
	}
}
