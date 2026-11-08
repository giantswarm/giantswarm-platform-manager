package e2e

// platformctl --local: the manager's tools run in process from the
// definitions built into platformctl, as the person whose GitHub token the
// session carries. The render is the deployed manager's render, and it says
// it was rendered locally; a commit stays the deployed manager's.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/platformctl/local"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
	"github.com/giantswarm/giantswarm-platform-manager/internal/version"
)

func openLocal(t *testing.T, st *stack, token string) (*local.Session, error) {
	t.Helper()
	s, err := local.Open(context.Background(), local.Options{Token: token, GitHubAPIURL: st.ghs.URL + "/api/v3", Registry: registrySources, Probes: st.probes.client()})
	if s != nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return s, err
}

func TestLocalRenderIsTheManagersRenderAndSaysSo(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	c := st.mcpClient(t, aliceToken)
	enableRowan(t, st, c, kagentEnabled(), kagentEnabled())
	deployed := verifyRowan(t, c, rowan)
	if deployed.RenderedBy.Local || deployed.RenderedBy.Version != testVersion {
		t.Fatalf("the deployed manager's verify: renderedBy %+v", deployed.RenderedBy)
	}

	ls, err := openLocal(t, st, aliceToken)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := ls.Call(context.Background(), tools.ToolVerifyCapability, map[string]any{tools.ArgInstallation: rowan})
	if err != nil {
		t.Fatal(err)
	}
	var got verify.Result
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.RenderedBy.Local || got.RenderedBy.Version != version.String() {
		t.Errorf("local verify: renderedBy %+v, want local %s", got.RenderedBy, version.String())
	}
	if got.Caller != alice || got.State != deployed.State || !reflect.DeepEqual(got.Summary, deployed.Summary) || !reflect.DeepEqual(got.Diff, deployed.Diff) {
		t.Errorf("local verify differs from the deployed manager's: caller %q state %q summary %v diff %v, want %q %q %v %v",
			got.Caller, got.State, got.Summary, got.Diff, alice, deployed.State, deployed.Summary, deployed.Diff)
	}

	args := func() map[string]any {
		return map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: kagentEnabled()}
	}
	want, text, isErr := dryRun(t, c, tools.ToolEnableCapability, args())
	if isErr {
		t.Fatalf("deployed dry run: %s", text)
	}
	dry := args()
	dry[tools.ArgDryRun] = true
	raw, err = ls.Call(context.Background(), tools.ToolEnableCapability, dry)
	if err != nil {
		t.Fatal(err)
	}
	var plan tools.CapabilityResult
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.RenderedBy.Local || want.RenderedBy.Local {
		t.Errorf("renderedBy: local %+v, deployed %+v", plan.RenderedBy, want.RenderedBy)
	}
	if len(plan.Installations) != 1 || len(want.Installations) != 1 || !reflect.DeepEqual(plan.Installations[0].Files, want.Installations[0].Files) || !reflect.DeepEqual(plan.PullRequests, want.PullRequests) {
		t.Errorf("the local plan differs from the deployed manager's:\n%.600s", raw)
	}

	commit := args()
	commit[tools.ArgMode] = string(tools.ModeCommit)
	commit[tools.ArgReason] = "a local commit"
	if _, err := ls.Call(context.Background(), tools.ToolEnableCapability, commit); err == nil || !strings.Contains(err.Error(), "rendered locally") {
		t.Errorf("a commit through the local session: %v, want the tools' refusal", err)
	}
}

func TestLocalRefusesATokenGitHubRefuses(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	if _, err := openLocal(t, st, bobToken); err == nil {
		t.Fatal("a token GitHub refuses opened a local session")
	}
}
