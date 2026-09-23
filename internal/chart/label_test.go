package chart

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// labelValue is a valid Kubernetes label value: empty, or alphanumeric at
// both ends with "-", "_" and "." between. Its length, at most 63, is
// checked apart.
var labelValue = regexp.MustCompile(`^(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])?$`)

// chartLabel is a helm.sh/chart label in a rendered manifest.
var chartLabel = regexp.MustCompile(`(?m)^\s*helm\.sh/chart:\s*"?([^"\n]*)"?\s*$`)

// TestChartLabelIsALabelValue renders the chart for a release version and
// for long versions whose 63-character cut of "<name>-<version>" lands on
// ".", on the "_" a "+" becomes and on a run like "--." -- a branch build's
// <version>-dev.<branch>.<date>.<time>.<sha>, or the <version>+<digest>
// helm-controller installs -- and asserts that every helm.sh/chart label is
// a valid label value, which the API server otherwise refuses for every
// labelled object. The version is set by packaging, since `helm template
// --version` does not apply to a chart directory.
func TestChartLabelIsALabelValue(t *testing.T) {
	cases := []struct {
		version string
		want    string
	}{
		{version: "0.1.0", want: "giantswarm-platform-manager-0.1.0"},
		{
			version: "0.10.12-dev.re.2026-09-22.14-54-24.h1a2b3c4",
			want:    "giantswarm-platform-manager-0.10.12-dev.re.2026-09-22.14-54-24",
		},
		{
			version: "0.10.12-dev.re.2026-09-22.14-54-24+h1a2b3c4",
			want:    "giantswarm-platform-manager-0.10.12-dev.re.2026-09-22.14-54-24",
		},
		{
			version: "0.10.12-dev.re.2026-09-22.14-54---.h1a2b3c4",
			want:    "giantswarm-platform-manager-0.10.12-dev.re.2026-09-22.14-54",
		},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			manifest, failure := helmTemplate(t, pack(t, tc.version))
			if failure != "" {
				t.Fatalf("render: %s", failure)
			}
			labels := chartLabel.FindAllSubmatch(manifest, -1)
			if len(labels) == 0 {
				t.Fatal("no helm.sh/chart label rendered")
			}
			for _, m := range labels {
				got := string(m[1])
				if len(got) > 63 || !labelValue.MatchString(got) {
					t.Errorf("helm.sh/chart %q is not a valid label value", got)
				}
				if got != tc.want {
					t.Errorf("helm.sh/chart = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// pack packages the chart at the version given and answers the archive.
func pack(t *testing.T, version string) string {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not on PATH: the chart render proofs run in CI's render-consumption job")
	}
	dir := t.TempDir()
	if out, err := exec.Command("helm", "package", chartDir, "--version", version, "-d", dir).CombinedOutput(); err != nil { // #nosec G204 -- fixed binary, arguments are the test's own chart and versions
		t.Fatalf("helm package: %v\n%s", err, out)
	}
	return filepath.Join(dir, "giantswarm-platform-manager-"+version+".tgz")
}

// helmTemplate runs helm template of a packaged chart over the test values
// and answers the manifest; a failed render answers helm's message instead.
func helmTemplate(t *testing.T, archive string) ([]byte, string) {
	t.Helper()
	cmd := exec.Command("helm", "template", "giantswarm-platform-manager", archive, "-f", testValues) // #nosec G204 -- fixed binary, arguments are the test's own archive and values file
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, stderr.String()
	}
	return stdout.Bytes(), ""
}
