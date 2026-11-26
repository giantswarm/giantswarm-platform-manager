package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// Invented installations of the disable's references.
const (
	dTarget  = "kestrel"
	dConfigs = "giantswarm/oak-configs"
	dHubB    = "osprey"
	dReg     = "gopher"
)

func disableReport(name string) installations.Report {
	return installations.Report{Installation: installations.Installation{Name: name, Customer: "oak"}, Federation: &installations.Federation{Hubs: []string{}, Targets: []installations.FederatedTarget{}}}
}

// Every reference that still depends on a capability is named: the
// registry's hub, the targets a hub's muster brokers into, a portal that
// proxies the platform, the installations a hosted portal shows, the hubs
// that broker into the MCP servers, a value paired with another
// installation that no capability that stays carries.
func TestDisableReferences(t *testing.T) {
	paired := plan.Disablement{Files: []plan.Removal{{Path: "secrets/exchange.yaml", Change: plan.ChangeDelete, Pairings: []string{dReg + ":management-clusters/gopher/x.yaml"}}}}
	cases := []struct {
		name       string
		report     func() installations.Report
		capability string
		portals    []installations.Portal
		d          plan.Disablement
		kept       map[string]bool
		want       []string
	}{
		{name: "nothing depends on it", report: func() installations.Report { return disableReport(dTarget) }, capability: installations.AgentPlatform},
		{name: "the registry's hub", report: func() installations.Report { return disableReport(dReg) }, capability: installations.ClusterMCPServers,
			want: []string{"gopher is the registry's hub"}},
		{name: "a hub's targets", capability: installations.AgentPlatform, report: func() installations.Report {
			r := disableReport(dHubB)
			r.Federation.Targets = []installations.FederatedTarget{{Installation: dTarget}}
			return r
		}, want: []string{"osprey's muster brokers into kestrel"}},
		{name: "a portal proxies the platform", capability: installations.AgentPlatform, report: func() installations.Report { return disableReport(dTarget) },
			portals: []installations.Portal{{Host: dHubB, PlatformProxied: []string{dTarget}}, {Host: dReg, Installations: []string{dTarget}}},
			want:    []string{"the portal on osprey proxies kestrel's agent platform"}},
		{name: "a hosted portal shows others", capability: installations.CustomerPortal, report: func() installations.Report {
			r := disableReport(dHubB)
			r.Hosted = &installations.HostedPortal{Installations: []installations.FederatedInstallation{{Name: dTarget}}}
			return r
		}, want: []string{"the portal on osprey shows kestrel"}},
		{name: "hubs broker into the MCP servers", capability: installations.ClusterMCPServers, report: func() installations.Report {
			r := disableReport(dTarget)
			r.Federation.Hubs = []string{dHubB, dReg}
			return r
		}, want: []string{"osprey, gopher broker into kestrel's MCP servers"}},
		{name: "a pairing no capability that stays carries", capability: installations.AgentPlatform, report: func() installations.Report { return disableReport(dTarget) }, d: paired,
			want: []string{"kestrel shares a value with gopher, which holds the other side in management-clusters/gopher/x.yaml"}},
		{name: "a pairing a capability that stays carries", capability: installations.AgentPlatform, report: func() installations.Report { return disableReport(dTarget) }, d: paired,
			kept: map[string]bool{dReg + ":management-clusters/gopher/x.yaml": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := disableReferences(dReg, tc.report(), tc.capability, tc.portals, tc.d, tc.kept)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q", got)
			}
			for i, w := range tc.want {
				if !strings.HasPrefix(got[i], w) {
					t.Errorf("reference %d: %q, want it to start %q", i, got[i], w)
				}
			}
		})
	}
}

