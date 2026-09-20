package agentplatform

import (
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Agent Substrate — the kagent runtime of the meta chart's 4 line (4.49.0 and
// later) — issues each agent's pod certificates through
// certificates.k8s.io/v1beta1 PodCertificateRequest and distributes its CA
// through ClusterTrustBundle projected volumes. Both are beta and off by
// default: the management cluster needs the three feature gates on the API
// server, the controller manager and every kubelet, and the meta chart refuses
// its install at render time where the API is not served. The record says
// whether the cluster has them (installation.podCertificateRequest, derived
// from the cluster App on record); a 4-line record without them is refused at
// plan time (checkRecord), and the live probe reads the API (probes).

// PodCertificateRequestAPI is the API Agent Substrate needs served, as the
// probe discovers it: the resource and its group, and the version.
const (
	PodCertificateRequestResource = "podcertificaterequests.certificates.k8s.io"
	PodCertificateRequestVersion  = "v1beta1"
)

// PodCertificateRequestGates are the feature gates the cluster needs enabled
// for the API and its pod certificates: the request API itself (Kubernetes
// 1.35 and later) and the trust bundle with its projection (1.33 and later).
var PodCertificateRequestGates = []string{"PodCertificateRequest", "ClusterTrustBundle", "ClusterTrustBundleProjection"}

// PodCertificateRequestComponents are the kubeadm components the gates are
// set on, as the cluster chart's values name them under
// cluster.internal.advancedConfiguration (each carries a featureGates list of
// {name, enabled, minKubernetesVersion}).
var PodCertificateRequestComponents = []string{"controlPlane.apiServer", "controlPlane.controllerManager", "kubelet"}

// ClusterChartsWithPodCertificateRequest is, per provider cluster chart, the
// first version whose kubeadm configuration enables the three gates by
// default: the one that ships giantswarm/cluster 8.3.0 (cluster#1005, which
// turned them on for every cluster at or above the gates' Kubernetes floors).
// A chart not listed here — cluster-vsphere, whose main pins 8.3.0 without a
// release of it yet; cluster-eks, which pins the 7 line — carries the gates
// only where the record sets them.
var ClusterChartsWithPodCertificateRequest = map[string]string{
	"cluster-aws":            "10.3.0",
	"cluster-azure":          "9.3.0",
	"cluster-cloud-director": "7.3.0",
}

// ClusterChartHasPodCertificateRequest says whether a cluster App on that
// chart, at that version, enables the gates by default. False for a chart not
// listed and for a version that is not a semantic version.
func ClusterChartHasPodCertificateRequest(chart, version string) bool {
	first, ok := ClusterChartsWithPodCertificateRequest[chart]
	if !ok {
		return false
	}
	v, err := semver.NewVersion(strings.TrimPrefix(version, "v"))
	if err != nil {
		return false
	}
	return !v.LessThan(semver.MustParse(first))
}

// podCertificateRequestDefaults names the charts that carry the gates by
// default, for a refusal: "cluster-aws 10.3.0, cluster-azure 9.3.0, …".
func podCertificateRequestDefaults() string {
	charts := make([]string, 0, len(ClusterChartsWithPodCertificateRequest))
	for chart := range ClusterChartsWithPodCertificateRequest {
		charts = append(charts, chart)
	}
	slices.Sort(charts)
	for i, chart := range charts {
		charts[i] = chart + " " + ClusterChartsWithPodCertificateRequest[chart]
	}
	return strings.Join(charts, ", ")
}
