package verify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A live path walks maps, greedy over keys that contain dots, indexes lists,
// crosses into a YAML document stored in a string field, and picks an
// argument by its prefix.
func TestResolveWalksLiveObjects(t *testing.T) {
	obj := map[string]any{
		"data": map[string]any{"config.yaml": "aggregator:\n  oauth:\n    server:\n      trustedAudiences: [a, b]\n      dex: {clientId: muster-x}\n"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{
			map[string]any{"args": []any{"--provider=oidc", "--oidc-extra-audience=x,y"}},
		}}}, "values": map[string]any{"providers": map[string]any{"anthropic": map[string]any{"apiKeySecretRef": "k"}}}},
	}
	cases := []struct {
		path, prefix string
		want         any
		ok           bool
	}{
		{"data.config.yaml:aggregator.oauth.server.dex.clientId", "", "muster-x", true},
		{"data.config.yaml:aggregator.oauth.server.trustedAudiences", "", []any{"a", "b"}, true},
		{"spec.template.spec.containers[0].args[args]", "--oidc-extra-audience=", "x,y", true},
		{"spec.template.spec.containers[0].args[args]", "--missing=", nil, false},
		{"spec.values.providers.anthropic.apiKeySecretRef", "", "k", true},
		{"spec.values.nothing", "", nil, false},
		{"data.config.yaml:aggregator.nothing", "", nil, false},
		{"spec.template.spec.containers[3]", "", nil, false},
	}
	for _, c := range cases {
		got, ok := resolve(obj, c.path, c.prefix)
		if ok != c.ok {
			t.Errorf("%s: ok %v, want %v (%v)", c.path, ok, c.ok, got)
			continue
		}
		if !ok {
			continue
		}
		flatGot, flatWant := map[string]string{}, map[string]string{}
		flatten(got, "", flatGot)
		flatten(c.want, "", flatWant)
		if len(diffPaths(flatWant, flatGot)) > 0 {
			t.Errorf("%s: got %v, want %v", c.path, got, c.want)
		}
	}
}

func TestSubtreeIsTheLeavesUnderAPath(t *testing.T) {
	flat := map[string]string{"a.b": "1", "a.c[0]": "2", "a.c[1]": "3", "ab": "x", "d": "4"}
	got := subtree(flat, "a")
	if len(got) != 3 || got["b"] != "1" || got["c[0]"] != "2" || got["c[1]"] != "3" {
		t.Errorf("subtree a: %v", got)
	}
	if got := subtree(flat, "d"); len(got) != 1 || got[""] != "4" {
		t.Errorf("subtree d: %v", got)
	}
	if got := subtree(flat, "zz"); len(got) != 0 {
		t.Errorf("subtree zz: %v", got)
	}
}

// Merge takes, per dimension, the side that checked it; a live dimension is
// the live side's whatever it says; drifted and waiting for the customer
// come from either side.
func TestMergeTakesTheCheckedSide(t *testing.T) {
	repo := Result{State: installations.StateEnabled, Caller: "alice", Features: []Feature{{ID: "f", Dimensions: []Dimension{
		{ID: "file", Kind: definitions.KindConfigMap, Mark: AsDefined},
		{ID: "live-a", Kind: definitions.KindLive, Mark: NotChecked, Reason: ReasonAuthority},
		{ID: "live-b", Kind: definitions.KindLive, Mark: NotChecked, Reason: ReasonAuthority},
		{ID: "probe", Kind: definitions.KindProbe, Mark: Drifted},
	}}}}
	live := Result{State: installations.StateWaitingForCustomer, Caller: "alice@example.test", Features: []Feature{{ID: "f", Dimensions: []Dimension{
		{ID: "file", Kind: definitions.KindConfigMap, Mark: NotChecked, Reason: ReasonRepositorySide},
		{ID: "live-a", Kind: definitions.KindLive, Mark: Drifted},
		{ID: "live-b", Kind: definitions.KindLive, Mark: NotChecked, Reason: ReasonNoProbe},
		{ID: "probe", Kind: definitions.KindProbe, Mark: NotChecked, Reason: ReasonRepositorySide},
	}}}}
	out := Merge(repo, live)
	dims := map[string]Dimension{}
	for _, d := range out.Features[0].Dimensions {
		dims[d.ID] = d
	}
	if dims["file"].Mark != AsDefined || dims["probe"].Mark != Drifted {
		t.Errorf("the repository side's checked dimensions stand: %+v %+v", dims["file"], dims["probe"])
	}
	if dims["live-a"].Mark != Drifted || dims["live-b"].Reason != ReasonNoProbe {
		t.Errorf("the live side's word on live dimensions: %+v %+v", dims["live-a"], dims["live-b"])
	}
	if out.Features[0].Mark != Drifted || out.Summary[Drifted] != 2 || out.Summary[NotChecked] != 1 {
		t.Errorf("roll-up %q summary %v", out.Features[0].Mark, out.Summary)
	}
	if out.State != installations.StateWaitingForCustomer || out.Caller != "alice" || out.LiveCaller != "alice@example.test" {
		t.Errorf("state %q caller %q live caller %q", out.State, out.Caller, out.LiveCaller)
	}
	live.State = installations.StateDrifted
	repo.State = installations.StateWaitingForCustomer
	if out := Merge(repo, live); out.State != installations.StateDrifted {
		t.Errorf("drifted wins over waiting: %q", out.State)
	}
}

