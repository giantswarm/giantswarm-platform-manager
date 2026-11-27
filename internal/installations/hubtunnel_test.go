package installations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// fleetValues is teleport-fleet's tunnelport values in their shape: the
// fleet's hub reaching its targets' Dex (dex-osprey), and two targets
// reaching the hub's Dex through dex-heron, one of them also a second hub's.
const fleetValues = `tunnelport:
  consumers:
    heron: {installNamespace: agent-platform}
    osprey: {installNamespace: giantswarm}
    plover: {installNamespace: giantswarm}
  tunnels:
    - name: dex-osprey
      appLabels: {app: dex, cluster: osprey, customer: giantswarm}
      tokens:
        - {name: dex-osprey-bot-token, consumer: heron}
    - name: dex-heron
      appLabels: {app: dex, cluster: heron, customer: giantswarm}
      tokens:
        - {name: dex-heron-bot-token-osprey, consumer: osprey}
        - {name: dex-heron-bot-token-plover, consumer: plover}
    - name: kubernetes-heron
      appLabels: {app: kubernetes, cluster: heron, customer: giantswarm}
      tokens:
        - {name: kubernetes-heron-bot-token-osprey, consumer: osprey}
`

// A consumer reaches a hub's Dex through every tunnel whose app is a Dex on
// the hub's cluster with a token of the consumer's, its Service the tunnel's
// name in the consumer's install namespace: osprey reaches heron's Dex and
// not its own, heron reaches no hub's, a tunnel to another app or to a
// cluster that is not one of the consumer's hubs counts for nothing.
func TestFleetDexTunnels(t *testing.T) {
	const heron = "heron"
	f, err := parseFleetTunnels(fleetValues)
	require.NoError(t, err)

	got, err := f.dexTunnels("osprey", []string{heron, "kite"})
	require.NoError(t, err)
	require.Equal(t, map[string]render.HubTunnel{heron: {Service: "dex-heron", Namespace: "giantswarm"}}, got)

	got, err = f.dexTunnels("heron", []string{"gopher"})
	require.NoError(t, err)
	require.Empty(t, got)

	got, err = f.dexTunnels("osprey", []string{"kite"})
	require.NoError(t, err)
	require.Empty(t, got)

	delete(f.Tunnelport.Consumers, "plover")
	_, err = f.dexTunnels("plover", []string{heron})
	require.ErrorContains(t, err, "consumer plover has no installNamespace")

	f.Tunnelport.Tunnels = append(f.Tunnelport.Tunnels, f.Tunnelport.Tunnels[1])
	f.Tunnelport.Tunnels[3].Name = "dex-heron-again"
	_, err = f.dexTunnels("osprey", []string{heron})
	require.ErrorContains(t, err, "two tunnels to heron's Dex for osprey")

	_, err = parseFleetTunnels("tunnelport: [\n")
	require.Error(t, err)
}

// The trust bundle's Secret is the target's tunnelport release's: a release
// that writes no bundle, or installs outside Dex's namespace where Dex could
// not mount it, is an error.
func TestTunnelBundleSecret(t *testing.T) {
	const path = "management-clusters/osprey/extras/tunnelport/helm-release.yaml"
	release := func(ns, bundle string) string {
		return "apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: tunnelport\nspec:\n  values:\n    installNamespace: " + ns + "\n" + bundle
	}
	secret, err := bundleSecret(path, release("giantswarm", "    trustBundle:\n      enabled: true\n      secretName: osprey-spiffe-bundle\n      tokenName: tunnelport-trust-bundle-token-osprey\n"))
	require.NoError(t, err)
	require.Equal(t, "osprey-spiffe-bundle", secret)

	_, err = bundleSecret(path, release("giantswarm", "    trustBundle:\n      enabled: false\n      secretName: osprey-spiffe-bundle\n"))
	require.ErrorContains(t, err, "writes no trust bundle")

	_, err = bundleSecret(path, release("tunnelport", "    trustBundle:\n      enabled: true\n      secretName: osprey-spiffe-bundle\n"))
	require.ErrorContains(t, err, "not Dex's namespace giantswarm")

	_, err = bundleSecret(path, "spec: [\n")
	require.ErrorContains(t, err, path)
}
