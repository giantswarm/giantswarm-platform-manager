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
// entry (dryRun). content keeps the rendered files' content; rotate names the
// generated values the plan rotates on request (a dry run's rotate).
func (t *Tools) compare(ctx context.Context, env *planned, r installations.Report, def installations.Capability, typed map[string]any, content bool, rotate []string) (*verify.Result, error) {
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
	in := verify.Inputs{Source: verify.Source(len(back) > 0, len(typed) > 0), Values: values, ReadBack: back, Unset: unset, Typed: typed}
	res := verify.Compare(ctx, verify.Options{Definition: def, Installation: r.Installation, Hub: env.hub, State: capabilityState(r, def.Name), Inputs: in, Read: read, Content: content, Probes: t.d.Probes, Rotate: rotate})
	res.Caller = identity.Caller(ctx)
	p := res.Plan()
	switch {
	case res.Refused != "":
		res.CommitRefused = res.Refused
	case len(res.Inputs.Missing) > 0:
		res.CommitRefused = missingChoices(def.Name, res.Inputs.Missing)
	default:
		res.CommitRefused = commitRefusal(p, r.Record)
	}
	res.PullRequests = plan.PullRequests([]plan.Installation{p}, env.byName, env.hub)
	return &res, nil
}

// commitRefusal is why a commit of p is refused over what its plan found on
// the record, or "": the record's dex-app too old for a referenced Dex client,
// its secret patch carrying a client the plan references, a generated value
// frozen where it cannot rotate — a rotation asked for alike —, a section of
// the hub's Dev Portal the commit would remove. It is the one list the
// single commit, the wave's pre-check and the comparison's commitRefused take
// these refusals from, the first one found answered, so the three refuse
// alike (TestCommitRefusalsAreOneList).
func commitRefusal(p plan.Installation, rec *installations.Record) string {
	for _, refusal := range []string{p.DexAppRefusal(rec), p.DexSecretRefusal(rec), p.FrozenRefusal(), p.HubRefusal()} {
		if refusal != "" {
			return refusal
		}
	}
	return ""
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

// dryRun is the result regrouped as the dry run's entry. One installation's
// entry carries the comparison with its evidence, as verify_capability
// answers it; a set's entry carries it rolled up — every dimension with its
// mark and reason, none of the differences, files compared or probe details
// — so a wave's answer stays in proportion to the set.
func dryRun(res *verify.Result, evidence bool) DryRun {
	features := res.Features
	if !evidence {
		features = rolledUp(features)
	}
	return DryRun{Installation: res.Plan(), Features: features, Summary: res.Summary}
}

// rolledUp is the features with each dimension's mark and reason and none of
// the evidence behind them.
func rolledUp(features []verify.Feature) []verify.Feature {
	out := make([]verify.Feature, len(features))
	for i, f := range features {
		dims := make([]verify.Dimension, len(f.Dimensions))
		for j, d := range f.Dimensions {
			dims[j] = verify.Dimension{ID: d.ID, Kind: d.Kind, Key: d.Key, Mark: d.Mark, Reason: d.Reason}
		}
		f.Dimensions = dims
		out[i] = f
	}
	return out
}

// plans are the entries' plans.
func plans(entries []DryRun) []plan.Installation {
	out := make([]plan.Installation, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Installation)
	}
	return out
}
