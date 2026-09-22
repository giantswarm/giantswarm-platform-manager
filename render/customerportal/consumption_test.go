package customerportal

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/labels"
)

// chartPins is where the agent-platform definition's render-consumption test
// pins the charts it renders; the portal's probes are held to the backstage
// chart at the same pin, so one Renovate bump moves both tests.
const chartPins = "../agentplatform/testdata/consumption/charts.yaml"

// TestRenderConsumption renders the backstage chart at its pin with the chart's
// own defaults — the definition sets none of the values the pod labels come
// from — and holds podSelector to the pods it renders: the portal's Deployment
// labels its pods so the selector matches them, and no other workload's pods
// match, so live-pods-running reads the portal's pods and only them. A chart
// that relabels its pods fails here, not as "no pod matches" on every
// installation. The test needs helm and the network and runs only with
// RENDER_CONSUMPTION=1.
func TestRenderConsumption(t *testing.T) {
	if os.Getenv("RENDER_CONSUMPTION") != "1" {
		t.Skip("set RENDER_CONSUMPTION=1 to render the backstage chart (needs helm and the network)")
	}
	selector, err := labels.Parse(podSelector)
	if err != nil {
		t.Fatalf("podSelector %q: %v", podSelector, err)
	}
	registry, version := backstagePin(t)
	dir := t.TempDir()
	helmRun(t, "pull", "oci://"+registry, "--version", version, "-d", dir)
	rendered := helmRun(t, "template", releaseName, filepath.Join(dir, "backstage-"+version+".tgz"), "--namespace", backstageNamespace)

	var matched, workloads []string
	dec := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var o map[string]any
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("decoding the rendered backstage chart: %v", err)
		}
		podLabels, ok := pathValue(o, "spec", "template", "metadata", "labels").(map[string]any)
		if !ok {
			continue
		}
		set := labels.Set{}
		for k, v := range podLabels {
			set[k], _ = v.(string)
		}
		workload := pathValue(o, "kind").(string) + " " + pathValue(o, "metadata", "name").(string)
		workloads = append(workloads, workload+": "+set.String())
		if selector.Matches(set) {
			matched = append(matched, workload)
		}
	}
	if len(workloads) == 0 {
		t.Fatalf("backstage %s renders no workload with a pod template", version)
	}
	if len(matched) != 1 || !strings.HasPrefix(matched[0], "Deployment ") {
		t.Fatalf("backstage %s: podSelector %q must match the portal's Deployment and nothing else, matched %v; the chart labels its pods:\n  %s",
			version, podSelector, matched, strings.Join(workloads, "\n  "))
	}
	t.Logf("backstage %s: %s carries %s", version, matched[0], podSelector)
}

// backstagePin reads the backstage chart's registry and version from chartPins.
func backstagePin(t *testing.T) (registry, version string) {
	t.Helper()
	raw, err := os.ReadFile(chartPins)
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Charts []struct {
			Name     string `yaml:"name"`
			Registry string `yaml:"registry"`
			Version  string `yaml:"version"`
		} `yaml:"charts"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Charts {
		if c.Name == "backstage" {
			return c.Registry, c.Version
		}
	}
	t.Fatalf("%s pins no backstage chart", chartPins)
	return "", ""
}

// helmRun executes helm with test-controlled arguments and fails the test with
// its stderr when it does not exit 0.
func helmRun(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("helm", args...) // #nosec G204 -- fixed binary, arguments are test-controlled paths and pins
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("helm %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return out
}

// pathValue walks a decoded document by keys; nil where the path does not exist.
func pathValue(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}
