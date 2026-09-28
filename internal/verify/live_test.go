package verify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

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
	keyMetadata     = "metadata"
	keyName         = "name"
	keyConditions   = "conditions"
	keyType         = "type"
	keyMessage      = "message"
	kindSecret      = "Secret"
	conditionTrue   = "True"
	proxyWorkload   = "proxy"
	metaRelease     = "agent-platform"
	testRelease     = "rel"
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
		return map[string]any{keyConditions: []any{map[string]any{keyType: condition, keyStatus: conditionTrue, keyMessage: "ok"}}}
	}
	key := func(resource, name string) string { return resource + "/" + testNamespace + "/" + name }
	cluster := &recordingCluster{
		objects: map[string]map[string]any{
			key(kindHelmRelease, testRelease): {keySpec: map[string]any{"valuesFrom": []any{map[string]any{"kind": "ConfigMap", "name": valuesKey}}, valuesKey: map[string]any{"kagent": map[string]any{"replicas": "2"}}},
				keyStatus: map[string]any{keyConditions: conditions("Ready")[keyConditions], "lastAppliedRevision": "1.2.3"}},
			key("ConfigMap", valuesKey):        {keyData: map[string]any{"values.yaml": "kagent:\n  replicas: \"2\"\n", "x": "2"}},
			key(kindDeployment, proxyWorkload): {keySpec: map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": proxyWorkload}}}, keyStatus: conditions("Available")},
			key("Secret", "credential"):        {keyData: map[string]any{"k": "***"}},
			key(kindSecret, "loaded"):          secretWrittenAt(written),
		},
		pods: []map[string]any{podStartedAt(proxyWorkload+"-0", proxyWorkload, started)},
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
		{"HelmReleaseReady", render.Probe{Kind: render.HelmReleaseReady, Namespace: testNamespace, Resource: kindHelmRelease, Name: testRelease}, []Shape{Readiness}, nil, ""},
		{"Condition", render.Probe{Kind: render.Condition, Namespace: testNamespace, Resource: kindDeployment, Name: proxyWorkload, Expect: render.Expectation{Condition: "Available", ConditionStatus: conditionTrue}}, []Shape{Readiness}, nil, ""},
		{"ResourcePresent", render.Probe{Kind: render.ResourcePresent, Namespace: testNamespace, Resource: "Secret", Name: "credential", Expect: render.Expectation{Keys: []string{"k"}}}, []Shape{Readiness}, nil, ""},
		{"PodsRunning", render.Probe{Kind: render.PodsRunning, Namespace: testNamespace, Name: "app=" + proxyWorkload}, []Shape{Readiness}, nil, ""},
		{"LogAbsent", render.Probe{Kind: render.LogAbsent, Namespace: testNamespace, Resource: kindDeployment, Name: proxyWorkload, Expect: render.Expectation{Absent: "does not match"}}, []Shape{Readiness, Readiness}, []int{LogTail}, logTailNote},
		{"LogAbsent with the definition's note", render.Probe{Kind: render.LogAbsent, Namespace: testNamespace, Resource: kindDeployment, Name: proxyWorkload, Expect: render.Expectation{Absent: "does not match", Note: definitionNote}}, []Shape{Readiness, Readiness}, []int{LogTail}, definitionNote + "; " + logTailNote},
		{"Drift of a HelmRelease's values", render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: kindHelmRelease, Name: testRelease}, []Shape{Configuration, Configuration}, nil, ""},
		{"Drift of a place", render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: "ConfigMap", Name: valuesKey, Expect: render.Expectation{Compare: []render.Comparison{{Live: "data.x", Rendered: renderedLeaf}}}}, []Shape{Configuration}, nil, ""},
		{"SecretLoaded", render.Probe{Kind: render.SecretLoaded, Namespace: testNamespace, Resource: kindSecret, Name: "loaded", Expect: render.Expectation{Pods: "app=" + proxyWorkload, Container: proxyWorkload}}, []Shape{Manifest, Readiness}, nil, ""},
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

