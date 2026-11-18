package plan

import (
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

func TestDexVersionDependentCommitRefusesAnUnknownVersion(t *testing.T) {
	const reason = "the OCIRepository dex-app selects a semver constraint, not an exact version"
	record := &installations.Record{DexAppVersionError: reason}
	planned := Installation{DexClients: []DexClient{{SecretRef: "client-secret"}}}
	if refusal := planned.DexAppRefusal(record); !strings.Contains(refusal, reason) {
		t.Fatalf("no prerequisite refusal: %q", refusal)
	}
	if refusal := (Installation{}).DexAppRefusal(record); refusal != "" {
		t.Fatalf("a plan without referenced clients is held: %s", refusal)
	}
}
