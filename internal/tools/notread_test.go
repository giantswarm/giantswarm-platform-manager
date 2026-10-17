package tools

import (
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The fixtures of the not-read tests: a private installation whose
// kubernetes server the caller is not signed in to in muster and whose
// kagent endpoint the manager cannot reach.
const (
	birchServer = "birch-mcp-kubernetes"
	signInURL   = "https://muster.example.test/oauth/proxy/start?state=fixture"
	kagentHost  = "kagent.birch.example"
	dialError   = "dial tcp 10.0.0.7:443: i/o timeout"
)

func notConnected() string {
	return verify.NotConnected + ": auth_required: server '" + birchServer + "' requires authentication before its tools can be called"
}

func privateResult() verify.Result {
	auth := &verify.AuthRequired{Server: birchServer, URL: signInURL, Message: "auth_required: server '" + birchServer + "' requires authentication"}
	releases := verify.Dimension{ID: "live-helmreleases-ready", Kind: definitions.KindLive, Mark: verify.NotChecked, Live: &verify.LiveResult{AuthRequired: auth, Checks: []verify.Check{
		{Kind: string(render.HelmReleaseReady), Namespace: fluxNS, Resource: helmRelease, Name: installations.AgentPlatform, Mark: verify.NotChecked, Message: notConnected()},
		{Kind: string(render.HelmReleaseReady), Namespace: fluxNS, Resource: helmRelease, Name: "muster", Mark: verify.NotChecked, Message: notConnected()},
	}}}
	// A live dimension with an HTTP check the manager could not send and an
	// object check muster refused.
	kagent := verify.Dimension{ID: "live-kagent", Kind: definitions.KindLive, Mark: verify.NotChecked, Live: &verify.LiveResult{AuthRequired: auth, Checks: []verify.Check{
		{Kind: string(render.HTTP), URL: "https://" + kagentHost + "/api/health", Mark: verify.NotChecked, Message: verify.ReasonUnreachable + ": " + kagentHost, Detail: dialError},
		{Kind: string(render.PodsRunning), Namespace: "kagent", Resource: "Pod", Mark: verify.NotChecked, Message: notConnected()},
	}}}
	probe := verify.Dimension{ID: "kagent-ui-reachable", Kind: definitions.KindProbe, Mark: verify.NotChecked, Reason: verify.ReasonUnreachable + ": " + kagentHost, Detail: dialError,
		Probe: &verify.ProbeResult{Requests: []verify.Request{{URL: "https://" + kagentHost + "/?redirect_uri=secret", Error: dialError}}}}
	// Left out: a dimension of the repositories and one whose checks answered.
	files := verify.Dimension{ID: "values-file", Kind: "file", Mark: verify.NotChecked, Reason: verify.ReasonRepositorySide}
	green := verify.Dimension{ID: "live-crds", Kind: definitions.KindLive, Mark: verify.AsDefined, Live: &verify.LiveResult{Checks: []verify.Check{{Kind: string(render.APIServed), Mark: verify.AsDefined}}}}
	return liveResult(installations.StateEnabled, releases, kagent, probe, files, green)
}

// The watch names every read it could not make by cause: auth_required on
// the muster server with its sign-in, the host the manager got no answer
// from — each with the checks and dimensions it left unread — never only a
// count of unchecked items, and never the probe's query.
func TestNotReadNamesAuthRequiredAndUnreachable(t *testing.T) {
	got := notRead(privateResult(), birchServer)
	want := []NotRead{
		{Cause: CauseAuthRequired, Server: birchServer, SignInURL: signInURL, Message: "auth_required: server '" + birchServer + "' requires authentication before its tools can be called",
			Fix: signIn(birchServer), Checks: 3, Dimensions: []string{"live-helmreleases-ready", "live-kagent"}},
		{Cause: CauseUnreachable, Host: kagentHost, Message: dialError, Fix: noRoute, Checks: 2, Dimensions: []string{"live-kagent", "kagent-ui-reachable"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("not read:\n got %+v\nwant %+v", got, want)
	}
	line := got[0].Line()
	for _, s := range []string{"auth_required on muster server " + birchServer, "3 checks of 2 dimensions", "muster auth login --server " + birchServer} {
		if !strings.Contains(line, s) {
			t.Errorf("auth_required line %q lacks %q", line, s)
		}
	}
	if strings.Contains(line, signInURL) {
		t.Errorf("the line carries the sign-in URL: %q", line)
	}
	if line := got[1].Line(); !strings.Contains(line, "unreachable "+kagentHost+": 2 checks of 2 dimensions") || strings.Contains(line, "secret") {
		t.Errorf("unreachable line: %q", line)
	}
}

// An auth_required that names no server is the installation's own: the
// watch names the server its reads go to; without one it still says what to
// sign in to.
func TestNotReadNamesTheServerTheReadsGoTo(t *testing.T) {
	res := liveResult(installations.StateEnabled, verify.Dimension{ID: "live-helmreleases-ready", Kind: definitions.KindLive, Live: &verify.LiveResult{
		AuthRequired: &verify.AuthRequired{Message: "auth_required"}, Checks: []verify.Check{{Mark: verify.NotChecked, Message: verify.NotConnected + ": auth_required"}}}})
	if got := notRead(res, birchServer); len(got) != 1 || got[0].Server != birchServer || !strings.Contains(got[0].Fix, "--server "+birchServer) {
		t.Errorf("server from the watch: %+v", got)
	}
	if got := notRead(res, ""); len(got) != 1 || got[0].Server != "" || !strings.Contains(got[0].Line(), "the installation's kubernetes server in muster") {
		t.Errorf("no server known: %+v", got)
	}
}

// Forbidden reads, reads that did not answer and other refusals are named
// too, the timeouts of many objects as one cause.
func TestNotReadGroupsTheOtherCauses(t *testing.T) {
	res := liveResult(installations.StateEnabled, verify.Dimension{ID: "live-objects", Kind: definitions.KindLive, Live: &verify.LiveResult{Checks: []verify.Check{
		{Mark: verify.NotChecked, Message: "forbidden for viewer: pods is forbidden"},
		{Mark: verify.NotChecked, Message: "no answer within 20s from HelmRelease flux-giantswarm/muster"},
		{Mark: verify.NotChecked, Message: "not read: the live verify's read budget of 2m0s is spent; the last read that did not answer: HelmRelease flux-giantswarm/muster"},
		{Mark: verify.NotChecked, Message: "muster lists no such tool for this session"},
		{Mark: verify.AsDefined, Message: isReady},
	}}})
	var causes []string
	for _, n := range notRead(res, birchServer) {
		causes = append(causes, n.Cause)
	}
	if want := []string{CauseForbidden, CauseNoAnswer, CauseOther}; !reflect.DeepEqual(causes, want) {
		t.Errorf("causes %v, want %v", causes, want)
	}
	if got := notRead(liveResult(installations.StateEnabled), birchServer); got != nil {
		t.Errorf("nothing unread: %+v", got)
	}
}

// A private installation whose endpoints the manager cannot reach is said
// up front: the hosts, the endpoint checks not read from the manager, and
// the checks of its objects that run through the caller's muster session.
// A public installation, or a private one whose endpoints answered, gets no note.
func TestPrivateNoteSaysWhatRunsFromTheCaller(t *testing.T) {
	res := privateResult()
	reads := notRead(res, birchServer)
	private := map[string]any{"installation": map[string]any{"name": birch, "private": true}}
	note := privateNote(birch, private, reads, res, birchServer)
	for _, s := range []string{birch + " is private", "does not reach its endpoints (" + kagentHost + ")", "2 endpoint checks are not read from the manager (live-kagent, kagent-ui-reachable)",
		"run through your muster session to " + birchServer + " instead (live-helmreleases-ready, live-kagent, live-crds)"} {
		if !strings.Contains(note, s) {
			t.Errorf("note %q lacks %q", note, s)
		}
	}
	if note := privateNote(birch, map[string]any{"installation": map[string]any{"private": false}}, reads, res, birchServer); note != "" {
		t.Errorf("a public installation: %q", note)
	}
	if note := privateNote(birch, private, reads[:1], res, birchServer); note != "" {
		t.Errorf("endpoints that answered: %q", note)
	}
}

// The report in the review's thread says the installation is private up
// front and names every read not made with its sign-in, never the URL.
func TestReportNamesTheReadsNotMade(t *testing.T) {
	res := privateResult()
	reads := notRead(res, birchServer)
	private := privateNote(birch, map[string]any{"installation": map[string]any{"private": true}}, reads, res, birchServer)
	a := &actions.Action{Spec: actions.Spec{Installations: []string{birch}}}
	status := actions.Status{State: actions.StateEnabled, Rollout: &actions.Rollout{Installations: []actions.InstallationRollout{{Name: birch, State: actions.StateEnabled, WatchedBy: "someone"}}}}
	report := (&Tools{}).report(a, status, 0, res, nil, nil, "", private, reads)
	lines := strings.Split(report, "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[1], birch+" is private") {
		t.Errorf("the private note is not up front: %q", report)
	}
	for _, s := range []string{"⚪ not read — auth_required on muster server " + birchServer, "muster auth login --server " + birchServer, "⚪ not read — unreachable " + kagentHost} {
		if !strings.Contains(report, s) {
			t.Errorf("report lacks %q:\n%s", s, report)
		}
	}
	if strings.Contains(report, signInURL) {
		t.Errorf("the report carries the sign-in URL:\n%s", report)
	}
}
