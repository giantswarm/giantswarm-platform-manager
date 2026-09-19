package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/google/go-github/v92/github"
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
	Order         []string            `json:"order"`
	Installations []plan.Installation `json:"installations"`
	PullRequests  []plan.PullRequest  `json:"pullRequests"`
	Skipped       []Skipped           `json:"skipped"`
	// Commit says what mode commit does with this plan.
	Commit string `json:"commit"`
}

// planned is what the render leaves behind for the commit: the caller's
// client, the hub, the reports and the effective inputs per installation.
type planned struct {
	c       *github.Client
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
	Name   string               `json:"name"`
	Reason string               `json:"reason"`
	OptIn  *installations.OptIn `json:"optIn,omitempty"`
	Errors []string             `json:"errors,omitempty"`
}

// The reasons an installation of a set is skipped.
const (
	SkippedNotOptedIn     = "not opted in"
	SkippedUnreadable     = "unreadable"
	SkippedNoRepositories = "no repositories on record"
)

// capabilityArgDescription describes the capability argument of the write
// tools and the verify: every registered definition.
const capabilityArgDescription = `The capability (default agent-platform): one of the definitions get_info lists, rendered from its own schema and compared against its own files.`

// stringItems is the schema of an array-of-strings argument.
func stringItems() map[string]any { return map[string]any{"type": "string"} }

func capabilityOptions() []mcp.ToolOption {
	return []mcp.ToolOption{
		mcp.WithString(ArgInstallation, mcp.Description("The one installation to render, by name. Rendered whether or not it is opted in; the answer names the opt-in state and why a commit would be refused.")),
		mcp.WithArray(ArgInstallations, mcp.Description("The set to render; empty with no installation is every installation of the registry. Installations not opted in are skipped, listed with the reason."), mcp.Items(stringItems())),
		mcp.WithArray(ArgOrder, mcp.Description("The rollout order of the set when the default (Giant Swarm's test installations, the hub, the customers) is not the one wanted: every rendered installation of the set exactly once."), mcp.Items(stringItems())),
		mcp.WithString(ArgCapability, mcp.Description(capabilityArgDescription), mcp.Enum(installations.CapabilityNames()...)),
		mcp.WithObject(ArgInputs, mcp.Description("The typed inputs of the definition (get_info lists the schema), merged over the facts on record: a typed installation.* key overrides the record; an unknown key, and a required section or choice left out, refuse with its name — the schema is the contract, nothing is chosen for you. Never a secret value.")),
		mcp.WithBoolean(ArgContent, mcp.Description("Include the rendered content of every file (default true); false answers paths and changes only.")),
		mcp.WithObject(ArgSecrets, mcp.Description("mode commit only: the secret values the plan's suppliedSecrets name, by field. They land inside the encrypted files and nowhere else — not in the Action, not in a log, not in an answer.")),
	}
}

