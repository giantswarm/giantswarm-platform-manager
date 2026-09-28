package plan

import (
	"slices"
	"strings"
	"testing"
)

const smTree = "giantswarm/acme-management-clusters/management-clusters/jackal/"

// The summary reads a line per component: a component created whole as new,
// versions old → new, list entries added and removed, values keys by path
// (inside a values document held in a string too), a Secret's keys added and
// removed without a value, and the credentials that rotate — the ones asked
// for marked, the credentials revision left out.
func TestSummaryReadsEachComponentsChange(t *testing.T) {
	p := Installation{Name: "jackal", Files: []File{
		{Path: smTree + "extras/mcp-kubernetes/kustomization.yaml", Change: ChangeUpdate,
			Current: "kind: Kustomization\nresources:\n  - https://github.com/giantswarm/management-cluster-bases//extras/mcp-kubernetes?ref=v1\n  - oauth.enc.yaml\n",
			Content: "kind: Kustomization\nresources:\n  - https://github.com/giantswarm/management-cluster-bases//extras/mcp-kubernetes?ref=v2\n  - oauth.enc.yaml\n  - revision.enc.yaml\n"},
		{Path: smTree + "extras/mcp-kubernetes/release.yaml", Change: ChangeUpdate,
			Current: "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: mcp-kubernetes\nspec:\n  chart:\n    spec:\n      version: 1.8.1\n  values:\n    oauth:\n      trustedAudiences: [a]\n    replicas: 1\n",
			Content: "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: mcp-kubernetes\nspec:\n  chart:\n    spec:\n      version: 1.8.2\n  values:\n    oauth:\n      trustedAudiences: [a, b]\n    replicas: 2\n"},
		{Path: smTree + "extras/mcp-kubernetes/oauth.enc.yaml", Change: ChangeUpdate,
			Current: "kind: Secret\nmetadata:\n  name: oauth\nstringData:\n  DEX_CLIENT_SECRET: ENC[redacted]\n  OLD: ENC[redacted]\n",
			Content: "kind: Secret\nmetadata:\n  name: oauth\nstringData:\n  DEX_CLIENT_SECRET: GENERATED(jackal-mcp-kubernetes-dex-client-secret)\n  VALKEY_PASSWORD: s3cr3t-value\n"},
		{Path: smTree + "extras/mcp-kubernetes/revision.enc.yaml", Change: ChangeUnchanged},
		{Path: "giantswarm/acme-configs/installations/jackal/apps/dex-app/configmap-values.yaml.patch", Change: ChangeUpdate,
			Current: "kind: ConfigMap\nmetadata:\n  name: dex-app-user-values\ndata:\n  values: |\n    oidc:\n      staticClients:\n        mcpKubernetes:\n          id: a\n",
			Content: "kind: ConfigMap\nmetadata:\n  name: dex-app-user-values\ndata:\n  values: |\n    oidc:\n      staticClients:\n        mcpKubernetes:\n          id: b\n"},
		{Path: smTree + "extras/mcp-capi/kustomization.yaml", Change: ChangeCreate, Content: "kind: Kustomization\n"},
		{Path: smTree + "extras/mcp-capi/oauth.enc.yaml", Change: ChangeCreate, Content: "kind: Secret\n"},
	}, GeneratedSecrets: []GeneratedSecret{
		{Name: "jackal-mcp-kubernetes-valkey-password", Files: []string{smTree + "extras/mcp-kubernetes/oauth.enc.yaml"}, Rotates: true, ForcedBy: ForcedByRequest},
		{Name: "jackal-mcp-kubernetes-credentials-revision", Files: []string{smTree + "extras/mcp-kubernetes/revision.enc.yaml"}, Rotates: true, ForcedBy: ForcedByRequest},
		{Name: "jackal-mcp-kubernetes-dex-client-secret", Files: []string{smTree + "extras/mcp-kubernetes/oauth.enc.yaml"}, Rotates: true, ForcedBy: "a file"},
		{Name: "jackal-mcp-capi-dex-client-secret", Files: []string{smTree + "extras/mcp-capi/oauth.enc.yaml"}},
	}}
	got := Summary(p)
	want := []string{
		"mcp-kubernetes: resources +extras/mcp-kubernetes?ref=v2 +revision.enc.yaml −extras/mcp-kubernetes?ref=v1; " +
			"HelmRelease mcp-kubernetes 1.8.1 → 1.8.2; values.oauth.trustedAudiences +b; values spec.values.replicas; " +
			"adds key VALKEY_PASSWORD; drops key OLD; rotates valkey-password (on request), dex-client-secret",
		"dex-app: values data.values.oidc.staticClients",
		"mcp-capi: new",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, l := range got {
		if strings.Contains(l, "s3cr3t") || strings.Contains(l, "GENERATED(") || strings.Contains(l, "ENC[") {
			t.Fatalf("a value in the summary: %s", l)
		}
	}
}

// An unchanged plan reads nothing; a file that does not parse reads rewritten.
func TestSummaryOfNothingAndOfTheUnreadable(t *testing.T) {
	if got := Summary(Installation{Files: []File{{Path: "a/extras/x/k.yaml", Change: ChangeUnchanged}}}); len(got) != 0 {
		t.Fatalf("unchanged: %v", got)
	}
	got := Summary(Installation{Files: []File{{Path: "a/extras/x/k.yaml", Change: ChangeUpdate, Current: "a: [", Content: "a: 1"}}})
	if !slices.Equal(got, []string{"x: k.yaml rewritten"}) {
		t.Fatalf("unreadable: %v", got)
	}
}

// A kustomization's patches read by their target: a patch added with its
// operations, one removed, one whose JSON patch gains, loses or changes an
// operation (op and path, never the value), a strategic merge patch changed.
func TestSummaryNamesAPatchByItsTarget(t *testing.T) {
	patch := func(kind, name, body string) string {
		return "  - patch: |-\n" + indent(body, "      ") + "    target:\n      kind: " + kind + "\n      name: " + name + "\n"
	}
	checksum := "- op: add\n  path: /spec/valuesFrom/-\n  value:\n    kind: Secret\n    name: rev\n"
	current := "kind: Kustomization\npatches:\n" +
		patch("HelmRelease", "mcp-kubernetes", checksum) +
		patch("HelmRelease", "mcp-kubernetes-valkey", "- op: replace\n  path: /spec/suspend\n  value: false\n") +
		patch("Deployment", "old", "- op: remove\n  path: /spec/replicas\n") +
		patch("ConfigMap", "smp", "apiVersion: v1\nkind: ConfigMap\ndata:\n  a: '1'\n")
	content := "kind: Kustomization\npatches:\n" +
		patch("HelmRelease", "mcp-kubernetes", checksum+"- op: replace\n  path: /spec/interval\n  value: 5m\n") +
		patch("HelmRelease", "mcp-kubernetes-valkey", "- op: replace\n  path: /spec/suspend\n  value: true\n") +
		patch("ConfigMap", "smp", "apiVersion: v1\nkind: ConfigMap\ndata:\n  a: '2'\n") +
		patch("Secret", "new", "- op: add\n  path: /metadata/labels/x\n  value: y\n")
	got := Summary(Installation{Name: "jackal", Files: []File{{Path: smTree + "extras/mcp-kubernetes/kustomization.yaml", Change: ChangeUpdate, Current: current, Content: content}}})
	want := []string{"mcp-kubernetes: patch ConfigMap smp changed; removes patch Deployment old; " +
		"patch HelmRelease mcp-kubernetes +replace /spec/interval; patch HelmRelease mcp-kubernetes-valkey ~replace /spec/suspend; " +
		"adds patch Secret new (add /metadata/labels/x)"}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, l := range got {
		if strings.Contains(l, "5m") || strings.Contains(l, "true") {
			t.Fatalf("a value in the summary: %s", l)
		}
	}
}

func indent(s, prefix string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		b.WriteString(prefix + l + "\n")
	}
	return b.String()
}
