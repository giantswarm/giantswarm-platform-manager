package plan

import (
	"reflect"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The fixtures of the object list.
const (
	fluxNS     = "flux-giantswarm"
	platformNS = "agent-platform"
	secretKind = "Secret"
)

// The objects of a render: the HelmReleases the probes name first, then
// every manifest among the files by kind, namespace and name — a
// kustomization, a values patch and a file that is no YAML document
// contribute none, and an object rendered twice is listed once.
func TestObjectsReadTheManifestsAndTheReleases(t *testing.T) {
	res := &render.Result{Probes: []render.Probe{
		{Kind: render.HelmReleaseReady, Namespace: fluxNS, Resource: helmReleaseKind, Name: "muster"},
		{Kind: render.HelmReleaseReady, Namespace: fluxNS, Resource: helmReleaseKind, Name: installations.AgentPlatform},
		{Kind: render.ResourcePresent, Namespace: "kagent", Resource: secretKind, Name: "chart-owned"},
	}}
	repo := render.Repository("giantswarm/acme-management-clusters")
	res.Add(repo, "extras/agent-platform/secrets/b.yaml", render.Secret("b-credentials", platformNS, nil, render.ValueKey("k", "v")))
	res.Add(repo, "extras/agent-platform/secrets/a.yaml", render.Secret("a-credentials", platformNS, nil, render.ValueKey("k", "v")))
	res.Add(repo, "extras/agent-platform/kustomization.yaml", render.File{Content: []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./secrets\n")})
	res.Add(repo, "extras/tunnelport/all.yaml", render.File{Content: []byte("# header\napiVersion: source.toolkit.fluxcd.io/v1\nkind: OCIRepository\nmetadata:\n  name: tunnelport\n  namespace: flux-giantswarm\n---\napiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: tunnelport\n  namespace: flux-giantswarm\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: a-credentials\n  namespace: agent-platform\n")})
	res.Add(render.Repository("giantswarm/acme-configs"), "installations/x/apps/agent-platform/configmap-values.yaml.patch", render.File{Content: []byte("configmap:\n  global: {}\n")})
	res.Add(repo, "extras/backstage/values.yaml", render.File{Content: []byte("not: [valid")})
	want := []Object{
		{Kind: helmReleaseKind, Namespace: fluxNS, Name: installations.AgentPlatform},
		{Kind: helmReleaseKind, Namespace: fluxNS, Name: "muster"},
		{Kind: helmReleaseKind, Namespace: fluxNS, Name: "tunnelport"},
		{Kind: "OCIRepository", Namespace: fluxNS, Name: "tunnelport"},
		{Kind: secretKind, Namespace: platformNS, Name: "a-credentials"},
		{Kind: secretKind, Namespace: platformNS, Name: "b-credentials"},
	}
	if got := Objects(res); !reflect.DeepEqual(got, want) {
		t.Errorf("objects:\n got %+v\nwant %+v", got, want)
	}
}
