package agentplatform

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Governance is the person's choice of the platform's governance (Governing
// tool calls, giantswarm/giantswarm#38902): the mode (governance.mode, the
// meta chart's top-level governance) and the servers muster governs with
// their safe lists (governance.governedServers, the meta chart's top-level
// governedServers), each read back from the configmap patch on record so a
// reconcile keeps it. Only admins change it, through an Action a second
// person approves, on test installations too (internal/tools).
type Governance struct {
	// Mode is trusted, the default and not written, or governed.
	Mode string `json:"mode"`
	// GovernedServers override the meta chart's governedServers by source:
	// a nil entry drops the source (written as null, so the chart governs
	// nothing for it); an entry's role or safe list left out keeps the
	// chart's (Helm merges the maps, a list replaces).
	GovernedServers map[string]*GovernedServer `json:"governedServers"`
}

// GovernedServer is one source of the meta chart's governedServers: the
// role whose floors hold on its servers and the safe list, the tools agents
// call unasked on governed.
type GovernedServer struct {
	Role string    `json:"role,omitempty"`
	Safe *[]string `json:"safe,omitempty"`
}

// The governance modes.
const (
	GovernanceTrusted  = "trusted"
	GovernanceGoverned = "governed"
)

// GovernanceChart is the first meta chart that takes governance and
// governedServers and renders muster's governance configuration from them
// (giantswarm/agent-platform's tool-approval work). UNSET until that release
// exists (giantswarm/giantswarm#38902 decision #89): a guessed floor would
// write the keys for a chart that refuses or ignores them, so a governed
// platform would not be governed. While it is unset every governance input
// is refused (fail closed). Set it to the release once it is cut; tests set
// it to exercise the render.
var GovernanceChart = ""

// GovernanceMuster is the first muster release that reads the governance
// configuration (the meta chart's agent-platform.muster.governanceFloor).
// UNSET for the same reason: governed with a muster hold is refused while it
// is unset, since the meta chart fails the render below it.
var GovernanceMuster = ""

// The inputs, by their schema paths.
const (
	inputGovernanceMode    = "governance.mode"
	inputGovernanceServers = "governance.governedServers"
)

// roleModelManager is the model-manager's governance role.
const roleModelManager = "modelManager"

// governanceRoleFloors are the floors of each role (muster's roleFloors): a
// floor asks agents in both modes, and muster refuses to start on one listed
// as safe on a server of its role.
var governanceRoleFloors = map[string][]string{
	"agentManager":     {"set_agent_approval", "set_organization_connector", "add_organization_member", "set_organization_model", "set_organization_egress_policy", "replace_organization_credential"},
	"platformManager":  {"approve_action", "merge_action"},
	"connectorManager": {"mcpserver_create", "mcpserver_connect", "mcpserver_set_approval"},
	"toolchainManager": {"approve_source"},
	roleModelManager:   {"add_backend"},
}

// governanceSourceRoles are the roles the meta chart's own governedServers
// give its sources; an override that names no role keeps it, so a safe
// entry is checked against the floors of the role it ends up with.
var governanceSourceRoles = map[string]string{
	"agent-manager": "agentManager", "kagent": "sessionManager", "model-manager": roleModelManager,
	"cluster-manager": "clusterManager", "vm-manager": "vmManager", "toolchain-manager": "toolchainManager",
	"workspace-manager": "workspaceManager", "skill-manager": "skillManager", "mcp-kubernetes": "infrastructure",
	"giantswarm-platform-manager": "platformManager",
}

// governs says whether the person chose anything of the governance: the
// governed mode or a governedServers override.
func (g Governance) governs() bool {
	return g.Mode == GovernanceGoverned || len(g.GovernedServers) > 0
}

// governanceValues appends the chosen governance to the configmap patch:
// governance: governed (trusted, the chart's default, is not written) and
// the governedServers overrides by source, a dropped one as null.
func (in *Input) governanceValues(m render.Map) render.Map {
	g := in.Governance
	if g.Mode == GovernanceGoverned {
		m = append(m, render.Entry{Key: "governance", Value: GovernanceGoverned, Comment: "The platform's governance mode: the input governance.mode, read back so a\nreconcile keeps it. Changed only through an Action a second person approves."})
	}
	if len(g.GovernedServers) > 0 {
		var servers render.Map
		for _, source := range slices.Sorted(maps.Keys(g.GovernedServers)) {
			s := g.GovernedServers[source]
			if s == nil {
				servers = append(servers, e(source, nil))
				continue
			}
			var entry render.Map
			if s.Role != "" {
				entry = append(entry, e("role", s.Role))
			}
			if s.Safe != nil {
				entry = append(entry, e("safe", slices.Clone(*s.Safe)))
			}
			servers = append(servers, e(source, entry))
		}
		m = append(m, render.Entry{Key: "governedServers", Value: servers, Comment: "The servers muster governs, over the meta chart's: the input\ngovernance.governedServers, read back so a reconcile keeps it. Changed only\nthrough an Action a second person approves; null drops a source."})
	}
	return m
}

// checkGovernance refuses the governance the installation cannot run as
// chosen: any choice while no meta chart release takes it (GovernanceChart
// unset), off the 4 chart line, under a hold of the meta chart before that
// release, governed under a muster hold before the release that reads the
// configuration, and what muster refuses to start on or the meta chart
// refuses to render — a source key that names no MCPServer, an entry
// without role or safe list, a floor of the entry's role on its safe list,
// an empty or repeated safe entry. The schema refuses a role muster does
// not know (muster's api.GovernanceRoles).
func (in *Input) checkGovernance() error {
	g := in.Governance
	if !g.governs() {
		return nil
	}
	input := inputGovernanceMode
	if g.Mode != GovernanceGoverned {
		input = inputGovernanceServers
	}
	if GovernanceChart == "" {
		return refuse(fmt.Sprintf("%s needs the meta chart release that renders muster's governance configuration, and none is released yet: until it is, the definition writes no governance, so no installation reads as governed while its muster is not", describe(input)))
	}
	if in.Installation.ChartLine != lineFour {
		return in.refuseOffLineFour(input)
	}
	if !holdAdmits(in.Versions.Chart, GovernanceChart) {
		return refuse(fmt.Sprintf("%s needs the meta chart %s or later, which renders muster's governance configuration, and %s holds it at %s; move or lift the hold first", describe(input), GovernanceChart, describe("versions.chart"), in.Versions.Chart))
	}
	if hold := in.Versions.Components[componentMuster]; g.Mode == GovernanceGoverned && hold != "" && (GovernanceMuster == "" || !floorAtLeast(hold, GovernanceMuster)) {
		floor := GovernanceMuster
		if floor == "" {
			floor = "the release that reads the governance configuration (not released yet)"
		}
		return refuse(fmt.Sprintf("%s governed needs muster %s or later, and %s holds it at %s, below which nothing asks agents and the meta chart refuses the render; move or lift the hold first", describe(inputGovernanceMode), floor, describe("versions.components.muster"), hold))
	}
	for _, source := range slices.Sorted(maps.Keys(g.GovernedServers)) {
		if err := checkGovernedSource(source, g.GovernedServers[source]); err != "" {
			return refuse(fmt.Sprintf("%s %s: %s", describe(inputGovernanceServers), source, err))
		}
	}
	return nil
}

// checkGovernedSource is what muster or the meta chart refuses of one
// governedServers source; empty when it is accepted.
func checkGovernedSource(source string, s *GovernedServer) string {
	for _, part := range strings.SplitN(source, "/", 2) {
		if errs := validation.IsDNS1123Subdomain(part); len(errs) > 0 {
			return fmt.Sprintf("is no source or muster server key (<name> or <namespace>/<name>): %s", errs[0])
		}
	}
	if s == nil {
		return ""
	}
	if s.Role == "" && s.Safe == nil {
		return "names neither role nor safe; type the source null to drop it, or leave it out to keep the meta chart's"
	}
	if s.Safe == nil {
		return ""
	}
	role := s.Role
	if role == "" {
		role = governanceSourceRoles[source]
	}
	seen := map[string]bool{}
	for i, tool := range *s.Safe {
		switch {
		case strings.TrimSpace(tool) == "":
			return fmt.Sprintf("safe entry %d is empty", i)
		case seen[tool]:
			return fmt.Sprintf("safe entry %d, %s, is listed twice", i, tool)
		case slices.Contains(governanceRoleFloors[role], tool):
			return fmt.Sprintf("safe entry %d, %s, is a floor of role %s, which asks agents in both modes; muster refuses to start on it", i, tool, role)
		}
		seen[tool] = true
	}
	return ""
}
