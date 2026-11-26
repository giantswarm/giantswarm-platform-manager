package e2e

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/render/clustermcpservers"
)

// tunnelledFleetValues is teleport-fleet's tunnelport values with one tunnel
// to the hub's Dex, consumed by alder in giantswarm.
var tunnelledFleetValues = "tunnelport:\n  consumers:\n    " + alder + ":\n      installNamespace: giantswarm\n  trustBundle:\n    tokens: []\n  tunnels:\n" +
	"    - name: dex-" + hub + "\n      appLabels: {app: dex, cluster: " + hub + ", customer: giantswarm}\n      tokens:\n        - {name: dex-" + hub + "-bot-token-" + alder + ", consumer: " + alder + "}\n"

const alderTunnelportRelease = "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: tunnelport\n  namespace: giantswarm\nspec:\n  values:\n    installNamespace: giantswarm\n    trustBundle:\n      enabled: true\n      secretName: alder-spiffe-bundle\n      tokenName: tunnelport-trust-bundle-token-" + alder + "\n"

// A target teleport-fleet tunnels to its hub's Dex registers the hub's
// connector through that tunnel — the tunnel's Service in the target's
// install namespace, trusted by the bundle of the Secret its tunnelport
// release writes, which Dex mounts — and a target teleport-fleet declares a
// tunnel for without that release on record is not planned. Encrypted Dex
// values that carry a volumes list hold the commit: the list would stand whole
// over the rendered mount.
func TestHubConnectorThroughTheFleetsDexTunnel(t *testing.T) {
	st, _ := mixedWaveStack(t)
	st.ghs.addFile(teleportFleet, tunnelportValuesPath, tunnelledFleetValues)
	c := st.mcpClient(t, aliceToken)
	args := map[string]any{tools.ArgInstallations: []string{alder, hub}, tools.ArgCapability: clustermcpservers.Capability, tools.ArgContent: true}

	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args)
	require.False(t, isErr, text)
	require.NotEmpty(t, dry.Skipped, "alder is tunnelled without its tunnelport release on record")
	i := slices.IndexFunc(dry.Skipped, func(s tools.Skipped) bool { return s.Name == alder })
	require.GreaterOrEqual(t, i, 0, text)
	require.Contains(t, strings.Join(dry.Skipped[i].Errors, "\n"), installations.TunnelportReleasePath(alder), text)

	st.ghs.addFile(acmeMCs, installations.TunnelportReleasePath(alder), alderTunnelportRelease)
	dry, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, args)
	require.False(t, isErr, text)
	require.Empty(t, dry.Skipped, text)
	p := findPlan(t, dry, alder)
	j := slices.IndexFunc(p.Files, func(f plan.File) bool { return strings.HasSuffix(f.Path, "/apps/dex-app/configmap-values.yaml.patch") })
	require.GreaterOrEqual(t, j, 0, text)
	patch := p.Files[j].Content
	tunnel := "https://dex-" + hub + ".giantswarm.svc.cluster.local:8443"
	for _, want := range []string{
		"issuer: " + tunnel + "\n", "issuerAlias: https://dex." + hub, "jwksURL: " + tunnel + "/keys",
		"- /etc/dex-tunnelport/alder-spiffe-bundle/svid_bundle.pem", "secretName: alder-spiffe-bundle",
		"mountPath: /etc/dex-tunnelport/alder-spiffe-bundle",
	} {
		require.Contains(t, patch, want)
	}
	st.ghs.addFile(acmeConfigs, installations.DexSecretPatchPath(alder),
		"dex:\n    volumes:\n        - name: ENC[AES256_GCM,data:x,iv:x,tag:x,type:str]\nsops:\n    version: 3.11.0\n")
	dry, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, args)
	require.False(t, isErr, text)
	held := findPlan(t, dry, alder).CommitRefused
	require.Contains(t, held, "dex.volumes (1 entries)", text)
	require.Contains(t, held, "the rendered Dex volumes alder-spiffe-bundle", text)
}

