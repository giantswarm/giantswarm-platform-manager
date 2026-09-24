package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The plan's refusals, as plan.Installation answers them.
var planRefusals = []string{"DexAppRefusal", "DexSecretRefusal", "FrozenRefusal", "HubRefusal"}

// The single commit, the wave's pre-check and the comparison's
// commitRefused take the plan's refusals from commitRefusal and nowhere else,
// so the three refuse alike: no function of the package but commitRefusal
// asks a plan for one of them, and each of the three calls it. A plan with a
// value frozen where it cannot rotate, or with sections of the hub's Dev
// Portal on record, is refused by commitRefusal and by the wave's pre-check
// with the same sentence, naming the installation.
func TestCommitRefusalsAreOneList(t *testing.T) {
	calls := callsByFunction(t)
	for fn, called := range calls {
		for _, r := range planRefusals {
			if slices.Contains(called, r) && fn != "commitRefusal" {
				t.Errorf("%s asks the plan for %s itself: take it from commitRefusal", fn, r)
			}
		}
	}
	for _, r := range planRefusals {
		if !slices.Contains(calls["commitRefusal"], r) {
			t.Errorf("commitRefusal does not ask the plan for %s", r)
		}
	}
	for _, fn := range []string{"capabilityCommit", "waveRefusal", "compare"} {
		if !slices.Contains(calls[fn], "commitRefusal") {
			t.Errorf("%s does not take its refusals from commitRefusal", fn)
		}
	}

	frozen := plan.Installation{Name: rowan, Diff: map[plan.Change]int{plan.ChangeUpdate: 1}, Files: []plan.File{{Change: plan.ChangeUpdate}},
		GeneratedSecrets: []plan.GeneratedSecret{{Name: rowan + "-muster-valkey-password", Refusal: rowan + "-muster-valkey-password is frozen in a file the definition does not own whole"}}}
	hub := plan.Installation{Name: rowan, Diff: map[plan.Change]int{plan.ChangeUpdate: 1}, Files: []plan.File{{Change: plan.ChangeUpdate}}, HubSections: []string{"app-config.yaml#backstage.appConfig.scaffolder"}}
	for _, p := range []plan.Installation{frozen, hub} {
		want := commitRefusal(p, &installations.Record{})
		if want == "" {
			t.Fatalf("no refusal for %+v", p)
		}
		err := waveRefusal(ToolReconcileCapability, p, &installations.Record{})
		if err == nil || !strings.Contains(err.Error(), rowan+": "+want) {
			t.Errorf("the wave's pre-check: %v, want %q", err, want)
		}
	}
	if err := waveRefusal(ToolReconcileCapability, plan.Installation{Name: rowan, Diff: map[plan.Change]int{}}, &installations.Record{}); err != nil {
		t.Errorf("a plan without a refusal: %v", err)
	}
}

// callsByFunction names, per function of the package's sources, the
// functions and methods it calls.
func callsByFunction(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					switch f := call.Fun.(type) {
					case *ast.Ident:
						out[fn.Name.Name] = append(out[fn.Name.Name], f.Name)
					case *ast.SelectorExpr:
						out[fn.Name.Name] = append(out[fn.Name.Name], f.Sel.Name)
					}
				}
				return true
			})
		}
	}
	return out
}

// A name of rotate is unknown when no rendered plan of the set lists it:
// a name another installation's plan lists applies there; a plan the
// definition refused lists none and takes no part, and with none rendered
// nothing is unknown.
func TestUnknownRotations(t *testing.T) {
	entry := func(name string, refused string, generated ...string) DryRun {
		p := plan.Installation{Name: name, Refused: refused}
		for _, g := range generated {
			p.GeneratedSecrets = append(p.GeneratedSecrets, plan.GeneratedSecret{Name: g})
		}
		return DryRun{Installation: p}
	}
	const typo = rowan + "-typo"
	set := []DryRun{entry(rowan, "", rowan+"-muster-valkey-password"), entry(birch, "", birch+"-muster-valkey-password"), entry("alder", "refused")}
	rotate := rotateArg(map[string]any{ArgRotate: []any{birch + "-muster-valkey-password", typo, rowan + "-muster-valkey-password", typo}})
	if got := unknownRotations(rotate, set); !slices.Equal(got, []string{typo}) {
		t.Fatalf("unknown %v", got)
	}
	if got := unknownRotations(rotate, []DryRun{entry(rowan, "refused")}); len(got) != 0 {
		t.Fatalf("no plan rendered: unknown %v", got)
	}
	if got := unknownRotations(nil, set); len(got) != 0 {
		t.Fatalf("nothing asked: unknown %v", got)
	}
}

// The review names the values the person asked to rotate; the change names
// them apart from the ones a file to write forced.
func TestReviewAndChangeNameTheRotationsOnRequest(t *testing.T) {
	a := &actions.Action{Spec: actions.Spec{Actor: actions.Actor{Login: "alice"}, Kind: actions.KindReconcile, Capability: installations.AgentPlatform,
		Installations: []string{rowan}, Rotate: []string{rowan + "-muster-valkey-password"}}}
	if got, want := reviewText(a), "*alice* asks to reconcile *agent-platform* on *rowan*; rotates on request: rowan-muster-valkey-password."; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	p := plan.Installation{Diff: map[plan.Change]int{plan.ChangeUpdate: 4}, GeneratedSecrets: []plan.GeneratedSecret{
		{Name: "rowan-muster-credentials-revision", Rotates: true, ForcedBy: plan.ForcedByRequest},
		{Name: "rowan-muster-dex-client-secret", Rotates: true, ForcedBy: "acme/mcs:muster-oauth-credentials.yaml"},
		{Name: "rowan-muster-valkey-password", Rotates: true, ForcedBy: plan.ForcedByRequest},
	}}
	got := changeSummary(p)
	for _, want := range []string{"rotates on request rowan-muster-credentials-revision, rowan-muster-valkey-password (", ", rotates rowan-muster-dex-client-secret ("} {
		if !strings.Contains(got, want) {
			t.Errorf("the change %q lacks %q", got, want)
		}
	}
}
