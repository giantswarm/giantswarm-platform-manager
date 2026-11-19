package e2e

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/getsops/sops/v3/decrypt"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/clustermcpservers"
)

// mixedWaveStack is the registry's hub on the agent platform brokering into
// alder, which runs cluster-mcp-servers: the pairs between the two are on
// record on neither side.
func mixedWaveStack(t *testing.T) (*stack, interface{ String() string }) {
	t.Helper()
	st := newStack(t)
	fixtures(st.ghs)
	id := sopsFor(t, st.ghs, acmeConfigs, acmeMCs, hubConfigs, hubMCs)
	portal := strings.Replace(portalConfig(hub, alder, birch), "        gs:\n", "        gs:\n          clusterTokenBroker:\n            tokenUrl: https://muster."+hub+".example.test/token\n", 1)
	st.ghs.addFile(hubMCs, installations.PortalConfigPath(hub), portal)
	marker := installations.Capabilities()[0].EnabledMarker(hub)
	patch, _ := st.ghs.file(hubConfigs, marker)
	st.ghs.addFile(hubConfigs, marker, strings.Replace(patch, "      server:\n", "      server:\n        tokenExchangeBroker:\n          brokerClients:\n            broker: {}\n", 1))
	st.ghs.addFile(acmeMCs, installations.ClusterMCPServersMarker(alder), "resources:\n  - https://github.com/giantswarm/management-cluster-bases/extras/mcp-kubernetes?ref=main\n")
	st.ghs.addFile(acmeMCs, installations.CollectionsKustomizationPath(alder), collectionsKustomization(plan.DexAppConnectorMaps))
	st.ghs.addFile(acmeMCs, extrasKustomizationPath(alder), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - ./mcp-kubernetes/\n")
	st.ghs.addRepo(teleportFleet, map[string]string{tunnelportValuesPath: tunnelportValuesBare})
	return st, id
}

// mixedWavePairs are the values the hub and alder hold one side each of.
var mixedWavePairs = []string{render.FederationSecretName(hub, alder), render.ExchangeSecretName(render.TokenExchangeClient(alder, hub, true))}

func TestMixedWaveDrawsAHubTargetPairAcrossDefinitions(t *testing.T) {
	st, id := mixedWaveStack(t)
	c := st.mcpClient(t, aliceToken)
	args := map[string]any{tools.ArgInstallations: []string{alder, hub}, tools.ArgCapability: clustermcpservers.Capability, tools.ArgContent: true}

	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args)
	require.False(t, isErr, text)
	require.Empty(t, dry.Skipped)
	require.Equal(t, []string{hub, alder}, dry.Order, "the hub goes before the target it shares a value with")
	capabilities := map[string]string{}
	for _, e := range dry.Installations {
		capabilities[e.Name] = e.Capability
	}
	require.Equal(t, map[string]string{hub: installations.AgentPlatform, alder: ""}, capabilities)
	for _, name := range mixedWavePairs {
		for _, side := range []struct{ inst, with string }{{hub, alder}, {alder, hub}} {
			p := findPlan(t, dry, side.inst)
			require.False(t, slices.ContainsFunc(p.DeferredPairs, func(d plan.DeferredPair) bool { return d.Name == name }), "%s defers %s", side.inst, name)
			i := slices.IndexFunc(p.GeneratedSecrets, func(g plan.GeneratedSecret) bool { return g.Name == name })
			require.GreaterOrEqual(t, i, 0, "%s renders no %s", side.inst, name)
			g := p.GeneratedSecrets[i]
			require.Equal(t, side.with, g.DrawnWith, "%s: %+v", side.inst, g)
			require.Empty(t, g.Refusal)
			require.NotContains(t, p.CommitRefused, name)
		}
	}

	for _, p := range dry.Installations {
		for _, f := range p.Files {
			if isSecretFile(f.Path) && strings.Contains(f.Content, "SUPPLIED(") {
				st.ghs.addFile(f.Repository, f.Path, "sops: on record\n")
			}
		}
	}
	seedRemote(t, st)
	delete(args, tools.ArgContent)
	delete(args, tools.ArgDryRun)
	args[tools.ArgReason] = commitReason
	args[tools.ArgMode] = string(tools.ModeCommit)
	text, isErr = call(t, c, tools.ToolReconcileCapability, args)
	var w tools.WaveResult
	require.False(t, isErr, text)
	require.NoError(t, json.Unmarshal([]byte(text), &w))
	require.NotNil(t, w.Action, text)
	require.Equal(t, clustermcpservers.Capability, w.Action.Spec.Capability)
	require.Equal(t, map[string]string{hub: installations.AgentPlatform}, w.Action.Spec.CapabilityByInstallation)
	require.Equal(t, installations.AgentPlatform, w.Action.CapabilityOf(hub))
	require.Equal(t, clustermcpservers.Capability, w.Action.CapabilityOf(alder))
	require.Equal(t, hubConfigs+":"+marker(installations.AgentPlatform, hub), w.Action.Spec.Markers[hub])

	t.Setenv("SOPS_AGE_KEY", id.String())
	name := render.FederationSecretName(hub, alder)
	sides := []struct{ inst, repo, path, key string }{
		{hub, hubMCs, render.PlatformSecretsPath(hub, render.DexClientSecretFile(render.FederationClient(alder))), render.DexSecretKey},
		{alder, acmeMCs, "management-clusters/" + alder + "/extras/agent-platform/secrets/dex-connector-example-simple-oidc-secret.yaml", "client-secret"},
	}
	values := map[string]string{}
	for _, s := range sides {
		i := slices.IndexFunc(w.PullRequests, func(pr actions.PullRequest) bool { return pr.Installation == s.inst && pr.Repository == s.repo })
		require.GreaterOrEqual(t, i, 0, "no pull request of %s on %s among %+v", s.inst, s.repo, w.PullRequests)
		content, ok := st.remote.Files(repoOf(t, s.repo), w.PullRequests[i].Head)[s.path]
		require.True(t, ok, "%s: no %s on %s", s.inst, s.path, w.PullRequests[i].Head)
		clear, err := decrypt.Data(content, "yaml")
		require.NoError(t, err)
		var secret struct {
			StringData map[string]string `yaml:"stringData"`
		}
		require.NoError(t, yaml.Unmarshal(clear, &secret))
		values[s.inst] = secret.StringData[s.key]
	}
	require.NotEmpty(t, values[hub], name)
	require.Equal(t, values[hub], values[alder], "%s holds two values", name)
}

