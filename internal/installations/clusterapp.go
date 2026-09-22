package installations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// ClusterAppManifestPath is where the installation's management-clusters
// repository keeps the cluster App the management cluster is created from
// and the ConfigMap with its values: the record of the cluster itself.
func ClusterAppManifestPath(name string) string {
	return "management-clusters/" + name + "/cluster-app-manifests.yaml"
}

// ReleasesRepository holds the fleet's releases: one directory per provider,
// one release.yaml per version naming the version of every component the
// release ships, the provider's cluster chart among them. A release-based
// cluster App — every CAPA and CAPZ management cluster — names its release in
// its values (global.release.version) and carries no chart version of its
// own; the chart's version is the release's.
const ReleasesRepository = "giantswarm/releases"

// releaseDirectories is, per provider cluster chart, the directory of
// ReleasesRepository that carries its releases.
var releaseDirectories = map[string]string{
	"cluster-aws":            "capa",
	"cluster-azure":          "azure",
	"cluster-cloud-director": "cloud-director",
	"cluster-vsphere":        "vsphere",
	"cluster-eks":            "eks",
	"cluster-proxmox":        "proxmox",
}

// ErrRelease is a release the cluster App on record names that could not be
// read as the person or does not list the App's chart. It is a fact's error,
// not a condition of reading the installation: the fact stays false with the
// reason on the report, and the comparison still runs.
var ErrRelease = errors.New("the release on record")

// ReleasePath is the release.yaml of version in the releases directory of the
// chart's provider; false for a chart without one.
func ReleasePath(chart, version string) (string, bool) {
	dir, ok := releaseDirectories[chart]
	if !ok {
		return "", false
	}
	return dir + "/v" + strings.TrimPrefix(version, "v") + "/release.yaml", true
}

// readAs reads a repository's file on its default branch as the person c
// acts as.
func readAs(c *gh.Client) Reader {
	return func(ctx context.Context, repository, path string) (string, error) {
		owner, repo, err := gh.SplitRepo(repository)
		if err != nil {
			return "", err
		}
		return gh.ReadFile(ctx, c, owner, repo, path)
	}
}

// readPodCertificateRequest reads whether inst's cluster serves
// certificates.k8s.io/v1beta1 PodCertificateRequest, from the cluster App on
// record as the person: false for an installation without the manifest (the
// record says nothing, so nothing says yes); a manifest that cannot be read
// or parsed is an error of the report. Where the App carries no chart version
// and the chart's default decides, the version is the one the release the
// values name lists for the chart (ReleasesRepository); a release that cannot
// be read or does not list the chart is ErrRelease, the fact false.
func readPodCertificateRequest(ctx context.Context, read Reader, inst Installation) (bool, error) {
	path := ClusterAppManifestPath(inst.Name)
	data, err := read(ctx, inst.Repositories.ManagementClusters, path)
	if errors.Is(err, gh.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("the cluster App on record: %w", err)
	}
	app, err := parseClusterApp(data)
	if err != nil {
		return false, fmt.Errorf("the cluster App on record: %s in %s: %w", path, inst.Repositories.ManagementClusters, err)
	}
	if app == nil || !app.listsEnableGates() {
		return false, nil
	}
	if !app.readsChart() {
		return true, nil
	}
	version := app.version
	if version == "" && app.release != "" {
		if version, err = releaseChartVersion(ctx, read, app.chart, app.release); err != nil {
			return false, fmt.Errorf("%w: %s in %s names release %s: %w", ErrRelease, path, inst.Repositories.ManagementClusters, app.release, err)
		}
	}
	return agentplatform.ClusterChartHasPodCertificateRequest(app.chart, version), nil
}

