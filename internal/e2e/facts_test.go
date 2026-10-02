package e2e

// The record's provider facts over the invented registry: on a CAPZ
// installation the 4 chart line's Substrate snapshot store renders its
// Workload Identity from provider.azure in the installation's record, which
// the definition never writes; a commit is held while the record leaves one
// empty, and the comparison still runs.

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// umbrellaConfigs is willow's configs repository.
const umbrellaConfigs = "example/umbrella-configs"

// willowOnLineFour puts willow, a CAPZ installation without the capability,
// on the 4 chart line with its cluster serving the pod certificates and its
// extras kustomization on record, its record carrying azure.
func willowOnLineFour(st *stack, azure string) {
	st.ghs.addFile(umbrellaConfigs, installations.ConfigPatchPath(willow), "codename: willow\nbase: umbrella.test\nprovider:\n  kind: capz\n"+azure+"agentPlatform:\n  kagentApiV2: true\n")
	st.ghs.addFile(umbrellaMCs, installations.ClusterAppManifestPath(willow), clusterAppManifest(willow, "cluster-azure", "9.3.0", true))
	st.ghs.addFile(umbrellaMCs, extrasKustomizationPath(willow), extrasKustomization)
}

// An enable on a CAPZ installation whose record lacks the subscription and
// the service-account issuer: the dry run and the comparison plan the files
// and say the commit would be refused, naming the record, both keys and
// where each value comes from; commit mode refuses with the same sentence
// before any write — no pull request, no action. With both set, the plan
// stands as before.
func TestCommitHeldByTheRecordsProviderFacts(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	willowOnLineFour(st, "")
	c := st.mcpClient(t, aliceToken)

	out, text, isErr := dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: willow})
	if isErr {
		t.Fatal(text)
	}
	p := findPlan(t, out, willow)
	want := p.CommitRefused
	for _, part := range []string{"the record " + umbrellaConfigs + ":" + installations.ConfigPatchPath(willow), "provider.azure.subscriptionId and provider.azure.oidcIssuerUrl empty",
		"kagent.harness.snapshotStore.crossplane.capz", "AzureCluster (spec.subscriptionID)", "/.well-known/openid-configuration"} {
		if !strings.Contains(want, part) {
			t.Errorf("the dry run's commitRefused %q lacks %q", want, part)
		}
	}
	if p.Refused != "" || len(p.Files) == 0 || p.MissingFacts == nil || len(p.MissingFacts.Missing) != 2 {
		t.Fatalf("the plan stands: refused %q, %d files, facts %+v", p.Refused, len(p.Files), p.MissingFacts)
	}
	if res := verifyWith(t, c, willow, nil); res.Refused != "" || res.CommitRefused != want || len(res.Features) == 0 {
		t.Fatalf("the comparison: refused %q, commitRefused %q, want %q", res.Refused, res.CommitRefused, want)
	}

	_, text, isErr = commitCall(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: willow})
	if !isErr || !strings.Contains(text, want) || !strings.Contains(text, "nothing is committed") {
		t.Fatalf("commit mode: %v %s", isErr, text)
	}
	if prs := st.remote.PullRequests(); len(prs) != 0 {
		t.Fatalf("the remote saw %d pull request(s)", len(prs))
	}
	if got := listActionsOf(t, c, willow); len(got) != 0 {
		t.Fatalf("the refusal recorded %d action(s)", len(got))
	}

	willowOnLineFour(st, "  azure:\n    subscriptionId: 00000000-0000-0000-0000-000000000000\n    oidcIssuerUrl: \"\"\n")
	out, _, _ = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: willow})
	if got := findPlan(t, out, willow).CommitRefused; !strings.Contains(got, "leaves provider.azure.oidcIssuerUrl empty") || strings.Contains(got, "subscriptionId empty") {
		t.Fatalf("the issuer alone empty: %q", got)
	}

	willowOnLineFour(st, "  azure:\n    subscriptionId: 00000000-0000-0000-0000-000000000000\n    oidcIssuerUrl: https://issuer.willow.umbrella.test\n")
	out, _, _ = dryRun(t, c, tools.ToolEnableCapability, map[string]any{tools.ArgInstallation: willow})
	if p := findPlan(t, out, willow); p.CommitRefused != "" || p.MissingFacts != nil {
		t.Fatalf("both set: commitRefused %q, facts %+v", p.CommitRefused, p.MissingFacts)
	}
}