// A commit is refused while a file is unreadable, a reference stands, or
// the marker stays; else it goes ahead.
func TestDisableRefusal(t *testing.T) {
	def, _ := installations.FindCapability(installations.AgentPlatform)
	r := disableReport(dTarget)
	r.Repositories = installations.Repositories{Configs: dConfigs, ManagementClusters: "giantswarm/oak-management-clusters"}
	marker := def.EnabledMarker(dTarget)
	deletesMarker := plan.Disablement{Files: []plan.Removal{{Repository: dConfigs, Path: marker, Change: plan.ChangeDelete}}}
	cases := []struct {
		name string
		out  DisableResult
		want string
	}{
		{name: "goes ahead", out: DisableResult{Plan: deletesMarker}},
		{name: "unreadable", out: DisableResult{Plan: plan.Disablement{Files: []plan.Removal{{Repository: dConfigs, Path: "x", Change: plan.ChangeUnknown, Error: "refused"}}}}, want: "1 file(s) could not be read as you"},
		{name: "a reference", out: DisableResult{Plan: deletesMarker, References: []string{"the portal on osprey proxies kestrel's agent platform"}}, want: "one reference still depends on agent-platform on kestrel"},
		{name: "the marker stays", out: DisableResult{Plan: plan.Disablement{Stays: []plan.Stay{{Repository: dConfigs, Path: marker, Why: "rendered by cluster-mcp-servers"}}}}, want: "the capability's marker " + marker + " stays (rendered by cluster-mcp-servers)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := disableRefusal(def, r, &tc.out)
			if tc.want == "" && got != "" || !strings.HasPrefix(got, tc.want) {
				t.Fatalf("got %q", got)
			}
		})
	}
}

// The resync reads a disable's marker gone as the disable done: the action
// and its stage are disabled, and the orphans stay the checklist its commit
// recorded — never the render's objects, which name what the capabilities
// that stay keep running.
func TestResyncDisablesWhenTheMarkerIsGone(t *testing.T) {
	def, _ := installations.FindCapability(installations.AgentPlatform)
	checklist := []actions.Orphan{{Installation: dTarget, Kind: "HelmRelease", Namespace: "flux-giantswarm", Name: "agent-platform"}}
	a := &actions.Action{Name: "disable-kestrel-abc123", Spec: actions.Spec{Kind: actions.KindDisable, Capability: def.Name, Installations: []string{dTarget}},
		Status: actions.Status{State: actions.StateRollingOut, Orphans: checklist}}
	status := a.Status
	text := remove(&status, a, def, []revertedStage{{installation: dTarget, repository: dConfigs, path: def.EnabledMarker(dTarget)}})
	if status.State != actions.StateDisabled || status.Rollout.Installations[0].State != actions.StateDisabled {
		t.Fatalf("state %s, stage %s", status.State, status.Rollout.Installations[0].State)
	}
	if len(status.Orphans) != 1 || status.Orphans[0] != checklist[0] {
		t.Fatalf("orphans %v", status.Orphans)
	}
	if !strings.Contains(text, "is disabled on *kestrel*") || !strings.Contains(text, "HelmRelease flux-giantswarm/agent-platform") {
		t.Fatalf("text %q", text)
	}
	if !actions.Terminal(actions.StateDisabled) {
		t.Fatal("disabled is not terminal")
	}
}