// A HelmRelease not Ready is failed when Flux's verdict on its last release
// is: Released=False after a failed upgrade — rolled back, or retried and in
// progress again — or Stalled=True; the message names the verdict. An upgrade
// in progress over a good release, a release waiting on a dependency and a
// Ready one are not failed.
func TestHelmReleaseReadyNamesAFailedRelease(t *testing.T) {
	condition := func(kind, status, reason, message string) any {
		return map[string]any{keyType: kind, keyStatus: status, "reason": reason, keyMessage: message}
	}
	const upgradeFailed = "Helm upgrade failed for release backstage/backstage with chart backstage@2.66.1: context deadline exceeded"
	progressing := condition("Ready", "Unknown", "Progressing", "Running 'upgrade' action with timeout of 10m0s")
	for _, c := range []struct {
		name       string
		conditions []any
		mark       Mark
		failed     bool
		message    string
	}{
		{"an upgrade in progress over a good release", []any{progressing, condition("Released", conditionTrue, "UpgradeSucceeded", "upgraded")},
			Drifted, false, "Ready=Unknown: Running 'upgrade' action with timeout of 10m0s"},
		{"waiting on a dependency", []any{condition("Ready", "False", "DependencyNotReady", "dependency 'flux-giantswarm/cnpg' is not ready")},
			Drifted, false, "Ready=False: dependency 'flux-giantswarm/cnpg' is not ready"},
		{"an upgrade rolled back", []any{condition("Ready", "False", "RollbackSucceeded", "Helm rollback to previous release backstage/backstage.v7 succeeded"),
			condition("Released", "False", "UpgradeFailed", upgradeFailed), condition("Remediated", conditionTrue, "RollbackSucceeded", "rolled back")},
			Drifted, true, "Ready=False: Helm rollback to previous release backstage/backstage.v7 succeeded; Released=False (UpgradeFailed): " + upgradeFailed},
		{"a retry in progress after a failed upgrade", []any{progressing, condition("Released", "False", "UpgradeFailed", upgradeFailed)},
			Drifted, true, "Ready=Unknown: Running 'upgrade' action with timeout of 10m0s; Released=False (UpgradeFailed): " + upgradeFailed},
		{"retries exhausted", []any{condition("Ready", "False", "UpgradeFailed", upgradeFailed), condition("Stalled", conditionTrue, "RetriesExceeded", "Failed to upgrade after 11 attempt(s)"),
			condition("Released", "False", "UpgradeFailed", upgradeFailed)},
			Drifted, true, "Ready=False: " + upgradeFailed + "; Stalled=True (RetriesExceeded): Failed to upgrade after 11 attempt(s)"},
		{"Ready", []any{condition("Ready", conditionTrue, "UpgradeSucceeded", "upgraded"), condition("Released", conditionTrue, "UpgradeSucceeded", "upgraded")},
			AsDefined, false, "Ready=True"},
	} {
		cluster := &recordingCluster{objects: map[string]map[string]any{
			kindHelmRelease + "/" + testNamespace + "/" + testRelease: {keyStatus: map[string]any{keyConditions: c.conditions}},
		}}
		x := &executor{opts: LiveOptions{Cluster: cluster}}
		check, _, _ := x.run(context.Background(), render.Probe{Kind: render.HelmReleaseReady, Namespace: testNamespace, Resource: kindHelmRelease, Name: testRelease})
		if check.Mark != c.mark || check.Failed != c.failed || check.Message != c.message {
			t.Errorf("%s: %s failed=%v %q, want %s failed=%v %q", c.name, check.Mark, check.Failed, check.Message, c.mark, c.failed, c.message)
		}
	}
}

// When a Secret's data was written and when the containers that read it
// started, in the fixtures of the SecretLoaded checks.
const (
	written = "2026-09-23T16:21:23Z"
	started = "2026-09-23T16:44:13Z"
	earlier = "2026-09-23T16:20:00Z"
	later   = "2026-09-23T17:00:00Z"
)

