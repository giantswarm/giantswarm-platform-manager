package installations

import (
	"strings"
	"testing"
)

// clusterAppManifest renders a cluster App manifest as the management-clusters
// repositories keep it: the App of a cluster chart and the ConfigMap with its
// values, the ConfigMap first (document order does not matter).
func clusterAppManifest(chart, version, values string) string {
	return "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: maple-userconfig\n  namespace: org-fleet\ndata:\n  values: |\n" + indent(values, "    ") +
		"---\napiVersion: application.giantswarm.io/v1alpha1\nkind: App\nmetadata:\n  name: maple\n  namespace: org-fleet\nspec:\n  catalog: cluster\n  name: " + chart + "\n  version: " + version + "\n  userConfig:\n    configMap:\n      name: maple-userconfig\n      namespace: org-fleet\n"
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
	b.WriteString("global:\n  metadata:\n    name: maple\ncluster:\n  internal:\n    advancedConfiguration:\n")
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
		got, err := podCertificateRequest(c.manifest)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := podCertificateRequest("---\nkind: App\nspec: [not a map]\n"); err == nil {
		t.Error("a manifest that does not decode is an error, not a no")
	}
	if ClusterAppManifestPath("maple") != "management-clusters/maple/cluster-app-manifests.yaml" {
		t.Errorf("path: %s", ClusterAppManifestPath("maple"))
	}
}
