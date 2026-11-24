package installations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// ChartConfig reads the config of the Helm chart at an oci:// URL and a
// reference (a tag, or a digest sha256:…): the chart's metadata, its
// Chart.yaml as JSON, which names the charts it depends on.
type ChartConfig func(ctx context.Context, ociURL, ref string) ([]byte, error)

// RegistryChartConfig reads a chart's config through the OCI distribution
// API, anonymously, as RegistryTags lists tags.
func RegistryChartConfig(client *http.Client) ChartConfig {
	return registryChartConfig(client, false)
}

func registryChartConfig(client *http.Client, insecure bool) ChartConfig {
	return func(ctx context.Context, ociURL, ref string) ([]byte, error) {
		repository, ok := strings.CutPrefix(ociURL, "oci://")
		if !ok || !strings.Contains(repository, "/") {
			return nil, fmt.Errorf("%q is no oci://<registry>/<repository> URL", ociURL)
		}
		separator := ":"
		if strings.HasPrefix(ref, "sha256:") {
			separator = "@"
		}
		var opts []name.Option
		if insecure {
			opts = append(opts, name.Insecure)
		}
		r, err := name.ParseReference(repository+separator+ref, opts...)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", ociURL, ref, err)
		}
		if client.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, client.Timeout)
			defer cancel()
		}
		transport := client.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}
		img, err := remote.Image(r, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous), remote.WithTransport(transport))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", r, err)
		}
		config, err := img.RawConfigFile()
		if err != nil {
			return nil, fmt.Errorf("%s: the chart's config: %w", r, err)
		}
		return config, nil
	}
}

// chartSource is the chart a Flux HelmRelease on record takes from its
// OCIRepository: the OCIRepository's namespace/name, whether the manifest
// holds it, and the repository URL and reference it names.
type chartSource struct {
	repository               string
	found                    bool
	url, tag, semver, digest string
}

// clusterSubchartVersion is the version of the cluster subchart
// (agentplatform.ClusterSubchart) the chart depends on, read from its config;
// empty for a chart that does not depend on it. Flux pulls a digest over a
// semver range over a tag; a semver range is not resolved here, and the
// OCIRepository must be on record.
func (s chartSource) clusterSubchartVersion(ctx context.Context, chart ChartConfig) (string, error) {
	if !s.found {
		return "", fmt.Errorf("its chart comes from OCIRepository %s, which the manifest does not hold", s.repository)
	}
	ref := s.digest
	switch {
	case ref != "":
	case s.semver != "":
		return "", fmt.Errorf("OCIRepository %s follows the semver range %q, which is not resolved to a chart", s.repository, s.semver)
	case s.tag != "":
		ref = s.tag
	default:
		return "", fmt.Errorf("OCIRepository %s names no tag or digest", s.repository)
	}
	data, err := chart(ctx, s.url, ref)
	if err != nil {
		return "", fmt.Errorf("OCIRepository %s: %w", s.repository, err)
	}
	var config struct {
		Dependencies []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("OCIRepository %s: the config of %s %s: %w", s.repository, s.url, ref, err)
	}
	for _, d := range config.Dependencies {
		if d.Name == agentplatform.ClusterSubchart {
			return d.Version, nil
		}
	}
	return "", nil
}
