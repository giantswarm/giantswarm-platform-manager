package tools

import (
	"context"
	"fmt"

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
	values, back, err := mergeInputs(ctx, def, r, installations.Reader(read), typed)
	if err != nil {
		return nil, err
	}
	env.inputs[r.Name] = values
	in := verify.Inputs{Source: verify.Source(len(back) > 0, len(typed) > 0), Values: values, ReadBack: back}
	res := verify.Compare(ctx, verify.Options{Definition: def, Installation: r.Installation, Hub: env.hub, State: capabilityState(r, def.Name), Inputs: in, Read: read, Content: content, Probes: t.d.Probes})
	res.Caller = identity.Caller(ctx)
	res.OptIn = r.OptIn
	p := res.Plan()
	switch refusal := p.FrozenRefusal(); {
	case res.Refused != "":
		res.CommitRefused = fmt.Sprintf("the definition refuses these inputs for %s (refused says why); nothing is committed", r.Name)
	case r.OptIn != nil && r.OptIn.State != installations.OptedIn:
		res.CommitRefused = fmt.Sprintf("%s is %s: %s", r.Name, r.OptIn.State, r.OptIn.HowToOptIn)
	case refusal != "":
		res.CommitRefused = refusal
	}
	res.PullRequests = plan.PullRequests([]plan.Installation{p}, env.byName, env.hub)
	return &res, nil
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
