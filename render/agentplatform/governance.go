package agentplatform

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
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

// maxSafeEntries bounds one source's safe list, as the schema does: the
// review shows every entry, and Slack shows a bounded message.
const maxSafeEntries = 64

// toolName is what a safe entry may be: an MCP tool name. Anything else
// (spaces, newlines, Slack markup) would also reach the review as text.
var toolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// chartSourceAlias matches a server key that names one of the meta chart's
// sources under another key: <namespace>/<source>, or a kubernetes family
// member <cluster>-mcp-kubernetes.
func chartSourceAlias(key string) string {
	name := key[strings.LastIndex(key, "/")+1:]
	chart := definitions.Governance().Chart
	if _, ok := chart[name]; ok && name != key {
		return name
	}
	if strings.HasSuffix(name, "-mcp-kubernetes") {
		return "mcp-kubernetes"
	}
	return ""
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
// refuses to render, and what would drop floors (checkGovernedSource). The
// schema refuses a role muster does not know (muster's api.GovernanceRoles).
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
// governedServers source, and what would switch governance off past the
// floors; empty when it is accepted. A server's role is what the server is,
// fixed by the meta chart for its own sources: an override may not change it,
// since muster applies a role's floors by the server's role and a new role
// would drop them. An entry under a server key of its own names its role,
// and may not name one of the chart's sources under another key. No floor
// of any role is ever safe, and a safe entry is a tool name.
func checkGovernedSource(source string, s *GovernedServer) string {
	for _, part := range strings.SplitN(source, "/", 2) {
		if errs := validation.IsDNS1123Subdomain(part); len(errs) > 0 {
			return fmt.Sprintf("is no source or muster server key (<name> or <namespace>/<name>): %s", errs[0])
		}
	}
	if alias := chartSourceAlias(source); alias != "" {
		return fmt.Sprintf("names the meta chart's source %s under a server key; override it as %s, whose role the chart fixes", alias, alias)
	}
	if s == nil {
		return ""
	}
	g := definitions.Governance()
	chart, known := g.Chart[source]
	switch {
	case known && s.Role != "" && s.Role != chart.Role:
		return fmt.Sprintf("role %s: the meta chart governs %s as %s, and an override may not change a server's role, whose floors muster applies by it", s.Role, source, chart.Role)
	case !known && s.Role == "":
		return "names no role: a server key the meta chart does not govern names the role of the server it is (its floors follow from it)"
	case s.Role == "" && s.Safe == nil:
		return "names neither role nor safe; type the source null to drop it, or leave it out to keep the meta chart's"
	}
	if s.Safe == nil {
		return ""
	}
	if len(*s.Safe) > maxSafeEntries {
		return fmt.Sprintf("safe lists %d entries, more than %d", len(*s.Safe), maxSafeEntries)
	}
	seen := map[string]bool{}
	for i, tool := range *s.Safe {
		switch {
		case !toolName.MatchString(tool):
			return fmt.Sprintf("safe entry %d, %q, is no tool name (letters, digits, _ . -, at most 128)", i, tool)
		case seen[tool]:
			return fmt.Sprintf("safe entry %d, %s, is listed twice", i, tool)
		case g.FloorRole(tool) != "":
			return fmt.Sprintf("safe entry %d, %s, is a floor of role %s, which asks agents in both modes; no floor is ever safe", i, tool, g.FloorRole(tool))
		}
		seen[tool] = true
	}
	return ""
}

// MusterHoldBelowGovernance says whether a muster version hold
// (components.muster.versionRange) lets an installation run a muster before
// GovernanceMuster, the release that reads the governance configuration and
// enforces its floors: such a hold switches governance off there. False while
// the gate is unset (no muster enforces it yet) and for no hold.
func MusterHoldBelowGovernance(hold string) bool {
	return GovernanceMuster != "" && hold != "" && !floorAtLeast(hold, GovernanceMuster)
}
