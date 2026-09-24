package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The capability tools' own arguments.
const (
	ArgInstallation = "installation"
	ArgCapability   = "capability"
	ArgInputs       = "inputs"
	ArgContent      = "content"
	// ArgRotate names the generated values to rotate on request.
	ArgRotate = "rotate"
)

// CapabilityResult is the dry run of enable_capability and
// reconcile_capability: the plan per installation, the pull requests in
// dependency order over the set, the installations skipped and the order
// the wave would roll out in.
type CapabilityResult struct {
	Caller     string `json:"caller"`
	Tool       string `json:"tool"`
	Capability string `json:"capability"`
	Hub        string `json:"hub"`
	DryRun     bool   `json:"dryRun"`
	// Order is the wave's rollout order: Giant Swarm's test installations,
	// the hub, then the customers' installations.
	Order         []string           `json:"order"`
	Installations []DryRun           `json:"installations"`
	PullRequests  []plan.PullRequest `json:"pullRequests"`
	Skipped       []Skipped          `json:"skipped"`
	// Commit says what mode commit does with this plan.
	Commit string `json:"commit"`
}

// planned is what the render leaves behind for the commit: the caller's
// client, the hub, the reports and the effective inputs per installation.
type planned struct {
	c       *gh.Client
	hub     installations.Installation
	byName  map[string]installations.Installation
	reports map[string]installations.Report
	inputs  map[string]map[string]any
}

// commitNext says what mode commit does with a dry run, word for word.
const commitNext = `mode "commit" with installation (one installation) opens the pull requests above as you, in this order, and records the Action on the hub in pending approval; reconcile_capability with installations (a set) is one wave: one action, one approval, the pull requests per installation, merged one installation after the other in the order above, each verified before the next; ` +
	`secrets carries the values of suppliedSecrets by field, and no value leaves the encrypted files`

// Skipped is an installation of the set the dry run did not render, and why.
type Skipped struct {
	Name   string   `json:"name"`
	Reason string   `json:"reason"`
	Errors []string `json:"errors,omitempty"`
}

// The reasons an installation of a set is skipped.
const (
	SkippedNotEnabled     = "not enabled"
	SkippedUnreadable     = "unreadable"
	SkippedNoRepositories = "no repositories on record"
)

// capabilityArgDescription describes the capability argument of the write
// tools and the verify: every registered definition.
const capabilityArgDescription = `The capability (default agent-platform): one of the definitions get_info lists, rendered from its own schema and compared against its own files.`

// inputsArgDescription describes the inputs argument of the write tools and
// the verify: the person's layer over the record and the read-back.
const inputsArgDescription = `Your typed inputs of the definition (get_info lists the schema), laid over the schema's defaults, the facts on record (installation.*) and what the definition reads back from the files on record. An unknown key, and a required choice no layer holds, refuse with its name. Never a secret value.`

// stringItems is the schema of an array-of-strings argument.
func stringItems() map[string]any { return map[string]any{"type": "string"} }

func capabilityOptions() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString(ArgInstallation, mcp.Description("The one installation to render, by name: a capability not on record renders as a fresh enable — which selects the 4 chart line and writes agentPlatform.kagentApiV2 into the record (installations/<name>/config.yaml.patch, one more file of the configs pull request) where the fleet policy grants the organisation a component the 4 line alone carries —, one on record as the changes to it; the answer says why a commit would be refused.")),
		mcp.WithArray(ArgInstallations, mcp.Description("The set to render; empty with no installation is every installation of the registry. A set answers each installation's plan with the comparison rolled up — every dimension with its mark and reason, none of the evidence — and no file content unless content is true; one installation's dry run (installation, alone) carries both. Installations without the capability on record are skipped, listed with the reason: a fresh enable is enable_capability with installation, alone."), mcp.Items(stringItems())),
		mcp.WithArray(ArgOrder, mcp.Description("The rollout order of the set when the default (Giant Swarm's test installations, the hub, the customers) is not the one wanted: every rendered installation of the set exactly once."), mcp.Items(stringItems())),
		mcp.WithString(ArgCapability, mcp.Description(capabilityArgDescription), mcp.Enum(installations.CapabilityNames()...)),
		mcp.WithObject(ArgInputs, mcp.Description(inputsArgDescription)),
		mcp.WithBoolean(ArgContent, mcp.Description("Include the rendered content of every file and the file on record (default: true for one installation, false for a set); false answers paths and changes only. An answer above 1 MiB is refused with its size: ask for less.")),
		mcp.WithObject(ArgSecrets, mcp.Description("mode commit only: the secret values the plan's suppliedSecrets name, by field. They land inside the encrypted files and nowhere else — not in the Action, not in a log, not in an answer.")),
		mcp.WithArray(ArgRotate, mcp.Description(rotateArgDescription), mcp.Items(stringItems())),
	}
}