// The watch answers a disabled action with the objects to delete, and
// refuses one not disabled yet, naming what it waits for.
func TestWatchDisable(t *testing.T) {
	a := &actions.Action{Name: "disable-kestrel-abc123", Spec: actions.Spec{Kind: actions.KindDisable, Capability: installations.AgentPlatform, Installations: []string{dTarget}},
		Status: actions.Status{State: actions.StatePendingApproval}}
	if _, err := watchDisable(a, ""); err == nil || !strings.Contains(err.Error(), "a disable rolls nothing out") {
		t.Fatalf("pending: %v", err)
	}
	a.Status.State = actions.StateDisabled
	a.Status.Orphans = []actions.Orphan{{Installation: dTarget, Kind: "Namespace", Name: kagentNS}}
	out, err := watchDisable(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if w := out.(WatchResult); w.State != actions.StateDisabled || w.Next != "delete on kestrel, in this order: Namespace kagent" {
		t.Fatalf("%+v", w)
	}
}

// A disable's pull requests are titled like every action's.
func TestDisablePRTitle(t *testing.T) {
	if got := prTitle(actions.KindDisable, dTarget, installations.AgentPlatform, "disable-kestrel-abc123", ""); got != "feat(kestrel): disable agent-platform (disable-kestrel-abc123)" {
		t.Fatalf("%q", got)
	}
}

// A reconcile's checklist, recorded at commit, names objects its plan took
// off the record and the render no longer names: once the fileset is gone
// again, the removed action keeps them before the render's own objects.
func TestRemoveKeepsTheChecklistTheCommitRecorded(t *testing.T) {
	def, _ := installations.FindCapability(installations.AgentPlatform)
	checklist := actions.Orphan{Installation: dTarget, Kind: "RemoteApp", Namespace: installations.AgentPlatform, Name: "kagent-glean"}
	a := &actions.Action{Name: "reconcile-kestrel-abc123", Spec: actions.Spec{Kind: actions.KindReconcile, Capability: def.Name, Installations: []string{dTarget}},
		Status: actions.Status{State: actions.StateEnabled, Orphans: []actions.Orphan{checklist}}}
	status := a.Status
	remove(&status, a, def, []revertedStage{{installation: dTarget, repository: dConfigs, path: def.EnabledMarker(dTarget)}})
	if status.State != actions.StateRemoved || len(status.Orphans) == 0 || status.Orphans[0] != checklist {
		t.Fatalf("state %s, orphans %+v", status.State, status.Orphans)
	}
}

// A wave removed while one stage is only partly merged: kestrel's stage is
// merged and gone again; heron's pull request that takes the RemoteApp off
// the record is merged while its other pull request is still open; wren's
// pull request is open. The removed action keeps heron's recorded orphan —
// it is off the record and on the cluster — and none of wren's, which took
// nothing off the record.
func TestRemoveKeepsTheOrphansOfAPartlyMergedStage(t *testing.T) {
	const heron, wren, heronMCs = "heron", "wren", "giantswarm/heron-management-clusters"
	def, _ := installations.FindCapability(installations.AgentPlatform)
	partly := actions.Orphan{Installation: heron, Kind: "RemoteApp", Namespace: installations.AgentPlatform, Name: "kagent-glean", Stage: heron, Repository: heronMCs}
	open := actions.Orphan{Installation: wren, Kind: "Secret", Namespace: installations.AgentPlatform, Name: "old-credentials", Stage: wren, Repository: "giantswarm/wren-management-clusters"}
	a := &actions.Action{Name: "reconcile-wave-abc123", Spec: actions.Spec{Kind: actions.KindReconcile, Capability: def.Name, Installations: []string{dTarget, heron, wren}},
		Status: actions.Status{State: actions.StateRollingOut, Orphans: []actions.Orphan{partly, open}, PullRequests: []actions.PullRequest{
			{Installation: dTarget, Repository: dConfigs, Number: 1, State: actions.PullRequestMerged},
			{Installation: heron, Repository: heronMCs, Number: 2, State: actions.PullRequestMerged},
			{Installation: heron, Repository: "giantswarm/heron-configs", Number: 3, State: actions.PullRequestOpen},
			{Installation: wren, Repository: "giantswarm/wren-management-clusters", Number: 4, State: actions.PullRequestOpen},
		}}}
	status := a.Status
	remove(&status, a, def, []revertedStage{{installation: dTarget, repository: dConfigs, path: def.EnabledMarker(dTarget)}})
	require.Equal(t, actions.StateRemoved, status.State)
	require.Contains(t, status.Orphans, partly)
	require.NotContains(t, status.Orphans, open)
}
