package installations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/google/go-github/v92/github"
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

// readPodCertificateRequest reads whether inst's cluster serves
// certificates.k8s.io/v1beta1 PodCertificateRequest, from the cluster App on
// record as the person: false for an installation without the manifest (the
// record says nothing, so nothing says yes); a manifest that cannot be read
// or parsed is an error of the report.
func readPodCertificateRequest(ctx context.Context, c *github.Client, inst Installation) (bool, error) {
	owner, repo, err := gh.SplitRepo(inst.Repositories.ManagementClusters)
	if err != nil {
		return false, err
	}
	data, err := gh.ReadFile(ctx, c, owner, repo, ClusterAppManifestPath(inst.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("the cluster App on record: %w", err)
	}
	served, err := podCertificateRequest(data)
	if err != nil {
		return false, fmt.Errorf("the cluster App on record: %s in %s: %w", ClusterAppManifestPath(inst.Name), inst.Repositories.ManagementClusters, err)
	}
	return served, nil
}

// clusterApp is the App document of the manifest: the chart and its version,
// and the ConfigMap the values come from.
type clusterApp struct {
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
// the feature gates per kubeadm component under the cluster chart's
// internal.advancedConfiguration.
type clusterValues struct {
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

// podCertificateRequest derives from the manifest whether the cluster serves
// the API: for each of the three components, the featureGates list on record
// enables every gate Agent Substrate needs, or the record sets none and the
// chart's version enables them by default. A manifest without an App
// document of a cluster chart, or without values, says no.
func podCertificateRequest(manifest string) (bool, error) {
	var app *clusterApp
	configMaps := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var node yaml.Node
		if err := dec.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return false, fmt.Errorf("decode: %w", err)
		}
		var kind struct {
			Kind string `yaml:"kind"`
		}
		if err := node.Decode(&kind); err != nil {
			return false, fmt.Errorf("decode: %w", err)
		}
		switch kind.Kind {
		case kindApp:
			var a clusterApp
			if err := node.Decode(&a); err != nil {
				return false, fmt.Errorf("decode the App: %w", err)
			}
			if strings.HasPrefix(a.Spec.Name, "cluster-") && app == nil {
				app = &a
			}
		case "ConfigMap":
			var cm clusterConfigMap
			if err := node.Decode(&cm); err != nil {
				return false, fmt.Errorf("decode the ConfigMap: %w", err)
			}
			if cm.Data.Values != "" {
				configMaps[cm.Metadata.Name] = cm.Data.Values
			}
		}
	}
	if app == nil {
		return false, nil
	}
	raw, ok := configMaps[app.Spec.UserConfig.ConfigMap.Name]
	if !ok && len(configMaps) == 1 {
		// The one ConfigMap with values, whatever the App names.
		raw = slices.Collect(maps.Values(configMaps))[0]
	}
	var values clusterValues
	if err := yaml.Unmarshal([]byte(raw), &values); err != nil {
		return false, fmt.Errorf("decode the cluster App's values: %w", err)
	}
	byDefault := agentplatform.ClusterChartHasPodCertificateRequest(app.Spec.Name, app.Spec.Version)
	for _, component := range values.components() {
		if component.FeatureGates == nil {
			if !byDefault {
				return false, nil
			}
			continue
		}
		if !enablesGates(component.FeatureGates, agentplatform.PodCertificateRequestGates) {
			return false, nil
		}
	}
	return true, nil
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