func TestDeepMergeReplacesEverythingButMaps(t *testing.T) {
	base := map[string]any{"a": map[string]any{"x": 1, "y": []any{1}}, "b": "old"}
	deepMerge(base, map[string]any{"a": map[string]any{"y": []any{2, 3}, "z": true}, "b": map[string]any{"n": 1}})
	flat := map[string]string{}
	flatten(base, "", flat)
	want := map[string]string{"a.x": "1", "a.y[2]": "2", "a.y[3]": "3", "a.z": "true", "b.n": "1"}
	if d := diffPaths(want, flat); len(d) > 0 {
		t.Errorf("merged %v, differs at %v", flat, d)
	}
}

// discoveryCluster is a Cluster that serves what it is told and reads nothing else.
type discoveryCluster struct {
	served map[string]bool // group/version resource
	err    error
}

func (c discoveryCluster) Get(context.Context, string, string, string, Shape) (map[string]any, error) {
	return nil, errors.New("not read")
}
func (c discoveryCluster) List(context.Context, string, string, string, Shape) ([]map[string]any, error) {
	return nil, errors.New("not read")
}
func (c discoveryCluster) Logs(context.Context, string, string, int) (string, error) {
	return "", errors.New("not read")
}
func (c discoveryCluster) Serves(_ context.Context, group, version, resource string) (bool, error) {
	return c.served[group+"/"+version+" "+resource], c.err
}

// An APIServed probe reads discovery: the API served is as defined; not
// served, the check is red and reads rolling — the record enables what the
// apiserver has not caught up with; a refusal of the read is not checked
// with the reason, as any probe's.
func TestAPIServedProbeReadsRolling(t *testing.T) {
	probe := render.Probe{ID: "live-api", Feature: "runtime", Kind: render.APIServed, Resource: "podcertificaterequests.certificates.k8s.io", Expect: render.Expectation{Version: "v1beta1", Note: "the note"}}
	for _, c := range []struct {
		name    string
		cluster discoveryCluster
		mark    Mark
		message string
	}{
		{"served", discoveryCluster{served: map[string]bool{"certificates.k8s.io/v1beta1 podcertificaterequests": true}}, AsDefined, "served: certificates.k8s.io/v1beta1 podcertificaterequests"},
		{"another version", discoveryCluster{served: map[string]bool{"certificates.k8s.io/v1 podcertificaterequests": true}}, Drifted, Rolling + "certificates.k8s.io/v1beta1 podcertificaterequests is not served yet"},
		{"not served", discoveryCluster{}, Drifted, Rolling + "certificates.k8s.io/v1beta1 podcertificaterequests is not served yet"},
		{"forbidden", discoveryCluster{err: &Forbidden{Person: "p", Reason: "no"}}, NotChecked, "forbidden for p: no"},
	} {
		x := &executor{opts: LiveOptions{Cluster: c.cluster}}
		check, diffs, auth := x.run(context.Background(), probe)
		if check.Mark != c.mark || check.Message != c.message || check.Note != "the note" || check.Kind != string(render.APIServed) || check.Resource != probe.Resource || len(diffs) != 0 || auth != nil {
			t.Errorf("%s: %+v (diffs %d, auth %v)", c.name, check, len(diffs), auth)
		}
	}
}

