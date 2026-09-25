package verify

import (
	"context"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A reconcile that keeps an id in the kagent UI's oidc-extra-audience and in
// muster's trustedAudiences commits render plus kept entries; the two drift
// probes — the flag's place and the HelmRelease's whole values — read the
// live objects that carry that value as defined, and a live object that
// lacks the kept id as drifted. Against the bare render the same live
// objects would read drifted.
func TestLiveProbesHoldTheKeptEntries(t *testing.T) {
	const (
		file         = "management-clusters/x/apps/agent-platform/" + configMapPatch
		audA         = "audience-a"
		audB         = "audience-b"
		rendered     = audA + "," + audB
		argsPlace    = "spec.template.spec.containers[0].args[args]"
		podTemplate  = "template"
		kagentValues = "kagent"
		keptID       = "kept-audience"
		committed    = rendered + "," + keptID
		trusted      = plan.ListTrustedAudiences
		proxy        = "oauth2-proxy"
	)
	bare := func() map[string]string {
		return map[string]string{plan.ListExtraAudience: rendered, trusted + "[" + audA + "]": audA, trusted + "[" + audB + "]": audB}
	}
	kept := []plan.Kept{{List: plan.ListExtraAudience, Entry: keptID}, {List: trusted, Entry: keptID}, {List: plan.ListAllowedCallers, Entry: "a-caller"}}
	withKeptValues := bare()
	withKept(withKeptValues, kept, file)
	if withKeptValues[plan.ListExtraAudience] != committed || withKeptValues[trusted+"["+keptID+"]"] != keptID {
		t.Fatalf("the render with the kept entries: %v", withKeptValues)
	}
	if _, ok := withKeptValues[plan.ListAllowedCallers+"[a-caller]"]; ok {
		t.Fatalf("a list the render lacks gained a kept entry: %v", withKeptValues)
	}
	withKept(withKeptValues, kept, file)
	if withKeptValues[plan.ListExtraAudience] != committed {
		t.Fatalf("a kept id was repeated: %v", withKeptValues)
	}

	live := func(extra string, audiences []any) *recordingCluster {
		values := map[string]any{
			kagentValues: map[string]any{proxy: map[string]any{"extraArgs": map[string]any{"oidc-extra-audience": extra}}},
			clientMuster: map[string]any{clientMuster: map[string]any{"oauth": map[string]any{"server": map[string]any{"trustedAudiences": audiences}}}},
		}
		return &recordingCluster{objects: map[string]map[string]any{
			kindHelmRelease + "/" + testNamespace + "/" + testRelease: {keySpec: map[string]any{valuesKey: values}},
			"Deployment/" + testNamespace + "/" + proxy: {keySpec: map[string]any{podTemplate: map[string]any{keySpec: map[string]any{"containers": []any{
				map[string]any{"args": []any{"--provider=oidc", "--oidc-extra-audience=" + extra}}}}}}},
		}}
	}
	whole := render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: kindHelmRelease, Name: testRelease}
	flag := render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: "Deployment", Name: proxy,
		Expect: render.Expectation{Compare: []render.Comparison{{Live: argsPlace, Prefix: "--oidc-extra-audience=", Rendered: plan.ListExtraAudience}}}}
	asCommitted := live(committed, []any{audA, audB, keptID})
	withoutKept := live(rendered, []any{audA, audB})
	for _, c := range []struct {
		name    string
		values  map[string]string
		cluster *recordingCluster
		mark    Mark
	}{
		{"the committed value, render and kept", withKeptValues, asCommitted, AsDefined},
		{"the kept id missing live", withKeptValues, withoutKept, Drifted},
		{"the committed value against the bare render", bare(), asCommitted, Drifted},
	} {
		for _, p := range []render.Probe{whole, flag} {
			x := &executor{opts: LiveOptions{Cluster: c.cluster}, lv: &liveRender{values: c.values}}
			check, diffs, _ := x.run(context.Background(), p)
			if check.Mark != c.mark {
				t.Errorf("%s, %s: %q %q %+v, want %q", c.name, p.Resource, check.Mark, check.Message, diffs, c.mark)
			}
		}
	}
}