// rotateArgDescription describes the rotate argument of the write tools.
const rotateArgDescription = `Generated values to rotate on request, by name as the plan lists them (generatedSecrets[].name, e.g. <installation>-muster-valkey-password). ` +
	`The dry run lists each with rotates and forcedBy "request", frozenIn the files on record that hold it, and those files as update; the commit draws a new value, rewrites every file that holds it — the other values of a rewritten file rotate with it — and draws the credentials revision of the component that owns it (forcedBy "request" as well), so every workload that reads it restarts. ` +
	`A name applies to each installation of the set whose plan lists it (the names carry the installation); a name no plan of the set lists is refused, and so is one frozen in a file with other owners, before anything is written.`

func (t *Tools) enableCapabilityTool() WriteTool {
	return WriteTool{Name: ToolEnableCapability,
		Description: "Enable a platform capability on an installation, or a set: answers the plan a commit would write — the files with their change, the pull requests in order, the secrets by name, the Dex clients, the customer's actions, the probes — with the comparison's marks. The inputs are the record, what the definition reads back and your typed inputs. No secret value ever appears in a dry run.",
		Options:     capabilityOptions(),
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			return t.capabilityDryRun(ctx, ToolEnableCapability, args)
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			return t.capabilityCommit(ctx, ToolEnableCapability, args)
		}}
}

func (t *Tools) reconcileCapabilityTool() WriteTool {
	return WriteTool{Name: ToolReconcileCapability,
		Description: "Reconcile a platform capability over a set of installations (empty: every one of the registry): the same plan as enable_capability per installation, every file compared with the repository as it is. A plan whose files are all unchanged is the definition matching the installation. Installations without the capability on record are skipped and listed: a wave reconciles what is on record, a fresh enable is enable_capability alone.",
		Options:     capabilityOptions(),
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			return t.capabilityDryRun(ctx, ToolReconcileCapability, args)
		},
		Commit: func(ctx context.Context, args map[string]any) (any, error) {
			return t.capabilityCommit(ctx, ToolReconcileCapability, args)
		}}
}

