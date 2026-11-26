package definitions_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// checkout is a sibling repository's file: env names its checkout, else the
// repository next to this one; the test skips where neither is present (CI).
func checkout(t *testing.T, env, repo, path string) []byte {
	t.Helper()
	dir := os.Getenv(env)
	if dir == "" {
		dir = filepath.Join("..", "..", repo)
	}
	raw, err := os.ReadFile(filepath.Join(dir, path))
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
					if name.Name == "roleFloors" {
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
// it copies: muster's roles and every role's floors, and the meta chart's
// own governedServers (role and safe list per source). A floor muster adds,
// or a source the chart adds or reassigns, fails here before a reviewed
// merge stops muster at start.
func TestGovernanceMatchesMusterAndTheChart(t *testing.T) {
	g := definitions.Governance()
	t.Run("muster", func(t *testing.T) {
		roles, floors := musterGovernance(t, checkout(t, "MUSTER_CHECKOUT", "muster", "internal/api/governance.go"))
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
		if !reflect.DeepEqual(values.GovernedServers, g.Chart) {
			t.Errorf("chart %v, the meta chart's %v", g.Chart, values.GovernedServers)
		}
	})
}

// No floor is on the chart's safe lists, every source's role is muster's.
func TestGovernanceDataIsConsistent(t *testing.T) {
	g := definitions.Governance()
	for source, s := range g.Chart {
		if !slices.Contains(g.Muster.Roles, s.Role) {
			t.Errorf("%s: role %q is none of muster's", source, s.Role)
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
}
