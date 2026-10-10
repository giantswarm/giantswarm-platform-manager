package tools

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/giantswarm/gitops-commit/provenance"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The disable: disable_capability takes a capability off one installation,
// the mirror of enable_capability. Its dry run names every file it deletes
// or edits (plan.Disable), the files of the definition that stay because
// another capability on record renders them, the objects the fleet's
// non-pruning Kustomization leaves on the cluster as a checklist, and what
// still depends on the capability — a commit is refused while one stands.
// Mode commit opens the pull requests as the caller through the action,
// approval and merge path enable_capability takes; the action is disabled
// once every pull request is merged and the definition's marker is gone from
// the default branch, which the resync reads.

// DisableResult is the answer of disable_capability: the plan, what depends
// on the capability, and for mode commit the action with its pull requests.
type DisableResult struct {
	Caller       string              `json:"caller"`
	Tool         string              `json:"tool"`
	Capability   string              `json:"capability"`
	Hub          string              `json:"hub"`
	Installation string              `json:"installation"`
	DryRun       bool                `json:"dryRun"`
	State        installations.State `json:"state"`
	Plan         plan.Disablement    `json:"plan"`
	// Prunes says the fleet's Kustomization over the definition's tree
	// prunes: Flux deletes what the files applied, and the checklist is empty.
	Prunes bool `json:"prunes"`
	// Remaining are the capabilities on record on the installation that stay:
	// their reconcile dry run and their verify are the check after the merge.
	Remaining []string `json:"remaining"`
	// References name what still depends on the capability, each a sentence
	// naming the reference: a commit is refused while one stands.
	References []string `json:"references"`
	// Unread are the remote bases whose objects could not be read for the
	// checklist; the checklist misses theirs.
	Unread []string `json:"unread,omitempty"`
	// CommitRefused says why mode commit would be refused; empty when it
	// could go ahead.
	CommitRefused string                `json:"commitRefused,omitempty"`
	Action        *actions.Action       `json:"action,omitempty"`
	PullRequests  []actions.PullRequest `json:"pullRequests,omitempty"`
	// Commit says what mode commit does with this plan; Next what follows a
	// commit.
	Commit string `json:"commit,omitempty"`
	Next   string `json:"next,omitempty"`
}

const disableCommitNext = `mode "commit" opens the pull requests above as you, one per repository in this order, and records the Action on the hub in pending approval (ready to merge when the installation is a test installation: no Team review); merge_action merges them, and the action is disabled once the definition's marker is gone from the default branch. ` +
	`The objects of the checklist stay on the cluster until a person deletes them, in its order: the fleet's Kustomization over the tree does not prune`

// basesDepth bounds how deep the checklist follows kustomizations into a
// remote base.
const basesDepth = 3

func (t *Tools) disableCapabilityTool() WriteTool {
	return WriteTool{Name: ToolDisableCapability,
		Description: "Disable a platform capability on one installation, the mirror of enable_capability: answers every file a commit deletes or edits, the files that stay because another capability on record renders them, the objects the fleet's Kustomization does not prune as a checklist in deletion order, and what still depends on the capability (a federation, a portal, a value paired with another installation) — a commit is refused while one stands. Mode commit opens the pull requests as you through the action and its approval, like enable_capability.",
		Options: []mcp.ToolOption{
			mcp.WithString(ArgInstallation, mcp.Required(), mcp.Description("The one installation to take the capability off, by name.")),
			mcp.WithString(ArgCapability, mcp.Description(capabilityArgDescription), mcp.Enum(installations.CapabilityNames()...)),
			mcp.WithBoolean(ArgContent, mcp.Description("Include each edited file as the commit writes it and as it is on record (default: true).")),
			mcp.WithString(ArgReason, mcp.Description(reasonArgDescription)),
		},
		DryRun: func(ctx context.Context, args map[string]any) (any, error) {
			out, _, err := t.disablePlan(ctx, args)
			if err != nil {
				return nil, err
			}
			if v, ok := args[ArgContent].(bool); ok && !v {
				for i := range out.Plan.Files {
					out.Plan.Files[i].Content, out.Plan.Files[i].Current = "", ""
				}
			}
			t.d.Log.Info(ToolDisableCapability, identity.LogAttr(ctx), "dryRun", true, "installation", out.Installation, "files", len(out.Plan.Files), "references", len(out.References))
			return out, nil
		},
		Commit: t.disableCommit,
	}
}

