package definitions_test

import (
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// TestEveryDefinitionParses holds every capability's data files to their
// shape: features.yaml, probes.yaml and removals.yaml of every definition
// load, and no file is empty. A removals.yaml no code path reads at run time
// is caught here, not on the first dry run that classifies with it.
func TestEveryDefinitionParses(t *testing.T) {
	caps, err := definitions.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	if len(caps) == 0 {
		t.Fatal("no capability under definitions/")
	}
	for _, c := range caps {
		t.Run(c, func(t *testing.T) {
			feats, err := definitions.Features(c)
			if err != nil {
				t.Fatalf("features.yaml: %v", err)
			}
			if len(feats) == 0 {
				t.Error("features.yaml: no feature")
			}
			probes, err := definitions.Probes(c)
			if err != nil {
				t.Fatalf("probes.yaml: %v", err)
			}
			if len(probes) == 0 {
				t.Error("probes.yaml: no probe")
			}
			removals, err := definitions.Removals(c)
			if err != nil {
				t.Fatalf("removals.yaml: %v", err)
			}
			if len(removals) == 0 {
				t.Error("removals.yaml: no removal")
			}
			seen := map[string]bool{}
			for _, r := range removals {
				if seen[r.Key] {
					t.Errorf("removals.yaml: key %q listed twice", r.Key)
				}
				seen[r.Key] = true
			}
		})
	}
}
