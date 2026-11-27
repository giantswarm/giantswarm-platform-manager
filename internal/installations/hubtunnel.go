package installations

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// fleetTunnels are the fleet's tunnels as teleport-fleet's tunnelport values
// declare them (render.TeleportFleet, render.TunnelportValuesPath).
type fleetTunnels struct {
	Tunnelport struct {
		Tunnels []struct {
			Name      string `yaml:"name"`
			AppLabels struct {
				App     string `yaml:"app"`
				Cluster string `yaml:"cluster"`
			} `yaml:"appLabels"`
			Tokens []fleetToken `yaml:"tokens"`
		} `yaml:"tunnels"`
		Consumers map[string]struct {
			InstallNamespace string `yaml:"installNamespace"`
		} `yaml:"consumers"`
	} `yaml:"tunnelport"`
}

// fleetToken is a tunnel's token for one consumer.
type fleetToken struct {
	Consumer string `yaml:"consumer"`
}

// readFleetTunnels reads teleport-fleet's tunnelport values as the person.
// No file declares no tunnel.
func readFleetTunnels(ctx context.Context, c *gh.Client) (*fleetTunnels, error) {
	owner, repo, err := gh.SplitRepo(string(render.TeleportFleet))
	if err != nil {
		return nil, err
	}
	data, err := gh.ReadFile(ctx, c, owner, repo, render.TunnelportValuesPath)
	if errors.Is(err, gh.ErrNotFound) {
		return &fleetTunnels{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s:%s: %w", render.TeleportFleet, render.TunnelportValuesPath, err)
	}
	return parseFleetTunnels(data)
}

func parseFleetTunnels(data string) (*fleetTunnels, error) {
	var f fleetTunnels
	if err := yaml.Unmarshal([]byte(data), &f); err != nil {
		return nil, fmt.Errorf("%s:%s: %w", render.TeleportFleet, render.TunnelportValuesPath, err)
	}
	return &f, nil
}

// dexTunnels are the tunnels through which consumer reaches the Dex of each
// of hubs, keyed by hub: every tunnel whose app is a Dex
// (render.DexTunnelApp) on a hub's cluster with a token of consumer's, its
// Service the tunnel's name in consumer's install namespace. A consumer
// without an install namespace, or two tunnels to one hub's Dex, is an error.
func (f *fleetTunnels) dexTunnels(consumer string, hubs []string) (map[string]render.HubTunnel, error) {
	out := map[string]render.HubTunnel{}
	for _, t := range f.Tunnelport.Tunnels {
		hub := t.AppLabels.Cluster
		if t.AppLabels.App != render.DexTunnelApp || !slices.Contains(hubs, hub) ||
			!slices.ContainsFunc(t.Tokens, func(tok fleetToken) bool { return tok.Consumer == consumer }) {
			continue
		}
		if prev, ok := out[hub]; ok {
			return nil, fmt.Errorf("two tunnels to %s's Dex for %s: %s and %s", hub, consumer, prev.Service, t.Name)
		}
		ns := f.Tunnelport.Consumers[consumer].InstallNamespace
		if ns == "" {
			return nil, fmt.Errorf("tunnel %s: consumer %s has no installNamespace", t.Name, consumer)
		}
		out[hub] = render.HubTunnel{Service: t.Name, Namespace: ns}
	}
	return out, nil
}

// TunnelportReleasePath is the file of an installation's management-clusters
// repository that runs its tunnelport release, a Flux HelmRelease.
func TunnelportReleasePath(name string) string {
	return "management-clusters/" + name + "/extras/tunnelport/helm-release.yaml"
}

// tunnelportRelease is the part of an installation's tunnelport release the
// connector reads: where the release installs, and the trust bundle's Secret.
type tunnelportRelease struct {
	Spec struct {
		Values struct {
			InstallNamespace string `yaml:"installNamespace"`
			TrustBundle      struct {
				Enabled    bool   `yaml:"enabled"`
				SecretName string `yaml:"secretName"`
			} `yaml:"trustBundle"`
		} `yaml:"values"`
	} `yaml:"spec"`
}

// bundleSecret is the Secret the trust bundle of release (path names it in
// errors) is written to. Dex mounts it, so the release must install into
// Dex's namespace and write the bundle; anything else is an error.
func bundleSecret(path, release string) (string, error) {
	var r tunnelportRelease
	if err := yaml.Unmarshal([]byte(release), &r); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	v := r.Spec.Values
	switch {
	case !v.TrustBundle.Enabled || v.TrustBundle.SecretName == "":
		return "", fmt.Errorf("%s: the release writes no trust bundle (trustBundle.enabled, trustBundle.secretName)", path)
	case v.InstallNamespace != render.DexNamespace:
		return "", fmt.Errorf("%s: the release installs into %q, not Dex's namespace %s, so Dex cannot mount its trust bundle", path, v.InstallNamespace, render.DexNamespace)
	}
	return v.TrustBundle.SecretName, nil
}

// hubTunnels are the tunnels through which inst reaches the Dex of each of
// hubs, keyed by hub: fleet's (dexTunnels), each with the trust bundle's
// Secret of inst's tunnelport release. A tunnel the fleet declares without
// that release on record is an error.
func hubTunnels(ctx context.Context, c *gh.Client, inst Installation, hubs []string, fleet func() (*fleetTunnels, error)) (map[string]render.HubTunnel, error) {
	f, err := fleet()
	if err != nil {
		return nil, err
	}
	tunnels, err := f.dexTunnels(inst.Name, hubs)
	if err != nil || len(tunnels) == 0 {
		return nil, err
	}
	if inst.Repositories.ManagementClusters == "" {
		return nil, errors.New("tunnelled to a hub's Dex without a management-clusters repository on record")
	}
	owner, repo, err := gh.SplitRepo(inst.Repositories.ManagementClusters)
	if err != nil {
		return nil, err
	}
	path := TunnelportReleasePath(inst.Name)
	release, err := gh.ReadFile(ctx, c, owner, repo, path)
	if err != nil {
		return nil, fmt.Errorf("tunnelled to a hub's Dex: the tunnelport release %s in %s: %w", path, inst.Repositories.ManagementClusters, err)
	}
	secret, err := bundleSecret(path, release)
	if err != nil {
		return nil, err
	}
	for hub, t := range tunnels {
		t.BundleSecret = secret
		tunnels[hub] = t
	}
	return tunnels, nil
}
