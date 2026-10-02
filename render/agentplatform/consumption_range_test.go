package agentplatform

import (
	"errors"
	"strings"
	"testing"
)

// TestOutsideRangeNamesThePin proves the render-consumption test's range check
// without the network: a child bumped alone past the range its meta chart pin
// declares, and a meta chart bump that moved a child's range past its pin, each
// fail with the version to pin, the newest release inside the range.
func TestOutsideRangeNamesThePin(t *testing.T) {
	released := []string{"1.1.0", "1.2.0", "1.2.1", "1.2.3", "1.2.4-dev.branch.h0abc123", "1.3.0", "latest"}
	tags := func(string) ([]string, error) { return released, nil }
	kagent := func(version string) pin {
		return pin{Name: "kagent", Line: lineFour, Registry: "gsoci.azurecr.io/giantswarm/kagent/helm/kagent", Version: version}
	}
	for _, tc := range []struct {
		name, version, rng, want string
	}{
		{name: "inside the range", version: "1.2.1", rng: ">=1.2.0 <1.3.0"},
		{name: "child bumped alone", version: "1.3.0", rng: ">=1.2.0 <1.3.0", want: "pin 1.2.3, the newest release inside it"},
		{name: "meta chart moved the range", version: "1.1.0", rng: ">=1.2.0 <1.3.0", want: "pin 1.2.3, the newest release inside it"},
		{name: "prerelease range", version: "1.1.0", rng: ">=1.2.0-0 <1.3.0-0", want: "pin 1.2.3, the newest release inside it"},
		{name: "nothing released inside", version: "1.1.0", rng: ">=2.0.0 <3.0.0", want: "no release of gsoci.azurecr.io/giantswarm/kagent/helm/kagent is inside it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := outsideRange(kagent(tc.version), "kagent", tc.rng, lineFour, tags)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("pin %s inside %s failed: %s", tc.version, tc.rng, got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("pin %s outside %s: want the failure to say %q, got %q", tc.version, tc.rng, tc.want, got)
			}
		})
	}

	failing := func(string) ([]string, error) { return nil, errors.New("unauthorized") }
	if got := outsideRange(kagent("1.3.0"), "kagent", ">=1.2.0 <1.3.0", lineFour, failing); !strings.Contains(got, "listing the releases of gsoci.azurecr.io/giantswarm/kagent/helm/kagent: unauthorized") {
		t.Fatalf("a registry that cannot list: want the listing error named, got %q", got)
	}
}