// disablePlan is the dry run of one installation's disable, every read as
// the caller.
func (t *Tools) disablePlan(ctx context.Context, args map[string]any) (*DisableResult, *planned, error) {
	tool := ToolDisableCapability
	token, ok := identity.TokenFromContext(ctx)
	if !ok {
		return nil, nil, errors.New(tool + " needs a caller: the request carried no GitHub user token to read the registry and the installation's repositories as; " + identity.SignIn)
	}
	def, err := capabilityArg(args)
	if err != nil {
		return nil, nil, err
	}
	one, _ := args[ArgInstallation].(string)
	if one = strings.TrimSpace(one); one == "" {
		return nil, nil, fmt.Errorf("%s needs %s: the one installation to take the capability off", tool, ArgInstallation)
	}
	c, err := t.person(token)
	if err != nil {
		return nil, nil, err
	}
	reg, err := t.registry(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	selected, err := reg.Select([]string{one}, "")
	if err != nil {
		return nil, nil, err
	}
	hub, _ := reg.Find(reg.Hub)
	byName := map[string]installations.Installation{}
	for _, inst := range reg.Installations {
		byName[inst.Name] = inst
	}
	caps := installations.Capabilities()
	r := reg.InspectAll(ctx, c, selected, caps, installations.Full)[0]
	env := &planned{c: c, hub: hub, byName: byName, reports: map[string]installations.Report{one: r}, inputs: map[string]map[string]any{}}
	if skip, ok := skipped(r, def.Name, true); ok {
		return nil, nil, fmt.Errorf("%s: %s is %s (%s); the record is read before any removal and nothing is removed blind", tool, one, skip.Reason, strings.Join(skip.Errors, "; "))
	}
	out := &DisableResult{Caller: identity.Caller(ctx), Tool: tool, Capability: def.Name, Hub: reg.Hub, Installation: one, DryRun: true,
		State: capabilityState(r, def.Name), Prunes: def.Prunes, Remaining: []string{}, References: []string{}, Commit: disableCommitNext,
		Plan: plan.Disablement{Name: one, Files: []plan.Removal{}, Stays: []plan.Stay{}, Directories: []string{}, Others: []string{}, Checklist: []plan.Object{}, PullRequests: []plan.PullRequest{}}}
	if !out.State.OnRecord() {
		out.CommitRefused = fmt.Sprintf("%s is %s on %s: nothing to disable", def.Name, out.State, one)
		return out, env, nil
	}

	read := readAs(c)
	values, res, err := renderOnRecord(ctx, def, r, read, nil, byName)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: the %s definition does not render the inputs on record of %s (%w): the files to remove cannot be named, nothing is removed", tool, def.Name, one, err)
	}
	env.inputs[one] = values
	var remaining []plan.Remaining
	for _, other := range caps {
		if other.Name == def.Name || !capabilityState(r, other.Name).OnRecord() {
			continue
		}
		_, rres, err := renderOnRecord(ctx, other, r, read, afterDisable(def.Name), byName)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %s stays on %s and its definition does not render the inputs on record without %s (%w): what of the files stays cannot be named, nothing is removed", tool, other.Name, one, def.Name, err)
		}
		remaining = append(remaining, plan.Remaining{Capability: other.Name, Result: rres})
		out.Remaining = append(out.Remaining, other.Name)
	}
	out.Plan = plan.Disable(ctx, plan.DisableOptions{Definition: def, Installation: r.Installation, Hub: hub, Result: res, Remaining: remaining, Read: read, List: listAs(c)})
	if !def.Prunes {
		objects, unread := baseObjects(ctx, readAtAs(c), out.Plan.Bases)
		out.Unread = unread
		out.Plan.Checklist = plan.Checklist(append(objects, out.Plan.Checklist...))
	} else {
		out.Plan.Checklist = []plan.Object{}
	}
	portals, failed := reg.Portals(ctx, c, []installations.Installation{r.Installation})
	for _, err := range failed {
		if err != nil {
			return nil, nil, fmt.Errorf("%s: the portals on record could not be read as you (%w): what depends on %s on %s cannot be told", tool, err, def.Name, one)
		}
	}
	kept, err := keptPairings(ctx, read, remaining, r.Installation, hub)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tool, err)
	}
	out.References = disableReferences(reg.Hub, r, def.Name, portals, out.Plan, kept)
	out.CommitRefused = disableRefusal(def, r, out)
	return out, env, nil
}

