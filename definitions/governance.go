package definitions

import (
	"fmt"
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

// GovernanceData is the governance the agent-platform definition writes
// against (agent-platform/governance.yaml): muster's roles and floors, and the
// meta chart's own governedServers by source.
type GovernanceData struct {
	Muster struct {
		Roles  []string            `yaml:"roles"`
		Floors map[string][]string `yaml:"floors"`
	} `yaml:"muster"`
	Chart map[string]GovernedServer `yaml:"chart"`
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
	for _, role := range g.Muster.Roles {
		if slices.Contains(g.Muster.Floors[role], tool) {
			return role
		}
	}
	return ""
}