// capabilityDryRun is the dry run of both tools: the plan, and nothing else.
func (t *Tools) capabilityDryRun(ctx context.Context, tool string, args map[string]any) (any, error) {
	out, _, err := t.capabilityPlan(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	t.d.Log.Info(tool, identity.LogAttr(ctx), "dryRun", true, "installations", len(out.Installations), "skipped", len(out.Skipped), "pullRequests", len(out.PullRequests))
	return out, nil
}

// capabilityPlan is the render of both tools over one installation or a set,
// every read as the caller: the dry run's answer, and what a commit of it
// needs.
func (t *Tools) capabilityPlan(ctx context.Context, tool string, args map[string]any) (*CapabilityResult, *planned, error) {
	token, ok := identity.TokenFromContext(ctx)
	if !ok {
		return nil, nil, errors.New(tool + " needs a caller: the request carried no GitHub user token to read the registry and the installations' repositories as; " + identity.SignIn)
	}
	def, err := capabilityArg(args)
	if err != nil {
		return nil, nil, err
	}
	one, _ := args[ArgInstallation].(string)
	set := stringSlice(args[ArgInstallations])
	if one == "" && tool == ToolEnableCapability && len(set) == 0 {
		return nil, nil, fmt.Errorf("%s needs %s (one installation) or %s (a set)", tool, ArgInstallation, ArgInstallations)
	}
	inputs, _ := args[ArgInputs].(map[string]any)
	rotate := rotateArg(args)
	// One installation named alone is the plan in full; a set — two or more,
	// the whole registry, one named next to a set — is the wave's shape.
	whole := one != "" && len(set) == 0
	content := contentArg(args, whole)

	c, err := t.person(token)
	if err != nil {
		return nil, nil, err
	}
	reg, err := t.registry(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	wanted := set
	if one != "" {
		wanted = append([]string{one}, set...)
	}
	selected, err := reg.Select(wanted, "")
	if err != nil {
		return nil, nil, err
	}
	hub, _ := reg.Find(reg.Hub)
	byName := map[string]installations.Installation{}
	for _, inst := range reg.Installations {
		byName[inst.Name] = inst
	}
	caps := installations.Capabilities()
	reports := reg.InspectAll(ctx, c, selected, caps, installations.Full)
	out := CapabilityResult{Caller: identity.Caller(ctx), Tool: tool, Capability: def.Name, Hub: reg.Hub, DryRun: true,
		Order: []string{}, Installations: []DryRun{}, PullRequests: []plan.PullRequest{}, Skipped: []Skipped{}, Commit: commitNext}
	env := &planned{c: c, hub: hub, byName: byName, reports: map[string]installations.Report{}, inputs: map[string]map[string]any{}}
	for _, r := range waveOrder(reports, hub) {
		env.reports[r.Name] = r
		if skip, ok := skipped(r, def.Name, r.Name == one); ok {
			out.Skipped = append(out.Skipped, skip)
			continue
		}
		res, err := t.compare(ctx, env, r, def, inputs, content, rotate)
		if err != nil {
			return nil, nil, err
		}
		out.Order = append(out.Order, r.Name)
		out.Installations = append(out.Installations, dryRun(res, whole))
	}
	if err := applyOrder(&out, stringSlice(args[ArgOrder])); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tool, err)
	}
	if unknown := unknownRotations(rotate, out.Installations); len(unknown) > 0 {
		where := "no plan of the set lists"
		if whole {
			where = "the plan of " + one + " does not list"
		}
		return nil, nil, fmt.Errorf("%s: %s names %s, which %s as a generated value: a name is the plan's generatedSecrets[].name and carries its installation (<installation>-muster-valkey-password); nothing is rotated or committed", tool, ArgRotate, strings.Join(unknown, ", "), where)
	}
	out.PullRequests = plan.PullRequests(plans(out.Installations), byName, hub)
	return &out, env, nil
}

// rotateArg is the rotate argument, each name once, sorted.
func rotateArg(args map[string]any) []string {
	names := stringSlice(args[ArgRotate])
	slices.Sort(names)
	return slices.Compact(names)
}

// unknownRotations are the names of rotate (rotateArg) no rendered plan among
// entries lists as a generated value: a name applies to each
// installation whose plan lists it, so one no plan lists is a typo or another
// installation's. A plan the definition refused lists none and takes no part;
// with none rendered nothing is unknown — the refusals say why.
func unknownRotations(rotate []string, entries []DryRun) []string {
	listed := map[string]bool{}
	rendered := false
	for _, e := range entries {
		if e.Refused != "" {
			continue
		}
		rendered = true
		for _, g := range e.GeneratedSecrets {
			listed[g.Name] = true
		}
	}
	var unknown []string
	for _, n := range rotate {
		if rendered && !listed[n] {
			unknown = append(unknown, n)
		}
	}
	return unknown
}

// contentArg says whether the files' content is answered: as asked, else
// for one installation's plan and not for a set's.
func contentArg(args map[string]any, whole bool) bool {
	if v, ok := args[ArgContent].(bool); ok {
		return v
	}
	return whole
}

// capabilityArg is the definition named in args, agent-platform by default,
// from the registry; a name the registry does not know is refused with the
// registry's names.
func capabilityArg(args map[string]any) (installations.Capability, error) {
	capability, _ := args[ArgCapability].(string)
	if capability == "" {
		capability = installations.AgentPlatform
	}
	def, ok := installations.FindCapability(capability)
	if !ok {
		return installations.Capability{}, fmt.Errorf("capability %q is not known: the definitions are %s", capability, strings.Join(installations.CapabilityNames(), ", "))
	}
	return def, nil
}

// readAs reads a repository file as the caller c stands for.
func readAs(c *gh.Client) plan.Reader {
	return func(ctx context.Context, repository, path string) (string, error) {
		owner, repo, err := gh.SplitRepo(repository)
		if err != nil {
			return "", err
		}
		return gh.ReadFile(ctx, c, owner, repo, path)
	}
}

// registry loads the registry as the caller, naming the App requirement on a
// refusal.
func (t *Tools) registry(ctx context.Context, c *gh.Client) (*installations.Registry, error) {
	reg, err := installations.Load(ctx, c, t.d.Registry)
	if err != nil && (errors.Is(err, gh.ErrNotFound) || errors.Is(err, gh.ErrForbidden)) {
		err = fmt.Errorf("%w — the registry is read as you through the App %s: the App must be installed on the repository (contents: read) and you must be able to read it", err, ToolPrefix)
	}
	return reg, err
}