// afterDisable are the typed inputs a capability that stays renders with
// once disabled is gone: the record still says the agent platform runs, so
// the cluster-mcp-servers definition would leave the servers to it.
func afterDisable(disabled string) map[string]any {
	if disabled != installations.AgentPlatform {
		return nil
	}
	return map[string]any{installations.InputsInstallation: map[string]any{"agentPlatform": false}}
}

// renderOnRecord renders def for r from the inputs on record (mergeInputs)
// with typed laid over, as a comparison renders: the effective inputs and
// the render.
func renderOnRecord(ctx context.Context, def installations.Capability, r installations.Report, read plan.Reader, typed map[string]any, byName map[string]installations.Installation) (map[string]any, *render.Result, error) {
	values, _, err := mergeInputs(ctx, def, r, installations.Reader(read), typed, byName)
	if err != nil {
		return nil, nil, err
	}
	in, err := def.Parse(values)
	if err != nil {
		return nil, nil, err
	}
	res, err := def.Render(values, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		return nil, nil, err
	}
	return values, res, nil
}

// listAs lists a directory of a repository as the caller.
func listAs(c *gh.Client) func(ctx context.Context, repository, dir string) ([]string, error) {
	return func(ctx context.Context, repository, dir string) ([]string, error) {
		owner, repo, err := gh.SplitRepo(repository)
		if err != nil {
			return nil, err
		}
		return gh.ListFiles(ctx, c, owner, repo, dir)
	}
}

// baseObjects are the objects the remote bases declare, read as the caller
// at their ref: every manifest a base's kustomization lists, a directory
// followed into its own kustomization, basesDepth deep. A base that could not
// be read is answered in unread.
func baseObjects(ctx context.Context, readAt func(ref string) plan.Reader, bases []string) (objects []plan.Object, unread []string) {
	var walk func(repository, dir, ref string, depth int) error
	walk = func(repository, dir, ref string, depth int) error {
		if depth > basesDepth {
			return fmt.Errorf("%s:%s: deeper than %d kustomizations", repository, dir, basesDepth)
		}
		at := readAt(ref)
		k, err := at(ctx, repository, path.Join(dir, "kustomization.yaml"))
		if err != nil {
			return err
		}
		for _, res := range plan.KustomizationResources([]byte(k)) {
			if repo, sub, subRef, ok := plan.RemoteBase(res); ok {
				if err := walk(repo, sub, subRef, depth+1); err != nil {
					return err
				}
				continue
			}
			p := path.Join(dir, res)
			if !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml") {
				if err := walk(repository, p, ref, depth+1); err != nil {
					return err
				}
				continue
			}
			content, err := at(ctx, repository, p)
			if err != nil {
				return err
			}
			objects = append(objects, plan.ObjectsIn([]byte(content))...)
		}
		return nil
	}
	for _, b := range bases {
		repo, dir, ref, ok := plan.RemoteBase(b)
		if !ok {
			unread = append(unread, b+": not a GitHub base")
			continue
		}
		if err := walk(repo, dir, ref, 1); err != nil {
			unread = append(unread, fmt.Sprintf("%s: %v", b, err))
		}
	}
	return objects, unread
}

// readAtAs reads a repository file at ref as the caller c stands for; the
// default branch when ref is empty.
func readAtAs(c *gh.Client) func(ref string) plan.Reader {
	return func(ref string) plan.Reader {
		return func(ctx context.Context, repository, p string) (string, error) {
			owner, repo, err := gh.SplitRepo(repository)
			if err != nil {
				return "", err
			}
			return gh.ReadFileAt(ctx, c, owner, repo, p, ref)
		}
	}
}

// keptPairings are the pairings (installation:path) a capability that stays
// carries in a file on record: removed with the disabled capability's files,
// the value pairs on all the same.
func keptPairings(ctx context.Context, read plan.Reader, remaining []plan.Remaining, inst, hub installations.Installation) (map[string]bool, error) {
	kept := map[string]bool{}
	for _, r := range remaining {
		for repo, files := range r.Result.Files {
			for p, f := range files {
				var peers []string
				for _, g := range f.Generated {
					if g.Peer != nil {
						peers = append(peers, g.Peer.Installation+":"+g.Peer.Path)
					}
				}
				if len(peers) == 0 {
					continue
				}
				_, err := read(ctx, plan.ResolveRepository(string(repo), inst, hub), p)
				switch {
				case errors.Is(err, gh.ErrNotFound):
					continue
				case err != nil:
					return nil, fmt.Errorf("%s:%s could not be read as you: %w", repo, p, err)
				}
				for _, peer := range peers {
					kept[peer] = true
				}
			}
		}
	}
	return kept, nil
}