// An HTTP probe that expects any of several statuses (Dex's login page or
// its redirect to the one connector) is as defined on each of them and
// drifted on another.
func TestHTTPProbeAcceptsAnyOfTheStatuses(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "https://dex.example/auth/local")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	probe := render.Probe{ID: "live-dex-auth-per-client", Kind: render.HTTP, URL: srv.URL + "/auth", Expect: render.Expectation{Statuses: []int{200, 302}}}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, c := range []struct {
		status  int
		mark    Mark
		message string
	}{
		{http.StatusOK, AsDefined, "200"},
		{http.StatusFound, AsDefined, "302"},
		{http.StatusBadRequest, Drifted, "400, expected one of [200 302]"},
	} {
		status = c.status
		check := liveHTTP(client, probe)
		if check.Mark != c.mark || check.Message != c.message {
			t.Errorf("%d: %q %q, want %q %q", c.status, check.Mark, check.Message, c.mark, c.message)
		}
	}
}

// The names and keys of the recording cluster's objects.
const (
	testNamespace   = "ns"
	kindHelmRelease = "HelmRelease"
	kindDeployment  = "Deployment"
	keySpec         = "spec"
	keyStatus       = "status"
	keyData         = "data"
	conditionTrue   = "True"
	proxyWorkload   = "proxy"
	metaRelease     = "agent-platform"
	renderedLeaf    = "kagent.replicas"
	definitionNote  = "what a match means"
)

// recordingCluster serves the objects, the pods and the log it holds and
// records how much each read asked for: the shape of every Get and List,
// the tail of every Logs.
type recordingCluster struct {
	objects map[string]map[string]any // resource/namespace/name
	pods    []map[string]any
	log     string
	err     error
	shapes  []Shape
	tails   []int
}

func (c *recordingCluster) Get(_ context.Context, namespace, resource, name string, shape Shape) (map[string]any, error) {
	c.shapes = append(c.shapes, shape)
	if c.err != nil {
		return nil, c.err
	}
	obj, ok := c.objects[resource+"/"+namespace+"/"+name]
	if !ok {
		return nil, ErrNotFound
	}
	return obj, nil
}

func (c *recordingCluster) List(_ context.Context, _, _, _ string, shape Shape) ([]map[string]any, error) {
	c.shapes = append(c.shapes, shape)
	return c.pods, c.err
}

func (c *recordingCluster) Logs(_ context.Context, _, _ string, tail int) (string, error) {
	c.tails = append(c.tails, tail)
	return c.log, c.err
}

func (c *recordingCluster) Serves(context.Context, string, string, string) (bool, error) {
	return false, errors.New("not read")
}