// Encrypted Dex values that carry the hub connector's owner's connectors as
// a list hold the commit of a target that renders the hub's connector there:
// the list would stand whole over the rendered map.
func TestHubConnectorHeldByAnEncryptedConnectorsList(t *testing.T) {
	st, _ := mixedWaveStack(t)
	st.ghs.addFile(acmeConfigs, installations.DexSecretPatchPath(alder),
		"oidc:\n    customer:\n        connectors:\n            - id: ENC[AES256_GCM,data:x,iv:x,tag:x,type:str]\nsops:\n    version: 3.11.0\n")
	c := st.mcpClient(t, aliceToken)
	args := map[string]any{tools.ArgInstallations: []string{alder, hub}, tools.ArgCapability: clustermcpservers.Capability}
	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args)
	require.False(t, isErr, text)
	require.Contains(t, findPlan(t, dry, alder).CommitRefused, "oidc.customer.connectors (1 entries)", text)
}

// A reconcile whose hub no longer renders a tunnelled app removes its
// RemoteApp document from remoteapps.yaml; the fleet's Kustomization over the
// extras tree does not prune, so the dry run lists the RemoteApp for a person
// to delete after the merge, and the committed action and its pull request
// carry the same checklist. A reconcile that removes nothing lists nothing.
func TestReconcileListsTheRemoteAppItTakesOffTheRecord(t *testing.T) {
	st := newStack(t)
	fixtures(st.ghs)
	sopsFixtures(t, st.ghs)
	st.ghs.addRepo(teleportFleet, map[string]string{tunnelportValuesPath: tunnelportValuesBare})
	c := st.mcpClient(t, aliceToken)
	putOnRecord(t, st, c, rowan, federation(true))
	args := func() map[string]any {
		return map[string]any{tools.ArgInstallation: rowan, tools.ArgInputs: federation(true)}
	}

	dry, text, isErr := dryRun(t, c, tools.ToolReconcileCapability, args())
	require.False(t, isErr, text)
	require.Empty(t, findPlan(t, dry, rowan).Checklist, "a reconcile that removes nothing lists nothing")

	repo, path, content := onRecord(t, st, "/tunnelport/remoteapps.yaml")
	kagent := "apiVersion: access.giantswarm.io/v1alpha1\nkind: RemoteApp\nmetadata:\n  name: kagent-" + alder + "\n  namespace: " + platformNamespace +
		"\nspec:\n  appName: kagent-" + alder + "\n  port: 4180\n  tokenName: kagent-" + alder + "-bot-token-" + rowan + "\n"
	st.ghs.addFile(repo, path, content+"---\n"+kagent)
	seedRemote(t, st)

	dry, text, isErr = dryRun(t, c, tools.ToolReconcileCapability, args())
	require.False(t, isErr, text)
	p := findPlan(t, dry, rowan)
	i := slices.IndexFunc(p.Files, func(f plan.File) bool { return f.Repository == repo && f.Path == path })
	require.GreaterOrEqual(t, i, 0, text)
	require.Equal(t, plan.ChangeUpdate, p.Files[i].Change, text)
	require.NotContains(t, p.Files[i].Content, "kagent-"+alder, text)
	want := plan.Object{Kind: "RemoteApp", Namespace: platformNamespace, Name: "kagent-" + alder}
	require.Equal(t, []plan.Object{want}, p.Checklist, text)

	out, text, isErr := commitCall(t, c, tools.ToolReconcileCapability, args())
	require.False(t, isErr, text)
	require.NotNil(t, out.Action, text)
	require.Equal(t, []actions.Orphan{{Installation: rowan, Kind: want.Kind, Namespace: want.Namespace, Name: want.Name}}, out.Action.Status.Orphans)
	prs := st.remote.PullRequests()
	require.Len(t, prs, 1)
	require.Contains(t, prs[0].Body, "- [ ] RemoteApp "+platformNamespace+"/kagent-"+alder+"\n")
}