// releaseChartVersion is the version of chart that release ships, read from
// ReleasesRepository as the person.
func releaseChartVersion(ctx context.Context, read Reader, chart, release string) (string, error) {
	path, ok := ReleasePath(chart, release)
	if !ok {
		return "", fmt.Errorf("no releases directory is known for the chart %s", chart)
	}
	data, err := read(ctx, ReleasesRepository, path)
	if errors.Is(err, gh.ErrNotFound) {
		return "", fmt.Errorf("%s is not in %s", path, ReleasesRepository)
	}
	if err != nil {
		return "", err
	}
	var doc struct {
		Spec struct {
			Components []struct {
				Name    string `yaml:"name"`
				Version string `yaml:"version"`
			} `yaml:"components"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(data), &doc); err != nil {
		return "", fmt.Errorf("decode %s in %s: %w", path, ReleasesRepository, err)
	}
	for _, c := range doc.Spec.Components {
		if c.Name == chart {
			return c.Version, nil
		}
	}
	return "", fmt.Errorf("%s in %s lists no component %s", path, ReleasesRepository, chart)
}

// clusterAppDocument is the App document of the manifest: the chart and its
// version, and the ConfigMap the values come from.
type clusterAppDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Name       string `yaml:"name"`
		Version    string `yaml:"version"`
		UserConfig struct {
			ConfigMap struct {
				Name string `yaml:"name"`
			} `yaml:"configMap"`
		} `yaml:"userConfig"`
	} `yaml:"spec"`
}

// clusterConfigMap is a ConfigMap document of the manifest with the chart
// values as YAML text.
type clusterConfigMap struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data struct {
		Values string `yaml:"values"`
	} `yaml:"data"`
}

// featureGate is one entry of a component's featureGates list in the cluster
// chart's values.
type featureGate struct {
	Name    string `yaml:"name"`
	Enabled bool   `yaml:"enabled"`
}

// gated is a kubeadm component's part of the values: its featureGates list,
// nil where the record sets none (the chart's default list stands) and a
// list where it does (which replaces the default — Helm merges no lists).
type gated struct {
	FeatureGates []featureGate `yaml:"featureGates"`
}

// clusterValues is the part of the cluster App's values the record reads:
// the release the App runs, and the feature gates per kubeadm component
// under the cluster chart's internal.advancedConfiguration.
type clusterValues struct {
	Global struct {
		Release struct {
			Version string `yaml:"version"`
		} `yaml:"release"`
	} `yaml:"global"`
	Cluster struct {
		Internal struct {
			AdvancedConfiguration struct {
				ControlPlane struct {
					APIServer         gated `yaml:"apiServer"`
					ControllerManager gated `yaml:"controllerManager"`
				} `yaml:"controlPlane"`
				Kubelet gated `yaml:"kubelet"`
			} `yaml:"advancedConfiguration"`
		} `yaml:"internal"`
	} `yaml:"cluster"`
}

// components are the values' three components, in the order the definition
// names them (agentplatform.PodCertificateRequestComponents).
func (v clusterValues) components() []gated {
	ac := v.Cluster.Internal.AdvancedConfiguration
	return []gated{ac.ControlPlane.APIServer, ac.ControlPlane.ControllerManager, ac.Kubelet}
}

// clusterApp is what the record says about the cluster: the chart, the
// version the App carries (empty for a release-based App), the release the
// values name (empty for an App pinned to a chart version) and the values.
type clusterApp struct {
	chart, version, release string
	values                  clusterValues
}

// parseClusterApp reads the manifest: the App of a cluster chart with the
// values of the ConfigMap it names. Nil without such an App.
func parseClusterApp(manifest string) (*clusterApp, error) {
	var app *clusterAppDocument
	configMaps := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode: %w", err)
		}
		var kind struct {
			Kind string `yaml:"kind"`
		}
		if err := node.Decode(&kind); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
		switch kind.Kind {
		case kindApp:
			var a clusterAppDocument
			if err := node.Decode(&a); err != nil {
				return nil, fmt.Errorf("decode the App: %w", err)
			}
			if strings.HasPrefix(a.Spec.Name, "cluster-") && app == nil {
				app = &a
			}
		case "ConfigMap":
			var cm clusterConfigMap
			if err := node.Decode(&cm); err != nil {
				return nil, fmt.Errorf("decode the ConfigMap: %w", err)
			}
			if cm.Data.Values != "" {
				configMaps[cm.Metadata.Name] = cm.Data.Values
			}
		}
	}
	if app == nil {
		return nil, nil
	}
	raw, ok := configMaps[app.Spec.UserConfig.ConfigMap.Name]
	if !ok && len(configMaps) == 1 {
		// The one ConfigMap with values, whatever the App names.
		raw = slices.Collect(maps.Values(configMaps))[0]
	}
	a := &clusterApp{chart: app.Spec.Name, version: app.Spec.Version}
	if err := yaml.Unmarshal([]byte(raw), &a.values); err != nil {
		return nil, fmt.Errorf("decode the cluster App's values: %w", err)
	}
	a.release = a.values.Global.Release.Version
	return a, nil
}

// listsEnableGates says whether every component whose featureGates list the
// record sets enables every gate Agent Substrate needs: a list on record
// replaces the chart's default, so one that lacks a gate says no whatever the
// chart, and is answered before any chart or release is read.
func (a *clusterApp) listsEnableGates() bool {
	for _, component := range a.values.components() {
		if component.FeatureGates != nil && !enablesGates(component.FeatureGates, agentplatform.PodCertificateRequestGates) {
			return false
		}
	}
	return true
}

// readsChart says whether the chart's default list decides for any
// component: one whose featureGates list the record does not set. A record
// that sets every list is read without the chart or its release.
func (a *clusterApp) readsChart() bool {
	return slices.ContainsFunc(a.values.components(), func(c gated) bool { return c.FeatureGates == nil })
}

// enablesGates says whether every wanted gate is in the list, enabled.
func enablesGates(list []featureGate, wanted []string) bool {
	for _, name := range wanted {
		if !slices.ContainsFunc(list, func(g featureGate) bool { return g.Name == name && g.Enabled }) {
			return false
		}
	}
	return true
}