// Every check asks for what it reads, so the answer stays within what
// mcp-kubernetes relays: a readiness check for the object's Readiness shape
// — the conditions and the revision are in it — a drift probe for its
// Configuration, the ConfigMaps a HelmRelease reads its values from too, and
// an absence check for the last LogTail lines of each pod's log, which its
// note says next to the definition's sentence.
func TestChecksAskForWhatTheyRead(t *testing.T) {
	conditions := func(condition string) map[string]any {
		return map[string]any{"conditions": []any{map[string]any{"type": condition, keyStatus: conditionTrue, "message": "ok"}}}
	}
	key := func(resource, name string) string { return resource + "/" + testNamespace + "/" + name }
	cluster := &recordingCluster{
		objects: map[string]map[string]any{
			key(kindHelmRelease, "rel"): {keySpec: map[string]any{"valuesFrom": []any{map[string]any{"kind": "ConfigMap", "name": valuesKey}}, valuesKey: map[string]any{"kagent": map[string]any{"replicas": "2"}}},
				keyStatus: map[string]any{"conditions": conditions("Ready")["conditions"], "lastAppliedRevision": "1.2.3"}},
			key("ConfigMap", valuesKey):        {keyData: map[string]any{"values.yaml": "kagent:\n  replicas: \"2\"\n", "x": "2"}},
			key(kindDeployment, proxyWorkload): {keySpec: map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": proxyWorkload}}}, keyStatus: conditions("Available")},
			key("Secret", "credential"):        {keyData: map[string]any{"k": "***"}},
		},
		pods: []map[string]any{{"metadata": map[string]any{"name": proxyWorkload + "-0"}, keyStatus: map[string]any{"phase": "Running"}}},
		log:  "level=info msg=\"listening\"\n",
	}
	x := &executor{opts: LiveOptions{Cluster: cluster}, lv: &liveRender{values: map[string]string{renderedLeaf: "2"}}}
	for _, c := range []struct {
		name   string
		probe  render.Probe
		shapes []Shape
		tails  []int
		note   string
	}{
		{"HelmReleaseReady", render.Probe{Kind: render.HelmReleaseReady, Namespace: testNamespace, Resource: kindHelmRelease, Name: "rel"}, []Shape{Readiness}, nil, ""},
		{"Condition", render.Probe{Kind: render.Condition, Namespace: testNamespace, Resource: kindDeployment, Name: proxyWorkload, Expect: render.Expectation{Condition: "Available", ConditionStatus: conditionTrue}}, []Shape{Readiness}, nil, ""},
		{"ResourcePresent", render.Probe{Kind: render.ResourcePresent, Namespace: testNamespace, Resource: "Secret", Name: "credential", Expect: render.Expectation{Keys: []string{"k"}}}, []Shape{Readiness}, nil, ""},
		{"PodsRunning", render.Probe{Kind: render.PodsRunning, Namespace: testNamespace, Name: "app=" + proxyWorkload}, []Shape{Readiness}, nil, ""},
		{"LogAbsent", render.Probe{Kind: render.LogAbsent, Namespace: testNamespace, Resource: kindDeployment, Name: proxyWorkload, Expect: render.Expectation{Absent: "does not match"}}, []Shape{Readiness, Readiness}, []int{LogTail}, logTailNote},
		{"LogAbsent with the definition's note", render.Probe{Kind: render.LogAbsent, Namespace: testNamespace, Resource: kindDeployment, Name: proxyWorkload, Expect: render.Expectation{Absent: "does not match", Note: definitionNote}}, []Shape{Readiness, Readiness}, []int{LogTail}, definitionNote + "; " + logTailNote},
		{"Drift of a HelmRelease's values", render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: kindHelmRelease, Name: "rel"}, []Shape{Configuration, Configuration}, nil, ""},
		{"Drift of a place", render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: "ConfigMap", Name: valuesKey, Expect: render.Expectation{Compare: []render.Comparison{{Live: "data.x", Rendered: renderedLeaf}}}}, []Shape{Configuration}, nil, ""},
	} {
		cluster.shapes, cluster.tails = nil, nil
		check, diffs, auth := x.run(context.Background(), c.probe)
		if check.Mark != AsDefined || len(diffs) != 0 || auth != nil {
			t.Errorf("%s: %+v (diffs %d, auth %v)", c.name, check, len(diffs), auth)
		}
		if !reflect.DeepEqual(cluster.shapes, c.shapes) || !reflect.DeepEqual(cluster.tails, c.tails) {
			t.Errorf("%s asked for %v and %v lines, want %v and %v", c.name, cluster.shapes, cluster.tails, c.shapes, c.tails)
		}
		if check.Note != c.note {
			t.Errorf("%s: note %q, want %q", c.name, check.Note, c.note)
		}
		if c.probe.Kind == render.HelmReleaseReady && check.Revision != "1.2.3" {
			t.Errorf("%s: revision %q from the readiness shape", c.name, check.Revision)
		}
		if c.probe.Kind == render.LogAbsent && !strings.Contains(check.Message, "the last 200 lines") {
			t.Errorf("%s: %q", c.name, check.Message)
		}
	}
}

// A read mcp-kubernetes refuses to answer whole is not checked with the
// refusal in plain words — the sizes, whose limit it is — never the tool's
// JSON; the object stays named by the check.
func TestTooLargeReadsNotCheckedInPlainWords(t *testing.T) {
	cluster := &recordingCluster{err: &TooLarge{Bytes: 139264, Limit: 131072}}
	x := &executor{opts: LiveOptions{Cluster: cluster}}
	check, diffs, auth := x.run(context.Background(), render.Probe{Kind: render.HelmReleaseReady, Namespace: "flux-giantswarm", Resource: kindHelmRelease, Name: metaRelease})
	const want = "the object or log is larger than mcp-kubernetes answers (136 KiB, the limit is 128 KiB): the check asks for too much"
	if check.Mark != NotChecked || check.Message != want || check.Name != metaRelease || len(diffs) != 0 || auth != nil {
		t.Errorf("%+v (diffs %d, auth %v)", check, len(diffs), auth)
	}
}
