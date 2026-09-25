package tools

import (
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The commit records, per installation, the entries of the audience lists
// the live objects carry beside the render, and nothing for an installation
// that keeps none.
func TestCommitRecordsTheKeptAudiences(t *testing.T) {
	extra := plan.Kept{List: plan.ListExtraAudience, Entry: "kept-audience"}
	p := plan.Installation{Name: rowan, Files: []plan.File{
		{Kept: []plan.Kept{extra, {List: "resources", Entry: "./other/"}}},
		{Kept: []plan.Kept{{List: plan.ListTrustedPeers, Entry: "a-peer"}}},
	}}
	got := keptByInstallation(nil, p)
	if len(got) != 1 || len(got[rowan]) != 1 || got[rowan][0] != extra {
		t.Fatalf("kept by installation: %v", got)
	}
	if got := keptByInstallation(nil, plan.Installation{Name: birch}); got != nil {
		t.Fatalf("an installation that keeps nothing: %v", got)
	}
}