// skipped says whether r is left out of the set: an installation named as
// the one installation never is; otherwise one unreadable, without
// repositories or without the capability on record is, with the reason — a
// wave reconciles what is on record and never enables.
func skipped(r installations.Report, capability string, named bool) (Skipped, bool) {
	switch {
	case !r.Repositories.Known():
		return Skipped{Name: r.Name, Reason: SkippedNoRepositories, Errors: r.Errors}, true
	case !r.Readable || r.Record == nil:
		return Skipped{Name: r.Name, Reason: SkippedUnreadable, Errors: r.Errors}, true
	case !named && !capabilityState(r, capability).OnRecord():
		return Skipped{Name: r.Name, Reason: SkippedNotEnabled}, true
	}
	return Skipped{}, false
}

// mergeInputs are the comparison's inputs, in layers: the schema's
// defaults, the facts on record the schema names under installation and the
// inputs the definition derives from the record beyond them (RecordInputs),
// what the definition reads back from the files on record (read as the caller,
// an installation a read-back names resolved among the registry's), and
// the person's typed inputs over it all. A read-back lays over the record
// only for a person input; a registry input's read-back is answered next
// to the fact and never laid over — the fact on record stands. The second
// answer names everything read back, by dotted input key. What the schema
// requires and no layer holds, the definition names in its refusal.
func mergeInputs(ctx context.Context, def installations.Capability, r installations.Report, read installations.Reader, typed map[string]any, registry map[string]installations.Installation) (map[string]any, map[string]any, error) {
	merged, err := def.Defaults()
	if err != nil {
		return nil, nil, err
	}
	facts, err := def.Facts(r.Facts())
	if err != nil {
		return nil, nil, err
	}
	merged[installations.InputsInstallation] = facts
	if def.RecordInputs != nil {
		record, err := def.RecordInputs(r)
		if err != nil {
			return nil, nil, err
		}
		overlay(merged, record)
	}
	known := make([]installations.Installation, 0, len(registry))
	for _, inst := range registry {
		known = append(known, inst)
	}
	back, err := def.ReadBack(ctx, read, r.Installation, known)
	if err != nil {
		return nil, nil, err
	}
	person, err := def.PersonInputs()
	if err != nil {
		return nil, nil, err
	}
	for k, v := range back {
		if slices.Contains(person, k) {
			setInput(merged, strings.Split(k, "."), v)
		}
	}
	if v, ok := typed[installations.InputsInstallation]; ok {
		if _, isMap := v.(map[string]any); !isMap {
			return nil, nil, fmt.Errorf("%s.installation must be an object of installation facts", ArgInputs)
		}
	}
	overlay(merged, typed)
	return merged, back, nil
}

// overlay lays over on base, key by key: a mapping over a mapping merges,
// anything else replaces.
func overlay(base, over map[string]any) {
	for k, v := range over {
		if ov, ok := v.(map[string]any); ok {
			if bv, ok := base[k].(map[string]any); ok {
				overlay(bv, ov)
				continue
			}
		}
		base[k] = v
	}
}

// setInput puts v at path in doc, creating the mappings on the way.
func setInput(doc map[string]any, path []string, v any) {
	for _, k := range path[:len(path)-1] {
		next, ok := doc[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			doc[k] = next
		}
		doc = next
	}
	doc[path[len(path)-1]] = v
}

// waveOrder sorts the reports into the wave's order (D8): Giant Swarm's own
// test installations, the hub, then the customers' installations, by name
// within each group.
func waveOrder(reports []installations.Report, hub installations.Installation) []installations.Report {
	group := func(r installations.Report) int {
		switch {
		case r.Name == hub.Name:
			return 1
		case r.Customer == hub.Customer:
			return 0
		}
		return 2
	}
	out := slices.Clone(reports)
	sort.SliceStable(out, func(i, j int) bool {
		gi, gj := group(out[i]), group(out[j])
		if gi != gj {
			return gi < gj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func capabilityState(r installations.Report, capability string) installations.State {
	for _, cs := range r.Capabilities {
		if cs.Name == capability {
			return cs.State
		}
	}
	return installations.StateUnknown
}

func stringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