func TestMixedWaveLeavesAHubWithNothingToDrawAsItIs(t *testing.T) {
	st, _ := mixedWaveStack(t)
	c := st.mcpClient(t, aliceToken)
	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{hub, birch}, tools.ArgCapability: clustermcpservers.Capability})
	require.False(t, isErr, text)
	require.True(t, slices.ContainsFunc(dry.Skipped, func(s tools.Skipped) bool { return s.Name == hub && s.Reason == tools.SkippedNotEnabled }), "skipped %+v", dry.Skipped)
	for _, e := range dry.Installations {
		require.Empty(t, e.Capability, "%s switched definition", e.Name)
	}

	all, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgCapability: clustermcpservers.Capability})
	require.False(t, isErr, text)
	require.True(t, slices.ContainsFunc(all.Skipped, func(s tools.Skipped) bool { return s.Name == hub && s.Reason == tools.SkippedNotEnabled }), "skipped %+v", all.Skipped)
	p := findPlan(t, all, alder)
	for _, name := range mixedWavePairs {
		require.True(t, slices.ContainsFunc(p.DeferredPairs, func(d plan.DeferredPair) bool { return d.Name == name }), "every installation of the fleet: %s not deferred", name)
	}
}

func TestMixedWaveRefusesTypedInputs(t *testing.T) {
	st, _ := mixedWaveStack(t)
	c := st.mcpClient(t, aliceToken)
	_, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, map[string]any{tools.ArgInstallations: []string{alder, hub}, tools.ArgCapability: clustermcpservers.Capability,
		tools.ArgInputs: map[string]any{"servers": map[string]any{"mcpCapi": map[string]any{"enabled": false}}}})
	require.True(t, isErr, text)
	require.Contains(t, text, hub)
	require.Contains(t, text, installations.AgentPlatform)
}

// marker is the enabled marker of capability on inst.
func marker(capability, inst string) string {
	def, _ := installations.FindCapability(capability)
	return def.EnabledMarker(inst)
}