// disableReferences name what still depends on capability on r, each a
// sentence naming the reference: the registry's hub runs the manager and the
// fleet's federation; a hub's muster brokers into its targets through its
// own agent platform; a portal proxies the installation's agent platform; a
// portal hosted on it shows other installations; hubs broker into its MCP
// servers; a value it shares with another installation leaves with it while
// no capability that stays carries it.
func disableReferences(registryHub string, r installations.Report, capability string, portals []installations.Portal, d plan.Disablement, kept map[string]bool) []string {
	out := []string{}
	if r.Name == registryHub {
		out = append(out, fmt.Sprintf("%s is the registry's hub: the manager, the Dev Portal and the fleet's federation run there", r.Name))
	}
	switch capability {
	case installations.AgentPlatform:
		if r.Federation != nil && len(r.Federation.Targets) > 0 {
			targets := make([]string, 0, len(r.Federation.Targets))
			for _, tg := range r.Federation.Targets {
				targets = append(targets, tg.Installation)
			}
			out = append(out, fmt.Sprintf("%s's muster brokers into %s for the portals that name it their broker: their federation runs through this agent platform", r.Name, strings.Join(targets, ", ")))
		}
		for _, p := range portals {
			if slices.Contains(p.PlatformProxied, r.Name) {
				out = append(out, fmt.Sprintf("the portal on %s proxies %s's agent platform (its app-config's agentPlatform.kagent.installations lists %s)", p.Host, r.Name, r.Name))
			}
		}
	case installations.CustomerPortal:
		if r.Hosted != nil && len(r.Hosted.Installations) > 0 {
			shown := make([]string, 0, len(r.Hosted.Installations))
			for _, f := range r.Hosted.Installations {
				shown = append(shown, f.Name)
			}
			out = append(out, fmt.Sprintf("the portal on %s shows %s besides %s (gs.installations): their Dex clients carry its redirect URI", r.Name, strings.Join(shown, ", "), r.Name))
		}
	case installations.ClusterMCPServers:
		if r.Federation != nil {
			var hubs []string
			for _, h := range r.Federation.Hubs {
				if h != r.Name {
					hubs = append(hubs, h)
				}
			}
			if len(hubs) > 0 {
				out = append(out, fmt.Sprintf("%s broker into %s's MCP servers: their portals list %s", strings.Join(hubs, ", "), r.Name, r.Name))
			}
		}
	}
	for _, p := range d.Pairings() {
		if kept[p] {
			continue
		}
		peer, file, _ := strings.Cut(p, ":")
		out = append(out, fmt.Sprintf("%s shares a value with %s, which holds the other side in %s: the disable removes %s's side and no capability that stays carries it (%s's token exchange into %s ends)", r.Name, peer, file, r.Name, peer, r.Name))
	}
	return out
}

// disableRefusal is why a commit of out is refused, or "": a file that could
// not be read, a reference that stands, the marker left on record, nothing
// to remove.
func disableRefusal(def installations.Capability, r installations.Report, out *DisableResult) string {
	if unknown := out.Plan.Unknown(); len(unknown) > 0 {
		names := make([]string, 0, len(unknown))
		for _, u := range unknown {
			names = append(names, fmt.Sprintf("%s:%s (%s)", u.Repository, u.Path, u.Error))
		}
		return fmt.Sprintf("%d file(s) could not be read as you: %s; nothing is removed blind", len(unknown), strings.Join(names, "; "))
	}
	if len(out.References) > 0 {
		return fmt.Sprintf("%s still depends on %s on %s: %s", referenceCount(len(out.References)), def.Name, r.Name, strings.Join(out.References, "; "))
	}
	repo, marker := def.Repository(r.Repositories), def.EnabledMarker(r.Name)
	if !out.Plan.Removes(repo, marker) {
		for _, s := range out.Plan.Stays {
			if s.Repository == repo && s.Path == marker {
				return fmt.Sprintf("the capability's marker %s stays (%s): %s would still read enabled on %s", marker, s.Why, def.Name, r.Name)
			}
		}
		return fmt.Sprintf("the disable does not remove the capability's marker %s: %s would still read enabled on %s", marker, def.Name, r.Name)
	}
	return ""
}

