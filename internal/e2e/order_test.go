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
	serversKey    = "servers"
	platformKey   = "agentPlatform"
)

// targetServers are the servers a target in these inputs runs: all three.
var targetServers = []any{"kubernetes", "prometheus", "capi"}

// federation makes rowan the hub of alder, a private target or a public one;
// alder runs no agent platform and keeps rowan's Dex client by hand.
func federation(private bool) map[string]any {
	return minimalInputs(map[string]any{argInstallation: map[string]any{federationKey: map[string]any{
		"brokerClientId": "broker", hubsKey: []any{},
		targetsKey: []any{map[string]any{argInstallation: alder, baseDomainKey: alder + ".example", argPrivate: private, serversKey: targetServers, platformKey: false}}}}})
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
// hub would create the Dex side of hazel's token-exchange client, whose value
// is one with hazel's credentials Secret for rowan — a file of hazel's plan,
// not on record. The commit draws a value per installation, so the pair would
// disagree: the reconcile is refused before any write, naming hazel's file.
// (The merge order a referenced Dex client's Secret sets is
// TestBuildOrdersTheReferencedClient's.)
func TestCommitRefusesOneSideOfAnExchangePair(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	aliceC := st.mcpClient(t, aliceToken)
	putOnRecord(t, st, aliceC, rowan, minimalInputs(nil))
	seedRemote(t, st)
	inputs := minimalInputs(map[string]any{argInstallation: map[string]any{federationKey: map[string]any{hubsKey: []any{hub}, "registryHub": hub, targetsKey: []any{}}}})
	hubFile := hubMCs + ":management-clusters/" + hub + "/extras/agent-platform/secrets/" + rowan + "-token-exchange-credentials.yaml"

	dry, text, isErr := dryRun(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs})
	if isErr {
		t.Fatal(text)
	}
	if p := findPlan(t, dry, rowan); !strings.Contains(p.CommitRefused, "muster-token-exchange-"+rowan+"-client-secret is one value with "+hub+"'s "+hubFile) || !strings.Contains(p.CommitRefused, "not on record") {
		t.Fatalf("commitRefused %q", p.CommitRefused)
	}
	calls := len(st.calls())
	if _, text, isErr := commitCall(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: inputs}); !isErr || !strings.Contains(text, hubFile) || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("commit: %v %s", isErr, text)
	}
	if got := st.calls(); len(got) != calls || len(st.remote.PullRequests()) != 0 {
		t.Fatalf("a refused commit wrote: %v", got[calls:])
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
