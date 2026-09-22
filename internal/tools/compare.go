package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"

	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// DryRun is the dry run's entry for one installation: the plan, with the
// comparison's marks the same computation produced.
type DryRun struct {
	plan.Installation
	Features []verify.Feature    `json:"features"`
	Summary  map[verify.Mark]int `json:"summary"`
}

// compare is the one computation verify_capability and the dry runs answer:
// the inputs from the record, the read-back and the person's typed inputs;
// the plan built once and read against the repositories as the caller; each
// difference named an input or drift; the anonymous probes run.
// verify_capability answers it as it is, a dry run regrouped as the plan's
// entry (dryRun). content keeps the rendered files' content.
func (t *Tools) compare(ctx context.Context, env *planned, r installations.Report, def installations.Capability, typed map[string]any, content bool) (*verify.Result, error) {
	read := readAs(env.c)
	values, back, err := mergeInputs(ctx, def, r, installations.Reader(read), typed, env.byName)
	if err != nil {
		return nil, err
	}
	unset, err := def.Unset(values)
	if err != nil {
		return nil, err
	}
	env.inputs[r.Name] = values
	in := verify.Inputs{Source: verify.Source(len(back) > 0, len(typed) > 0), Values: values, ReadBack: back, Unset: unset}
	res := verify.Compare(ctx, verify.Options{Definition: def, Installation: r.Installation, Hub: env.hub, State: capabilityState(r, def.Name), Inputs: in, Read: read, Content: content, Probes: t.d.Probes})
	res.Caller = identity.Caller(ctx)
	p := res.Plan()
	dexApp, frozen := p.DexAppRefusal(r.Record), p.FrozenRefusal()
	switch {
	case res.Refused != "":
		res.CommitRefused = fmt.Sprintf("the definition refuses these inputs for %s (refused says why); nothing is committed", r.Name)
	case len(res.Inputs.Missing) > 0:
		res.CommitRefused = missingChoices(def.Name, res.Inputs.Missing)
	case dexApp != "":
		res.CommitRefused = dexApp
	case frozen != "":
		res.CommitRefused = frozen
	}
	res.PullRequests = plan.PullRequests([]plan.Installation{p}, env.byName, env.hub)
	return &res, nil
}

// missingInputs names the choices not on record for a refusal.
func missingInputs(fields []string) string {
	return fmt.Sprintf("%d choice(s) not on record: %s", len(fields), strings.Join(fields, ", "))
}

// missingChoices is the comparison's word on the choices not on record, one
// sentence a person reads under a button: every field, with what the
// definition's schema says it is when that is short.
func missingChoices(capability string, fields []string) string {
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f
		if what := definitions.InputSummary(capability, f); what != "" {
			parts[i] += " (" + what + ")"
		}
	}
	return "Choose " + strings.Join(parts, "; ") + " before a commit."
}

// dryRun is the result regrouped as the dry run's entry.
func dryRun(res *verify.Result) DryRun {
	return DryRun{Installation: res.Plan(), Features: res.Features, Summary: res.Summary}
}

// plans are the entries' plans.
func plans(entries []DryRun) []plan.Installation {
	out := make([]plan.Installation, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Installation)
	}
	return out
}