func referenceCount(n int) string {
	if n == 1 {
		return "one reference"
	}
	return fmt.Sprintf("%d references", n)
}

// disableCommit is mode commit: the action in pending approval, the pull
// requests as the caller in the plan's order, the checklist recorded as the
// action's orphans.
func (t *Tools) disableCommit(ctx context.Context, args map[string]any) (any, error) {
	tool := ToolDisableCapability
	id, _ := identity.FromContext(ctx)
	token, _ := identity.TokenFromContext(ctx)
	switch {
	case t.d.Actions == nil:
		return nil, fmt.Errorf("%s: mode commit records an Action on the hub and the manager runs without access to the hub's API server (chart actions.enabled, ACTIONS_NAMESPACE); dryRun: true answers the plan", tool)
	case t.d.Remote == nil:
		return nil, fmt.Errorf("%s: mode commit has no git remote to open the pull requests on", tool)
	case t.approvals == nil:
		return nil, fmt.Errorf("%s: mode commit asks the team's approval through klaus-gateway's Team review and no gateway is configured (chart approvals.gatewayURL; get_info reports approvals.configured): nothing is committed that no one can approve", tool)
	}
	reason, err := reasonArg(tool, args)
	if err != nil {
		return nil, err
	}
	out, env, err := t.disablePlan(ctx, args)
	if err != nil {
		return nil, err
	}
	if out.CommitRefused != "" {
		return nil, fmt.Errorf("%s: commit refused: %s; nothing is committed", tool, out.CommitRefused)
	}
	def, _ := installations.FindCapability(out.Capability)
	one := out.Installation
	test := installations.TestInstallation(one, env.byName[one].Customer, env.hub)
	if err := t.standupRefusal(tool, test); err != nil {
		return nil, err
	}
	deleted, updated := out.Plan.Changes()
	spec := actions.Spec{Actor: actions.Actor{Login: id.Login, ID: id.ID, Email: id.Email}, Capability: out.Capability, Installations: []string{one}, Inputs: env.inputs[one], Kind: actions.KindDisable,
		InputsByInstallation: map[string]map[string]any{one: env.inputs[one]}, Customer: env.byName[one].Customer != env.hub.Customer, AccountEngineers: accountEngineers(env, one), AccountEngineerOf: accountEngineerOf(env, one), Reason: reason, Markers: markersOf(def, env, one),
		Change: fmt.Sprintf("%d file(s) to delete, %d to update", deleted, updated), Changes: map[string][]string{one: disableSummary(out.Plan)}}
	a, err := t.record(ctx, spec, actions.Status{State: actions.StatePendingApproval})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	remote, err := t.d.Remote(token)
	if err != nil {
		return nil, t.fail(ctx, tool, a, nil, nil, err)
	}
	prs, err := t.openRemovals(ctx, env.c, a, out.Plan, remote, prTitle(actions.KindDisable, one, out.Capability, a.Name, ""), disableBody(a, out))
	if err != nil {
		return nil, t.fail(ctx, tool, a, remote, prs, err)
	}
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, actions.Status{State: actions.StatePendingApproval, PullRequests: prs, Orphans: orphansFrom(one, out.Plan.Checklist)})
	if err != nil {
		return nil, fmt.Errorf("%s: the pull requests are open (%s) and the action could not record them: %w", tool, prList(prs), err)
	}
	t.d.Log.Info(tool, identity.LogAttr(ctx), "action", a.Name, "installation", one, "state", a.Status.State, "pullRequests", len(prs))
	a, err = t.requestApproval(ctx, a, tool, test)
	if err != nil {
		return nil, fmt.Errorf("%w — the pull requests are open (%s) and the action pends approval; %s posts the review", err, prList(prs), ToolMergeAction)
	}
	out.DryRun, out.Commit, out.Action, out.PullRequests = false, "", a, prs
	if test {
		out.Next = fmt.Sprintf("%s is a test installation: no Team review; the pull requests are open as you, and once green you merge them with %s; the action is disabled once the marker is gone from the default branch", one, ToolMergeAction)
	} else {
		out.Next = fmt.Sprintf("the action waits for the team's approval (review %s in %s); the pull requests are open as you, and once approved and green you merge them with %s; the action is disabled once the marker is gone from the default branch", a.Status.Approval.ReviewID, a.Status.Approval.Channel, ToolMergeAction)
	}
	return out, nil
}