func (t *Tools) enableCapabilityTool() WriteTool {
	return WriteTool{Name: ToolEnableCapability,
		Description: "Enable a platform capability on an installation: render its fileset from the facts on record and your typed inputs through the definition and answer the plan — the files per repository with the change each one is against the repository now, the pull requests in dependency order (configs before management-clusters, the hub's with them), the generated secrets by name, the Dex clients with their redirect URIs, the secret values you supply at commit (by field), the actions the customer has to take and the probes the verify runs. installation names the one installation; installations a set, whose members without the opt-in are skipped. No secret value ever appears in a dry run.",
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
		Description: "Reconcile a platform capability over a set of installations (empty: every installation of the registry): the same render as enable_capability per installation, with every file compared against the repository as it is — a plan whose files are all unchanged is an empty diff, the definition matching the installation. Installations not opted in are skipped and listed as such; the order answers the wave's rollout order.",
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
	content := true
	if v, ok := args[ArgContent].(bool); ok {
		content = v
	}

	c, err := gh.AsPerson(t.d.GitHubAPIURL, token)
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
	reports := reg.InspectAll(ctx, c, selected, caps)
	out := CapabilityResult{Caller: identity.Caller(ctx), Tool: tool, Capability: def.Name, Hub: reg.Hub, DryRun: true,
		Order: []string{}, Installations: []plan.Installation{}, PullRequests: []plan.PullRequest{}, Skipped: []Skipped{}, Commit: commitNext}
	env := &planned{c: c, hub: hub, byName: byName, reports: map[string]installations.Report{}, inputs: map[string]map[string]any{}}
	read := readAs(c)
	for _, r := range waveOrder(reports, hub) {
		env.reports[r.Name] = r
		if skip, ok := skipped(r, r.Name == one); ok {
			out.Skipped = append(out.Skipped, skip)
			continue
		}
		merged, err := mergeInputs(def, r, inputs)
		if err != nil {
			return nil, nil, err
		}
		env.inputs[r.Name] = merged
		p := plan.Build(ctx, plan.Options{Definition: def, Installation: r.Installation, Hub: hub, Inputs: merged, Content: content, Read: read})
		p.State = capabilityState(r, def.Name)
		p.OptIn = r.OptIn
		switch refusal := p.FrozenRefusal(); {
		case p.Refused != "":
			p.CommitRefused = fmt.Sprintf("the definition refuses these inputs for %s (refused says why); nothing is committed", r.Name)
		case r.OptIn != nil && r.OptIn.State != installations.OptedIn:
			p.CommitRefused = fmt.Sprintf("%s is %s: %s", r.Name, r.OptIn.State, r.OptIn.HowToOptIn)
		case refusal != "":
			p.CommitRefused = refusal
		}
		out.Order = append(out.Order, r.Name)
		out.Installations = append(out.Installations, p)
	}
	if err := applyOrder(&out, stringSlice(args[ArgOrder])); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tool, err)
	}
	out.PullRequests = plan.PullRequests(out.Installations, byName, hub)
	return &out, env, nil
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
func readAs(c *github.Client) plan.Reader {
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
func (t *Tools) registry(ctx context.Context, c *github.Client) (*installations.Registry, error) {
	reg, err := installations.Load(ctx, c, t.d.Registry)
	if err != nil && (errors.Is(err, gh.ErrNotFound) || errors.Is(err, gh.ErrForbidden)) {
		err = fmt.Errorf("%w — the registry is read as you through the App %s: the App must be installed on the repository (contents: read) and you must be able to read it", err, ToolPrefix)
	}
	return reg, err
}

// skipped says whether r is left out of the set: an installation named as
// the one installation never is; otherwise one not opted in, unreadable or
// without repositories is, with the reason.
func skipped(r installations.Report, named bool) (Skipped, bool) {
	switch {
	case !r.Repositories.Known():
		return Skipped{Name: r.Name, Reason: SkippedNoRepositories, Errors: r.Errors}, true
	case !r.Readable || r.Record == nil:
		return Skipped{Name: r.Name, Reason: SkippedUnreadable, OptIn: r.OptIn, Errors: r.Errors}, true
	case !named && r.OptIn.State != installations.OptedIn:
		return Skipped{Name: r.Name, Reason: SkippedNotOptedIn, OptIn: r.OptIn}, true
	}
	return Skipped{}, false
}

// mergeInputs lays the typed inputs over the facts on record: the facts
// def's schema names under installation first, the person's keys over them.
// What the schema requires and the person left out, the definition names in
// its refusal.
func mergeInputs(def installations.Capability, r installations.Report, typed map[string]any) (map[string]any, error) {
	facts, err := def.Facts(r.Facts())
	if err != nil {
		return nil, err
	}
	merged := map[string]any{"installation": facts}
	for k, v := range typed {
		if k == "installation" {
			over, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s.installation must be an object of installation facts", ArgInputs)
			}
			base := merged["installation"].(map[string]any)
			for kk, vv := range over {
				base[kk] = vv
			}
			continue
		}
		merged[k] = v
	}
	return merged, nil
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
