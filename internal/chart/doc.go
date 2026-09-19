// Package chart holds the render proofs of the Helm chart in
// helm/giantswarm-platform-manager: the chart rendered with helm over the
// values files in tests/, read back as objects. The proofs run where helm is
// on PATH (make test-chart; CI's render-consumption job) and are skipped
// elsewhere.
package chart