// openRemovals opens the disable's pull requests as the caller, one per
// repository in the plan's order on platform/<action>/<installation>: a
// deleted file is removed from the tree, an edited one written as the plan
// has it. An edited file is never one the repository's .sops.yaml encrypts:
// the manager decrypts nothing, so it edits no encrypted file.
func (t *Tools) openRemovals(ctx context.Context, c *gh.Client, a *actions.Action, d plan.Disablement, remote commit.Remote, title, body string) ([]actions.PullRequest, error) {
	req := commit.Request{Branch: branchPrefix + a.Name + "/" + d.Name, Title: title, Body: body}
	var prs []actions.PullRequest
	for _, pl := range d.PullRequests {
		files := map[string][]byte{}
		var enc *target
		for _, r := range d.Files {
			if r.Repository != pl.Repository {
				continue
			}
			switch r.Change {
			case plan.ChangeDelete:
				files[r.Path] = nil
			case plan.ChangeUpdate:
				if enc == nil {
					var err error
					if enc, err = targetAt(ctx, c, map[string]*target{}, pl.Repository); err != nil {
						return prs, err
					}
				}
				if enc.isSecretFile(r.Path) {
					return prs, fmt.Errorf("%s:%s is encrypted by the repository's rules and the disable would edit it: the manager decrypts nothing; nothing is committed", r.Repository, r.Path)
				}
				files[r.Path] = []byte(r.Content)
			}
		}
		if len(files) == 0 {
			continue
		}
		owner, repo, err := gh.SplitRepo(pl.Repository)
		if err != nil {
			return prs, err
		}
		base, err := gh.DefaultBranch(ctx, c, owner, repo)
		if err != nil {
			return prs, err
		}
		loc, err := provenance.Explicit(pl.Repository, base, "")
		if err != nil {
			return prs, err
		}
		opened, err := commit.Open(ctx, remote, req, []commit.Change{{Location: loc, Files: files}})
		for _, o := range opened {
			prs = append(prs, actions.PullRequest{Installation: d.Name, Repository: o.Repository.String(), Number: o.Number, URL: o.URL, State: actions.PullRequestOpen, Head: o.Head, HeadSHA: o.HeadSHA})
		}
		if err != nil {
			return prs, remoteError(pl.Repository, err)
		}
	}
	return prs, nil
}

// orphansFrom is the checklist as the action's orphans on installation.
func orphansFrom(installation string, checklist []plan.Object) []actions.Orphan {
	out := make([]actions.Orphan, 0, len(checklist))
	for _, o := range checklist {
		out = append(out, actions.Orphan{Installation: installation, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name})
	}
	return out
}

// disableSummary is the disable's change in lines for the review: each file
// with what happens to it.
func disableSummary(d plan.Disablement) []string {
	var out []string
	for _, r := range d.Files {
		out = append(out, fmt.Sprintf("%s %s:%s", r.Change, r.Repository, r.Path))
	}
	return out
}

// disableBody is the text of every pull request of a disable: the action,
// why, the pull requests in order, the files that stay and the checklist.
func disableBody(a *actions.Action, out *DisableResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Action `%s`: disable %s on %s, opened by %s as the person.\n\n", a.Name, a.Spec.Capability, out.Installation, ToolPrefix)
	if a.Spec.Reason != "" {
		fmt.Fprintf(&b, "Why: %s\n\n", a.Spec.Reason)
	}
	b.WriteString("Pull requests of this action, in order:\n")
	for _, pr := range out.Plan.PullRequests {
		fmt.Fprintf(&b, "%d. %s — %d file(s)\n", pr.Order, pr.Repository, pr.Changes)
	}
	if len(out.Plan.Stays) > 0 {
		b.WriteString("\nFiles of the definition that stay, rendered by a capability that stays on record:\n")
		for _, s := range out.Plan.Stays {
			fmt.Fprintf(&b, "- %s:%s (%s)\n", s.Repository, s.Path, s.Why)
		}
	}
	if len(out.Plan.Checklist) > 0 {
		b.WriteString("\nThe fleet's Kustomization over the tree does not prune: once merged, these objects stay on the cluster until a person deletes them, in this order:\n")
		for _, o := range out.Plan.Checklist {
			fmt.Fprintf(&b, "- [ ] %s\n", o)
		}
	}
	b.WriteString("\nThe action waits for the team's approval; merge follows it in this order.\n")
	return b.String()
}
