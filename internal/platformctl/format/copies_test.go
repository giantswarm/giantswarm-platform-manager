package format

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
)

// The dry run of a wave that moves a token-exchange pair onto a target whose
// record holds unrelated generated values, as platformctl prints it, held to
// a golden: the target's plan lists the wave's vault copy by key — the
// pair's value, supplied from the hub's file — and, outside it, the two
// carries of the target's own that the wave does not copy, and the commit
// refusal names them. No value exists at a dry run; the text names keys.
func TestWavePairCopiesDryRunGolden(t *testing.T) {
	const (
		pairing     = "muster-token-exchange-oak-hazel-client-secret"
		clientValue = oak + "-mcp-capi-dex-client-secret"
		password    = oak + "-mcp-prometheus-valkey-password"
	)
	extras := acmeMCs + ":management-clusters/" + oak + "/extras/"
	hubFile := acmeMCs + ":management-clusters/" + hazel + "/extras/agent-platform/secrets/" + oak + "-token-exchange-credentials.yaml"
	pairFile := "management-clusters/" + oak + "/extras/agent-platform/secrets/dex-client-muster-token-exchange-oak-hazel-secret.yaml"
	target := plan.Installation{
		Name: oak, State: enabled,
		Files: []plan.File{
			{Repository: acmeMCs, Path: pairFile, Change: plan.ChangeCreate},
			{Repository: acmeMCs, Path: "management-clusters/" + oak + "/extras/mcp-capi/dex-client-mcp-capi-secret.yaml", Change: plan.ChangeCreate},
			{Repository: acmeMCs, Path: "management-clusters/" + oak + "/extras/mcp-prometheus/oauth-credentials.enc.yaml", Change: plan.ChangeUpdate},
		},
		GeneratedSecrets: []plan.GeneratedSecret{
			{Name: pairing, Kind: base64Kind, Length: 32, Files: []string{acmeMCs + ":" + pairFile}, Peer: hubFile, Supplied: true},
			{Name: clientValue, Kind: base64Kind, Length: 32, Files: []string{extras + "mcp-capi/dex-client-mcp-capi-secret.yaml", extras + "mcp-capi/oauth-credentials.enc.yaml"},
				FrozenIn: []string{extras + "mcp-capi/oauth-credentials.enc.yaml"}, Kept: true,
				Carries: []plan.Carry{{From: extras + "mcp-capi/oauth-credentials.enc.yaml#stringData.dex-client-secret", To: extras + "mcp-capi/dex-client-mcp-capi-secret.yaml#stringData.secret", Create: true}}},
			{Name: password, Kind: alphanumeric, Length: 32, Files: []string{extras + "mcp-prometheus/oauth-credentials.enc.yaml", extras + "mcp-prometheus/valkey-credentials.enc.yaml"},
				FrozenIn: []string{extras + "mcp-prometheus/valkey-credentials.enc.yaml"}, Kept: true,
				Carries: []plan.Carry{{From: extras + "mcp-prometheus/valkey-credentials.enc.yaml#stringData.default", To: extras + "mcp-prometheus/oauth-credentials.enc.yaml#stringData.VALKEY_PASSWORD"}}},
		},
		SuppliedSecrets: []string{pairing},
		Diff:            map[plan.Change]int{plan.ChangeCreate: 2, plan.ChangeUpdate: 1},
	}
	target.VaultCopies = target.WaveVaultCopies()
	target.CommitRefused = target.CarryRefusal()
	r := tools.CapabilityResult{
		Caller: "wren", Hub: hazel, Tool: tools.ToolReconcileCapability, Capability: agentPlatform, DryRun: true,
		Order:         []string{hazel, oak},
		Installations: []tools.DryRun{{Installation: plan.Installation{Name: hazel, State: enabled, Diff: map[plan.Change]int{plan.ChangeUnchanged: 3}}}, {Installation: target}},
		PullRequests:  []plan.PullRequest{{Order: 1, Repository: acmeMCs, Installations: []string{oak}, Changes: 3}},
	}
	var buf bytes.Buffer
	if err := Plan(&buf, r, false); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"Vault copies for the wave, the pair's values only:",
		pairing + ": " + hubFile + " → --secret " + pairing + "=… at commit",
		"Outside the pair, not copied by the wave",
		clientValue + ": " + extras + "mcp-capi/oauth-credentials.enc.yaml#stringData.dex-client-secret → " + extras + "mcp-capi/dex-client-mcp-capi-secret.yaml#stringData.secret",
		password + ": " + extras + "mcp-prometheus/valkey-credentials.enc.yaml#stringData.default → " + extras + "mcp-prometheus/oauth-credentials.enc.yaml#stringData.VALKEY_PASSWORD",
		"A commit would be refused: the wave's vault copy selects the pair it moves (" + pairing + ")",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the dry run lacks %q", want)
		}
	}
	const golden = "testdata/wave-pair-copies-dry-run.golden"
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if got != string(want) {
		t.Fatalf("the dry run differs from %s (run with -update to accept):\n%s", golden, got)
	}
}