// managedFields is a managed-fields entry of manager at the time, owning the
// fields of fieldsV1.
func managedFields(manager, at string, fieldsV1 map[string]any) map[string]any {
	return map[string]any{"manager": manager, "operation": "Apply", "time": at, "fieldsV1": fieldsV1}
}

// dataFields and labelFields are the fields an entry owns: a Secret's data,
// its labels only.
var (
	dataFields  = map[string]any{"f:data": map[string]any{"f:secret": map[string]any{}}}
	labelFields = map[string]any{"f:metadata": map[string]any{"f:labels": map[string]any{"f:team": map[string]any{}}}}
)

// secretWrittenAt is a Secret, its values masked as the kubernetes tool
// answers them, whose data the managed-fields entries say was written at.
func secretWrittenAt(at string, more ...map[string]any) map[string]any {
	entries := []any{managedFields("kustomize-controller", at, dataFields)}
	for _, m := range more {
		entries = append(entries, m)
	}
	return map[string]any{keyMetadata: map[string]any{"managedFields": entries}, keyData: map[string]any{"secret": "***REDACTED***"}}
}

// podStartedAt is a pod whose container of the name runs since at; an empty
// at is a container that waits to start.
func podStartedAt(pod, container, at string) map[string]any {
	state := map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff"}}
	if at != "" {
		state = map[string]any{"running": map[string]any{"startedAt": at}}
	}
	return map[string]any{keyMetadata: map[string]any{keyName: pod}, keyStatus: map[string]any{"phase": "Running",
		"containerStatuses": []any{map[string]any{keyName: "sidecar", "state": map[string]any{"running": map[string]any{"startedAt": later}}}, map[string]any{keyName: container, "state": state}}}}
}

