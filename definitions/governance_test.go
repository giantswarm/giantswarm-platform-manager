package definitions_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/render/agentplatform"
)

// checkout is a sibling repository's file: env names its checkout, else the
// repository next to this one; the test skips where neither is present (CI).
func checkout(t *testing.T, env, repo, path string) []byte {
	t.Helper()
	dir := os.Getenv(env)
	if dir == "" {
		dir = filepath.Join("..", "..", repo)
	}
	raw, err := os.ReadFile(filepath.Join(dir, path)) // #nosec G304 G703 -- a test reading a sibling checkout the developer names
	if err != nil {
		t.Skipf("no %s checkout (%s, or set %s): %v", repo, dir, env, err)
	}
	return raw
}

// musterGovernance reads muster's roles and floors from its source: the
// string constants, GovernanceRoles' list and the roleFloors map.
func musterGovernance(t *testing.T, src []byte) ([]string, map[string][]string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "governance.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{}
	value := func(e ast.Expr) string {
		switch v := e.(type) {
		case *ast.BasicLit:
			s, _ := strconv.Unquote(v.Value)
			return s
		case *ast.Ident:
			return consts[v.Name]
		}
		return ""
	}
	var roles []string
	floors := map[string][]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if i < len(n.Values) {
					if lit, ok := n.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						consts[name.Name] = value(lit)
					}
					if name.Name == "roleFloors" || name.Name == "builtinRoleFloors" {
						for _, el := range n.Values[i].(*ast.CompositeLit).Elts {
							kv := el.(*ast.KeyValueExpr)
							for _, tool := range kv.Value.(*ast.CompositeLit).Elts {
								floors[value(kv.Key)] = append(floors[value(kv.Key)], value(tool))
							}
						}
					}
				}
			}
		case *ast.FuncDecl:
			if n.Name.Name == "GovernanceRoles" {
				ast.Inspect(n.Body, func(m ast.Node) bool {
					if lit, ok := m.(*ast.CompositeLit); ok {
						for _, el := range lit.Elts {
							roles = append(roles, value(el))
						}
						return false
					}
					return true
				})
			}
		}
		return true
	})
	return roles, floors
}

// The governance data the definition writes against matches the releases
// it copies: muster's built-in roles and floors, and the meta chart's own
// governedServers. Giant Swarm's platform parts are no longer theirs
// (decision #92): a muster or chart from before still carrying the Platform
// Manager's role or entry is read without it, and must agree with ours. A
// floor muster adds, or a source the chart adds or reassigns, fails here
// before a reviewed merge stops muster at start.
func TestGovernanceMatchesMusterAndTheChart(t *testing.T) {
	g := definitions.Governance()
	t.Run("muster", func(t *testing.T) {
		roles, floors := musterGovernance(t, checkout(t, "MUSTER_CHECKOUT", "muster", "internal/api/governance.go"))
		for role, ours := range g.Platform.RoleFloors {
			if theirs, built := floors[role]; built && !reflect.DeepEqual(theirs, ours) {
				t.Errorf("muster still builds in role %s with floors %v, the platform's are %v", role, theirs, ours)
			}
			delete(floors, role)
			roles = slices.DeleteFunc(roles, func(r string) bool { return r == role })
		}
		if !slices.Equal(roles, g.Muster.Roles) {
			t.Errorf("roles %v, muster's %v", g.Muster.Roles, roles)
		}
		if !reflect.DeepEqual(floors, g.Muster.Floors) {
			t.Errorf("floors %v, muster's %v", g.Muster.Floors, floors)
		}
	})
	t.Run("chart", func(t *testing.T) {
		var values struct {
			GovernedServers map[string]definitions.GovernedServer `yaml:"governedServers"`
		}
		if err := yaml.Unmarshal(checkout(t, "AGENT_PLATFORM_CHECKOUT", "agent-platform", "helm/agent-platform/values.yaml"), &values); err != nil {
			t.Fatal(err)
		}
		for source, s := range values.GovernedServers {
			if s.Safe == nil {
				values.GovernedServers[source] = definitions.GovernedServer{Role: s.Role, Safe: []string{}}
			}
		}
		for source, ours := range g.Platform.GovernedServers {
			if theirs, ok := values.GovernedServers[source]; ok && !reflect.DeepEqual(theirs, ours) {
				t.Errorf("the meta chart still ships %s as %v, the platform's is %v", source, theirs, ours)
			}
			delete(values.GovernedServers, source)
		}
		if !reflect.DeepEqual(values.GovernedServers, g.Chart) {
			t.Errorf("chart %v, the meta chart's %v", g.Chart, values.GovernedServers)
		}
	})
}

// No floor is on a safe list the definition renders, every source's role
// is one muster knows with the platform's role floors, the platform's role
// floors name no built-in role, and the connector admins are groups.
func TestGovernanceDataIsConsistent(t *testing.T) {
	g := definitions.Governance()
	for source, s := range g.Servers() {
		if !slices.Contains(g.Roles(), s.Role) {
			t.Errorf("%s: role %q is none muster knows", source, s.Role)
		}
		for _, tool := range s.Safe {
			if role := g.FloorRole(tool); role != "" {
				t.Errorf("%s: safe %s is a floor of %s", source, tool, role)
			}
		}
	}
	for role := range g.Muster.Floors {
		if !slices.Contains(g.Muster.Roles, role) {
			t.Errorf("floors of unknown role %s", role)
		}
	}
	for role := range g.Platform.RoleFloors {
		if slices.Contains(g.Muster.Roles, role) {
			t.Errorf("the platform's role %s is one of muster's built-ins", role)
		}
	}
	for name, list := range map[string][]definitions.Subject{"connector admins": g.Platform.ConnectorAdmins, "workflow editors": g.Platform.WorkflowEditors} {
		if len(list) == 0 || slices.ContainsFunc(list, func(s definitions.Subject) bool { return s.Kind != "Group" }) {
			t.Errorf("%s: %+v", name, list)
		}
	}
	if _, ok := g.Chart["giantswarm-platform-manager"]; ok {
		t.Error("the Platform Manager is the platform's governed server, not the chart's")
	}
}

// governanceFloor reads the meta chart's governance floor, the muster
// release from which it renders muster's governance configuration (empty
// while unreleased).
var governanceFloor = regexp.MustCompile(`define "agent-platform.muster.governanceFloor" -}}(.*?){{- end -}}`)

// Review F1: once the meta chart renders the governance (its floor set),
// the definition must render Giant Swarm's floors and governed server with
// the same release: otherwise a chart and muster with the governance reach
// an installation without the Platform Manager's floors, and agents approve
// and merge its Actions unasked. The definition's gates must be set to the
// chart's floor before the chart ships it.
func TestGovernanceGatesFollowTheChart(t *testing.T) {
	m := governanceFloor.FindSubmatch(checkout(t, "AGENT_PLATFORM_CHECKOUT", "agent-platform", "helm/agent-platform/templates/_helpers.tpl"))
	if m == nil {
		t.Fatal("the meta chart defines no agent-platform.muster.governanceFloor")
	}
	if floor := strings.TrimSpace(string(m[1])); floor != "" && (agentplatform.GovernanceChart == "" || agentplatform.GovernanceMuster != floor) {
		t.Errorf("the meta chart renders the governance from muster %s, and the definition's gates are GovernanceChart %q, GovernanceMuster %q: set them in the release that precedes the chart's, or Giant Swarm installations run without the Platform Manager's floors", floor, agentplatform.GovernanceChart, agentplatform.GovernanceMuster)
	}
}
