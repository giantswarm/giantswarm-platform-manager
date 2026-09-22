package e2e

// The merge order follows what the files reference, not the repository
// kind. A reconcile whose dex patch gains a client with a secretRef lists
// the management-clusters pull request, which creates the Secret, before the
// configs pull request that names it — on dex-app the reference is a
// secretKeyRef on the Dex pod, which does not start before the Secret
// exists. A reconcile whose tunnelport files gain a token lists
// teleport-fleet, whose values create the token, before management-clusters.
// The dry run, the commit's answer, the review's list, the pull requests'
// bodies and merge_action's merges carry one order, each pull request saying
// which ones it follows and why.

import (
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/mark3labs/mcp-go/client"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// The fleet's repository the tunnelport values live in, as the definition
// names it, and the values on record before any hub's entries: the template
// reads them, so the lists exist and are empty.
const (
	teleportFleet        = "giantswarm/teleport-fleet"
	tunnelportValuesPath = "kubernetes/envs/prod/values.yaml"
	tunnelportValuesBare = "# teleport-fleet's production values; the tunnelport template reads .Values.tunnelport.\ntunnelport:\n  consumers: {}\n  trustBundle:\n    tokens: []\n  tunnels: []\n"
)

// The keys of the federation inputs under installation.
const (
	federationKey = "federation"
	hubsKey       = "hubs"
	targetsKey    = "targets"
	baseDomainKey = "baseDomain"
)

// federation makes rowan the hub of alder, a private target or a public one.
func federation(private bool) map[string]any {
	return minimalInputs(map[string]any{argInstallation: map[string]any{federationKey: map[string]any{
		"brokerClientId": "broker", hubsKey: []any{},
		targetsKey: []any{map[string]any{argInstallation: alder, baseDomainKey: alder + ".example", argPrivate: private}}}}})
}

// position is the index of repository among prs, or -1.
func position(prs []plan.PullRequest, repository string) int {
	for i, pr := range prs {
		if pr.Repository == repository {
			return i
		}
	}
	return -1
}

// reconcileAndMerge reconciles rowan with inputs in mode commit as alice,
// approves as carol, turns every check green and merges as alice: the dry
// run's pull requests and the ones merged, in the order of each. On the way
// it checks that the commit's answer, the review's list and the pull
// requests' bodies carry the dry run's order.
func reconcileAndMerge(t *testing.T, st *stack, aliceC, carolC *client.Client, inputs map[string]any) ([]plan.PullRequest, []actions.PullRequest) {
	t.Helper()
	dry, text, isErr := dryRun(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, dry, rowan); p.Refused != "" || p.CommitRefused != "" {
		t.Fatalf("refused %q, commitRefused %q", p.Refused, p.CommitRefused)
	}
	out, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr || out.Action == nil || len(out.PullRequests) != len(dry.PullRequests) {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	posted := st.gateway.posted()
	listed, _ := posted[len(posted)-1].Body["pullRequests"].([]any)
	bodies := map[string]string{}
	for _, pr := range st.remote.PullRequests() {
		bodies[pr.Repository.String()] = pr.Body
	}
	for i, pr := range dry.PullRequests {
		if out.PullRequests[i].Repository != pr.Repository || out.PullRequests[i].Number != i+1 || listed[i] != out.PullRequests[i].URL {
			t.Fatalf("the commit and the review carry the dry run's order: dry %+v, committed %+v, review %v", dry.PullRequests, out.PullRequests, listed)
		}
		if after := pr.AfterClause(); after != "" && !strings.Contains(bodies[pr.Repository], ", "+after) {
			t.Fatalf("the body of %s lacks %q:\n%s", pr.Repository, after, bodies[pr.Repository])
		}
	}
	if _, text, isErr := decide(t, carolC, tools.ToolApproveAction, map[string]any{tools.ArgAction: out.Action.Name}); isErr {
		t.Fatalf("approve: %s", text)
	}
	for _, pr := range st.remote.PullRequests() {
		st.remote.SetChecks(pr.PullRequest, commit.ChecksSuccess)
	}
	m, text, isErr := mergeCall(t, aliceC, out.Action.Name)
	if isErr || m.Waiting != "" || len(m.Merged) != len(dry.PullRequests) || m.Action.Status.State != actions.StateRollingOut {
		t.Fatalf("merge: %v %s", isErr, text)
	}
	return dry.PullRequests, m.Merged
}

// rowan is enabled by hand without a hub; a reconcile that makes hazel its
// hub renders hazel's token-exchange client into the configs dex patch with a
// secretRef, and the client's Secret into management-clusters. The
// management-clusters pull request comes first everywhere and the configs
// one says it follows it for that Secret; merge_action merges them so.
func TestMergeOrderFollowsAReferencedDexClient(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)
	putOnRecord(t, st, aliceC, rowan, minimalInputs(nil))
	seedRemote(t, st)
	inputs := minimalInputs(map[string]any{argInstallation: map[string]any{federationKey: map[string]any{hubsKey: []any{hub}, targetsKey: []any{}}}})
	exchangeClient := "Secret/dex-client-" + hub + "-token-exchange"

	planned, merged := reconcileAndMerge(t, st, aliceC, carolC, inputs)
	if len(planned) != 2 || planned[0].Repository != acmeMCs || planned[1].Repository != acmeConfigs || planned[1].AfterClause() != "after "+acmeMCs+" ("+exchangeClient+")" || len(planned[0].After) != 0 {
		t.Fatalf("pull requests: %+v", planned)
	}
	if merged[0].Repository != acmeMCs || merged[1].Repository != acmeConfigs {
		t.Fatalf("merged: %+v", merged)
	}
	if calls := st.calls(); !strings.HasPrefix(calls[len(calls)-2], alice+" "+commit.OpMerge+" "+acmeMCs+"#1") || !strings.HasPrefix(calls[len(calls)-1], alice+" "+commit.OpMerge+" "+acmeConfigs+"#2") {
		t.Fatalf("remote calls: %v", calls)
	}
}

// rowan is the hub of alder, a public target, enabled by hand; a reconcile
// that makes alder private renders the tunnelport release and the RemoteApps
// into management-clusters, each naming a Teleport token, and the tokens
// into teleport-fleet's tunnelport values. teleport-fleet comes before
// management-clusters everywhere, which says it follows it for the tokens;
// merge_action merges them so.
func TestMergeOrderFollowsATunnelToken(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	st.ghs.addRepo(teleportFleet, map[string]string{tunnelportValuesPath: tunnelportValuesBare})
	aliceC, carolC := st.mcpClient(t, aliceToken), st.mcpClient(t, carolToken)
	putOnRecord(t, st, aliceC, rowan, federation(false))
	seedRemote(t, st)

	planned, merged := reconcileAndMerge(t, st, aliceC, carolC, federation(true))
	fleet, mcs := position(planned, teleportFleet), position(planned, acmeMCs)
	if fleet < 0 || mcs < 0 || fleet > mcs {
		t.Fatalf("pull requests: %+v", planned)
	}
	after := planned[mcs].AfterClause()
	for _, want := range []string{"after " + teleportFleet + " (", "ProvisionToken/tunnelport-trust-bundle-token-" + rowan, "ProvisionToken/dex-" + alder + "-bot-token"} {
		if !strings.Contains(after, want) {
			t.Errorf("management-clusters follows teleport-fleet for the tokens; %q lacks %q", after, want)
		}
	}
	if len(planned[fleet].After) != 0 {
		t.Errorf("teleport-fleet follows nothing: %+v", planned[fleet].After)
	}
	if position(mergedRepos(merged), teleportFleet) > position(mergedRepos(merged), acmeMCs) {
		t.Fatalf("merged: %+v", merged)
	}
	values := st.ghs.repos()[teleportFleet][tunnelportValuesPath]
	if !strings.Contains(values, "name: tunnelport-trust-bundle-token-"+rowan) || !strings.Contains(values, "name: dex-"+alder+"-bot-token") {
		t.Fatalf("the tokens landed in teleport-fleet's values:\n%s", values)
	}
}

// mergedRepos are prs as the plan's, by repository, in the order merged.
func mergedRepos(prs []actions.PullRequest) []plan.PullRequest {
	out := make([]plan.PullRequest, 0, len(prs))
	for _, pr := range prs {
		out = append(out, plan.PullRequest{Repository: pr.Repository})
	}
	return out
}