// A Secret a container reads only at its start is loaded when every running
// container started at or after the Secret's data last changed — the time
// of the managed-fields entry that owns the data — and holds the value from
// before when one started earlier, which the check names with the pod, the
// Secret and both times: a rotated client secret Dex has not read since.
// A later write of the labels alone moves nothing; the stringData a Secret
// was written with counts as its data. A container not running reads the
// Secret when it starts; with none running, or no entry saying when the data
// changed, the check claims nothing; with no pod, the workload is not there.
func TestSecretLoadedHoldsTheContainersToTheSecretsLastChange(t *testing.T) {
	const object, container = "dex-client-mcp-capi", "dex"
	probe := render.Probe{Kind: render.SecretLoaded, Namespace: "giantswarm", Resource: kindSecret, Name: object,
		Expect: render.Expectation{Pods: "app.kubernetes.io/name=dex", Container: container, Note: definitionNote}}
	for _, c := range []struct {
		name    string
		secret  map[string]any
		pods    []map[string]any
		mark    Mark
		message string
	}{
		{"Dex restarted after the rotation", secretWrittenAt(written), []map[string]any{podStartedAt("dex-a", container, started)}, AsDefined,
			"container dex of 1 pod(s) started after Secret giantswarm/dex-client-mcp-capi changed its data at " + written},
		{"Dex started with the write", secretWrittenAt(written), []map[string]any{podStartedAt("dex-a", container, written)}, AsDefined,
			"container dex of 1 pod(s) started after Secret giantswarm/dex-client-mcp-capi changed its data at " + written},
		{"a rotation Dex has not read", secretWrittenAt(written), []map[string]any{podStartedAt("dex-a", container, earlier)}, Drifted,
			"container dex of dex-a (at " + earlier + ") started before Secret giantswarm/dex-client-mcp-capi changed its data at " + written + ": it holds the value from before"},
		{"one replica of two restarted", secretWrittenAt(written), []map[string]any{podStartedAt("dex-a", container, started), podStartedAt("dex-b", container, earlier)}, Drifted,
			"container dex of dex-b (at " + earlier + ") started before Secret giantswarm/dex-client-mcp-capi changed its data at " + written + ": it holds the value from before"},
		{"the latest data write counts", secretWrittenAt(earlier, managedFields("kubectl", later, dataFields)), []map[string]any{podStartedAt("dex-a", container, started)}, Drifted,
			"container dex of dex-a (at " + started + ") started before Secret giantswarm/dex-client-mcp-capi changed its data at " + later + ": it holds the value from before"},
		{"a later label change moves nothing", secretWrittenAt(written, managedFields("kubectl", later, labelFields)), []map[string]any{podStartedAt("dex-a", container, started)}, AsDefined,
			"container dex of 1 pod(s) started after Secret giantswarm/dex-client-mcp-capi changed its data at " + written},
		{"stringData is the data", map[string]any{keyMetadata: map[string]any{"managedFields": []any{managedFields("kubectl", later, map[string]any{"f:stringData": map[string]any{}})}}}, []map[string]any{podStartedAt("dex-a", container, started)}, Drifted,
			"container dex of dex-a (at " + started + ") started before Secret giantswarm/dex-client-mcp-capi changed its data at " + later + ": it holds the value from before"},
		{"a container not running is not counted", secretWrittenAt(written), []map[string]any{podStartedAt("dex-a", container, started), podStartedAt("dex-b", container, "")}, AsDefined,
			"container dex of 1 pod(s) started after Secret giantswarm/dex-client-mcp-capi changed its data at " + written},
		{"no container running", secretWrittenAt(written), []map[string]any{podStartedAt("dex-a", container, "")}, NotChecked,
			"container dex runs in none of the 1 pod(s) app.kubernetes.io/name=dex selects: it reads the Secret when it starts"},
		{"no record of the data's change", map[string]any{keyData: map[string]any{"secret": "***REDACTED***"}}, nil, NotChecked,
			"no managed-fields entry of Secret giantswarm/dex-client-mcp-capi owns its data: when it changed is not known"},
		{"no Dex pod", secretWrittenAt(written), nil, Drifted, "no pod matches app.kubernetes.io/name=dex"},
	} {
		cluster := &recordingCluster{objects: map[string]map[string]any{kindSecret + "/giantswarm/" + object: c.secret}, pods: c.pods}
		x := &executor{opts: LiveOptions{Cluster: cluster}}
		check, diffs, auth := x.run(context.Background(), probe)
		if check.Mark != c.mark || check.Message != c.message || check.Note != definitionNote || check.Name != object || len(diffs) != 0 || auth != nil {
			t.Errorf("%s: %+v (diffs %d, auth %v), want %q %q", c.name, check, len(diffs), auth, c.mark, c.message)
		}
	}
	// Dex's Secret missing is the client unable to sign in at all.
	x := &executor{opts: LiveOptions{Cluster: &recordingCluster{}}}
	if check, _, _ := x.run(context.Background(), probe); check.Mark != Drifted || !strings.HasPrefix(check.Message, "does not exist") {
		t.Errorf("no Secret: %+v", check)
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

// A rendered leaf the live object lacks under a key the capability's
// migrations name is the planned addition it is on the record — a
// definition released after the installation's values were written — not
// drift: the check and its differences read planned with the migration's
// reason, on the whole values and on a compared place alike. A leaf the
// live object holds with another value, or lacks under no key, is drift.
func TestLiveDriftUnderAMigrationKeyIsPlanned(t *testing.T) {
	const kagentKey, anthropicKey, configKey, maxTokens = "kagent", "anthropic", "config", "maxTokens"
	const leaf, reason = kagentKey + ".providers." + anthropicKey + "." + configKey + "." + maxTokens, "Added: the cap · M34"
	lv := &liveRender{values: map[string]string{renderedLeaf: "2", leaf: "32000"},
		file: &fileDiff{path: "management-clusters/x/apps/agent-platform/" + configMapPatch, kind: definitions.KindConfigMap},
		migs: readMigrations([]definitions.Migration{{Key: "configmap:" + leaf, Reason: reason}}, nil)}
	withProviders := func(providers map[string]any) *recordingCluster {
		// The meta chart's values hold kagent's providers under kagent, and
		// kagent's own HelmRelease forwards them flat at spec.values.providers.
		values := map[string]any{kagentKey: map[string]any{"replicas": "2"}}
		if providers != nil {
			values[kagentKey].(map[string]any)["providers"] = providers
			values["providers"] = providers
		}
		return &recordingCluster{objects: map[string]map[string]any{kindHelmRelease + "/" + testNamespace + "/" + testRelease: {keySpec: map[string]any{valuesKey: values}}}}
	}
	whole := render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: kindHelmRelease, Name: testRelease}
	place := render.Probe{Kind: render.Drift, Namespace: testNamespace, Resource: kindHelmRelease, Name: testRelease,
		Expect: render.Expectation{Compare: []render.Comparison{{Live: "spec.values.providers", Rendered: "kagent.providers"}}}}
	capped := map[string]any{anthropicKey: map[string]any{configKey: map[string]any{maxTokens: "32000"}}}
	uncapped := map[string]any{anthropicKey: map[string]any{configKey: map[string]any{}}}
	other := map[string]any{anthropicKey: map[string]any{configKey: map[string]any{maxTokens: "8192"}}}
	for _, c := range []struct {
		name    string
		probe   render.Probe
		cluster *recordingCluster
		mark    Mark
		message string
		planned string
	}{
		{"the leaf absent from the values", whole, withProviders(nil), Planned, "1 planned change(s)", reason},
		{"the leaf absent from the place", place, withProviders(uncapped), Planned, "1 planned change(s)", reason},
		{"the place itself absent", place, withProviders(nil), Drifted, "1 difference(s)", ""},
		{"the leaf with another value", whole, withProviders(other), Drifted, "1 difference(s)", ""},
		{"the leaf present", place, withProviders(capped), AsDefined, "equal to the render", ""},
	} {
		x := &executor{opts: LiveOptions{Cluster: c.cluster}, lv: lv}
		check, diffs, _ := x.run(context.Background(), c.probe)
		if check.Mark != c.mark || check.Message != c.message {
			t.Errorf("%s: %q %q, want %q %q", c.name, check.Mark, check.Message, c.mark, c.message)
		}
		if c.mark == AsDefined {
			continue
		}
		if len(diffs) != 1 || diffs[0].Planned != c.planned {
			t.Errorf("%s: %+v", c.name, diffs)
		}
		if c.mark == Planned && (diffs[0].Path != leaf || diffs[0].Rendered != "32000" || diffs[0].Current != "") {
			t.Errorf("%s: the difference: %+v", c.name, diffs[0])
		}
	}
	// A leaf absent under no key stays drift.
	lv.values[kagentKey+".newLeaf"] = "x"
	x := &executor{opts: LiveOptions{Cluster: withProviders(capped)}, lv: lv}
	if check, diffs, _ := x.run(context.Background(), whole); check.Mark != Drifted || len(diffs) != 1 || diffs[0].Planned != "" || diffs[0].Path != kagentKey+".newLeaf" {
		t.Errorf("an unnamed leaf: %+v %+v", check, diffs)
	}
	// Without a values file on the render nothing is planned.
	x = &executor{opts: LiveOptions{Cluster: withProviders(nil)}, lv: &liveRender{values: lv.values, migs: lv.migs}}
	if check, _, _ := x.run(context.Background(), whole); check.Mark != Drifted {
		t.Errorf("without the file: %+v", check)
	}
}

// hangingCluster answers every read of one object never (until the context
// ends) and every other read at once.
type hangingCluster struct {
	recordingCluster
	hang string // resource/namespace/name
}

func (c *hangingCluster) Get(ctx context.Context, namespace, resource, name string, shape Shape) (map[string]any, error) {
	if resource+"/"+namespace+"/"+name == c.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.recordingCluster.Get(ctx, namespace, resource, name, shape)
}

// A read that does not answer within its bound is not checked, naming what
// did not answer and the bound; the checks that follow are read while the
// budget lasts and left unread, naming the read that hung, once it is
// spent — the call answers what it has, never the caller's deadline. Every
// check is logged with its duration.
func TestLiveReadsAreBounded(t *testing.T) {
	const hung, after, okRelease = "hung", "after", "answers"
	ready := func(name string) map[string]any {
		return map[string]any{keyStatus: map[string]any{keyConditions: []any{map[string]any{keyType: "Ready", keyStatus: conditionTrue, keyMessage: "ok"}}}, keyMetadata: map[string]any{keyName: name}}
	}
	key := func(name string) string { return kindHelmRelease + "/" + testNamespace + "/" + name }
	cluster := &hangingCluster{hang: key(hung), recordingCluster: recordingCluster{objects: map[string]map[string]any{
		key(okRelease): ready(okRelease), key(hung): ready(hung), key(after): ready(after),
	}}}
	var logs strings.Builder
	bounded := func() LiveOptions {
		return LiveOptions{Cluster: cluster, Installation: "x", ReadTimeout: 30 * time.Millisecond, ReadBudget: 50 * time.Millisecond, Log: slog.New(slog.NewTextHandler(&logs, nil))}
	}
	probe := func(name string) render.Probe {
		return render.Probe{ID: "live-" + name, Kind: render.HelmReleaseReady, Namespace: testNamespace, Resource: kindHelmRelease, Name: name}
	}
	x := &executor{opts: bounded()}
	if c, _, _ := x.run(context.Background(), probe(okRelease)); c.Mark != AsDefined {
		t.Fatalf("a read that answers: %+v", c)
	}
	c, _, _ := x.run(context.Background(), probe(hung))
	if c.Mark != NotChecked || c.Message != "no answer within 30ms from "+kindHelmRelease+" "+testNamespace+"/"+hung {
		t.Errorf("the read that hung: %+v", c)
	}
	// The budget has about 20 ms left: the next read is bounded by them and
	// hangs them away; the one after is not started.
	c, _, _ = x.run(context.Background(), probe(hung))
	if c.Mark != NotChecked || !strings.HasPrefix(c.Message, "no answer within ") || strings.Contains(c.Message, "30ms") {
		t.Errorf("the read bounded by the rest of the budget: %+v", c)
	}
	c, _, _ = x.run(context.Background(), probe(after))
	if c.Mark != NotChecked || c.Message != "not read: the live verify's read budget of 50ms is spent; the last read that did not answer: "+kindHelmRelease+" "+testNamespace+"/"+hung {
		t.Errorf("a read after the budget: %+v", c)
	}
	// A caller's own context ending is not a read that hung.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c, _, _ := (&executor{opts: LiveOptions{Cluster: cluster, ReadTimeout: time.Second}}).run(ctx, probe(hung)); c.Mark != NotChecked || !strings.Contains(c.Message, "context canceled") {
		t.Errorf("the caller's context: %+v", c)
	}
	// The whole dimension answers, and every check is logged with its duration.
	x = &executor{opts: bounded(), lv: &liveRender{probes: []render.Probe{probe(okRelease), probe(hung), probe(after)}}}
	logs.Reset()
	dim := x.dimension(context.Background(), definitions.Dimension{ID: "live-" + hung, Kind: definitions.KindLive})
	if dim.Mark != NotChecked || !strings.HasPrefix(dim.Reason, "no answer within 30ms from ") {
		t.Errorf("the dimension: %+v", dim)
	}
	for _, want := range []string{"msg=live_check", "probe=live-" + hung, "duration_ms=", "mark=\"not checked\""} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
}
