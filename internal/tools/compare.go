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
	res := verify.Compare(ctx, verify.Options{Definition: def, Installation: r.Installation, Hub: env.hub, State: capabilityState(r, def.Name), Inputs: in, Read: read, Content: content, Probes: t.d.Probes, Rotate: rotate, Installations: env.byName})
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
// its secret patch carrying a client the plan references, the release
// candidates dropped while one is ahead of the stable release, a generated value
// frozen where it cannot rotate — a rotation asked for alike —, a section of
// the hub's Dev Portal the commit would remove. It is the one list the
// single commit, the wave's pre-check and the comparison's commitRefused take
// these refusals from, the first one found answered, so the three refuse
// alike (TestCommitRefusalsAreOneList). Last, a fact the render needs that
// the record leaves empty (FactsRefusal).
func commitRefusal(p plan.Installation, rec *installations.Record) string {
	for _, refusal := range []string{p.DexAppRefusal(rec), p.DexSecretRefusal(rec), p.ReleaseCandidateRefusal(rec), p.FrozenRefusal(), p.CarryRefusal(), p.HubRefusal()} {
		if refusal != "" {
			return refusal
		}
	}
	return p.FactsRefusal()
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

// dryRun is the result regrouped as the dry run's entry: the plan and the
// comparison with its evidence, as verify_capability answers it. A set's
// answer rolls each entry up (rolledUp); the wave commits the entries whole.
func dryRun(res *verify.Result) DryRun {
	return DryRun{Installation: res.Plan(), Features: res.Features, Summary: res.Summary}
}

// rolledUp is a set's entry: what differs from the definition, the rest
// counted — so a set's answer grows with what the wave would change, not with
// the fleet. Each feature keeps its mark and counts and lists the dimensions
// that differ with their mark and reason, none of the evidence; the files are
// those that change or carry a finding (an error, a literal held encrypted,
// a value dropped or replaced), without the objects and values they relate,
// which the pull requests carry for the set; the generated values are those a
// commit writes, rotates or refuses, not the ones kept on record. The diff and
// the summary count everything; one installation's dry run lists it.
func rolledUp(e DryRun) DryRun {
	features := make([]verify.Feature, len(e.Features))
	for i, f := range e.Features {
		dims := []verify.Dimension{}
		for _, d := range f.Dimensions {
			if d.Mark != verify.AsDefined && d.Mark != verify.NotChecked {
				dims = append(dims, verify.Dimension{ID: d.ID, Kind: d.Kind, Key: d.Key, Mark: d.Mark, Reason: d.Reason})
			}
		}
		f.Dimensions = dims
		features[i] = f
	}
	e.Features = features
	files := []plan.File{}
	for _, f := range e.Files {
		if f.Change != plan.ChangeUnchanged || f.Error != "" || len(f.Unseen) > 0 || len(f.Dropped) > 0 || len(f.Replaced) > 0 {
			f.Generated, f.Kept, f.Creates, f.References = nil, nil, nil, nil
			files = append(files, f)
		}
	}
	e.Files = files
	generated := []plan.GeneratedSecret{}
	for _, g := range e.GeneratedSecrets {
		if !g.Kept || g.Rotates || g.Refusal != "" {
			generated = append(generated, g)
		}
	}
	e.GeneratedSecrets = generated
	return e
}

// plans are the entries' plans.
func plans(entries []DryRun) []plan.Installation {
	out := make([]plan.Installation, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Installation)
	}
	return out
}
