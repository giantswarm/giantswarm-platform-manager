package e2e

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/getsops/sops/v3/decrypt"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A hub and its target in one set, both files of the token-exchange pair
// new: alone, each side's dry run refuses the pair and names the wave as
// the way out; in one set the two plans are paired — each side is drawn
// with the other, nothing is refused for it — and the wave draws the value
// once: both installations' pull requests carry it encrypted, and decrypted
// it is one value on both sides.
func TestSetWaveDrawsATokenExchangePairOnce(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	id := sopsFor(t, st.ghs, acmeConfigs, acmeMCs, hubConfigs, hubMCs)
	// The hub brokers into birch, which runs the platform: the hub's
	// credentials Secret for birch and birch's Dex-side copy are both new.
	portal := strings.Replace(portalConfig(hub, alder, birch), "        gs:\n", "        gs:\n          clusterTokenBroker:\n            tokenUrl: https://muster."+hub+".example.test/token\n", 1)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), portal)
	// The hub's broker client is on record in its patch, read back as the broker's id.
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	patch, _ := st.ghs.file(hubConfigs, marker)
	st.ghs.addFile(hubConfigs, marker, strings.Replace(patch, "      server:\n", "      server:\n        tokenExchangeBroker:\n          brokerClients:\n            broker: {}\n", 1))
	// birch is private: the hub's tunnel tokens land in teleport-fleet's values, on record.
	st.ghs.addRepo(teleportFleet, map[string]string{tunnelportValuesPath: tunnelportValuesBare})
	aliceC := st.mcpClient(t, aliceToken)
	name := "muster-token-exchange-" + birch + "-client-secret"
	hubPath := "management-clusters/" + hub + "/extras/agent-platform/secrets/" + birch + "-token-exchange-credentials.yaml"
	peerPath := "management-clusters/" + birch + "/extras/agent-platform/secrets/dex-client-muster-token-exchange-" + birch + "-secret.yaml"

	for _, inst := range []string{hub, birch} {
		out, text, isErr := dryRun(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallation: inst, tools.ArgInputs: minimalInputs(nil)})
		if isErr {
			t.Fatal(text)
		}
		if p := findPlan(t, out, inst); !strings.Contains(p.CommitRefused, name+" is one value with") || !strings.Contains(p.CommitRefused, "in the same wave") {
			t.Fatalf("%s alone: commitRefused %q", inst, p.CommitRefused)
		}
	}
	dry, text, isErr := dryRun(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{hub, birch}, tools.ArgInputs: minimalInputs(nil), tools.ArgContent: true})
	if isErr {
		t.Fatal(text)
	}
	sides := []struct{ inst, with, repo, path, key string }{{hub, birch, hubMCs, hubPath, "client-secret"}, {birch, hub, acmeMCs, peerPath, render.DexSecretKey}}
	for _, s := range sides {
		p := findPlan(t, dry, s.inst)
		i := slices.IndexFunc(p.GeneratedSecrets, func(g plan.GeneratedSecret) bool { return g.Name == name })
		if i < 0 {
			t.Fatalf("%s: no %s among %+v", s.inst, name, p.GeneratedSecrets)
		}
		if g := p.GeneratedSecrets[i]; g.DrawnWith != s.with || g.Refusal != "" || g.Supplied || !slices.Equal(g.Files, []string{s.repo + ":" + s.path}) || strings.Contains(p.CommitRefused, name) {
			t.Errorf("%s in the set: %+v, commitRefused %q", s.inst, g, p.CommitRefused)
		}
	}

	// The wave: a supplied value's file goes on record first (this wave
	// supplies none), then the commit draws the pair once and opens both stages.
	for _, p := range dry.Installations {
		for _, f := range p.Files {
			if isSecretFile(f.Path) && strings.Contains(f.Content, "SUPPLIED(") {
				st.ghs.addFile(f.Repository, f.Path, "sops: on record\n")
			}
		}
	}
	seedRemote(t, st)
	text, isErr = call(t, aliceC, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{hub, birch}, tools.ArgInputs: minimalInputs(nil), tools.ArgReason: commitReason, tools.ArgMode: string(tools.ModeCommit)})
	var w tools.WaveResult
	if isErr || json.Unmarshal([]byte(text), &w) != nil || w.Action == nil {
		t.Fatalf("wave commit: %v %s", isErr, text)
	}
	assertNoLeak(t, "the wave's answer", text)
	t.Setenv("SOPS_AGE_KEY", id.String())
	values := map[string]string{}
	for _, s := range sides {
		i := slices.IndexFunc(w.PullRequests, func(pr actions.PullRequest) bool { return pr.Installation == s.inst && pr.Repository == s.repo })
		if i < 0 {
			t.Fatalf("no pull request of %s on %s among %+v", s.inst, s.repo, w.PullRequests)
		}
		content, ok := st.remote.Files(repoOf(t, s.repo), w.PullRequests[i].Head)[s.path]
		if !ok || !strings.Contains(string(content), "ENC[") || strings.Contains(string(content), "GENERATED(") {
			t.Fatalf("%s: %s on %s:\n%s", s.inst, s.path, w.PullRequests[i].Head, content)
		}
		clear, err := decrypt.Data(content, "yaml")
		if err != nil {
			t.Fatalf("%s: decrypting %s: %v", s.inst, s.path, err)
		}
		var secret struct {
			StringData map[string]string `yaml:"stringData"`
		}
		if err := yaml.Unmarshal(clear, &secret); err != nil {
			t.Fatal(err)
		}
		values[s.inst] = secret.StringData[s.key]
	}
	if values[hub] == "" || values[hub] != values[birch] {
		t.Fatalf("the pair holds two values: %d and %d characters", len(values[hub]), len(values[birch]))
	}
}
