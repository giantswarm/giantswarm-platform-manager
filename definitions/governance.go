package definitions

import (
	"fmt"
	"maps"
	"slices"
	"sync"

	"gopkg.in/yaml.v3"
)

// GovernedServer is one source of the meta chart's governedServers: the role
// whose floors hold on its servers and its safe list.
type GovernedServer struct {
	Role string   `yaml:"role" json:"role"`
	Safe []string `yaml:"safe" json:"safe"`
}

// Subject is an RBAC subject in the meta chart's shape.
type Subject struct {
	APIGroup string `yaml:"apiGroup" json:"apiGroup,omitempty"`
	Kind     string `yaml:"kind" json:"kind"`
	Name     string `yaml:"name" json:"name"`
}

// GovernanceData is the governance the agent-platform definition writes
// against (agent-platform/governance.yaml): muster's built-in roles and
// floors, Giant Swarm's own platform parts the definition always renders
// (the Platform Manager's role floors, its governed server, the connector
// admins), and the meta chart's own governedServers by source.
type GovernanceData struct {
	Muster struct {
		Roles  []string            `yaml:"roles"`
		Floors map[string][]string `yaml:"floors"`
	} `yaml:"muster"`
	Platform struct {
		RoleFloors      map[string][]string       `yaml:"roleFloors"`
		GovernedServers map[string]GovernedServer `yaml:"governedServers"`
		ConnectorAdmins []Subject                 `yaml:"connectorAdmins"`
	} `yaml:"platform"`
	Chart map[string]GovernedServer `yaml:"chart"`
}

// Roles are the roles muster knows with the platform's role floors: its
// built-ins and the roles the rendered governance.roleFloors add.
func (g GovernanceData) Roles() []string {
	out := slices.Clone(g.Muster.Roles)
	for _, role := range slices.Sorted(maps.Keys(g.Platform.RoleFloors)) {
		if !slices.Contains(out, role) {
			out = append(out, role)
		}
	}
	return out
}

// Floors are the floors of role: muster's built-ins and the platform's
// role floors, a union as muster takes them.
func (g GovernanceData) Floors(role string) []string {
	out := slices.Clone(g.Muster.Floors[role])
	for _, t := range g.Platform.RoleFloors[role] {
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// Servers are the governed servers by source as the definition renders
// them before any input: the meta chart's own and the platform's.
func (g GovernanceData) Servers() map[string]GovernedServer {
	out := maps.Clone(g.Chart)
	if out == nil {
		out = map[string]GovernedServer{}
	}
	maps.Copy(out, g.Platform.GovernedServers)
	return out
}

var governance = sync.OnceValues(func() (GovernanceData, error) {
	raw, err := FS.ReadFile("agent-platform/governance.yaml")
	if err != nil {
		return GovernanceData{}, err
	}
	var g GovernanceData
	if err := yaml.Unmarshal(raw, &g); err != nil {
		return GovernanceData{}, fmt.Errorf("agent-platform/governance.yaml: %w", err)
	}
	return g, nil
})

// Governance is agent-platform/governance.yaml, read once. The file is
// embedded and tested, so a read failure panics.
func Governance() GovernanceData {
	g, err := governance()
	if err != nil {
		panic(err)
	}
	return g
}

// FloorRole is the role whose floor tool is, empty when it is no floor.
func (g GovernanceData) FloorRole(tool string) string {
	for _, role := range g.Roles() {
		if slices.Contains(g.Floors(role), tool) {
			return role
		}
	}
	return ""
}
