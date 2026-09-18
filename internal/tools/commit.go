package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/giantswarm/gitops-commit/commit"
	"github.com/giantswarm/gitops-commit/provenance"
	"github.com/giantswarm/gitops-commit/sopsenc"
	"github.com/google/go-github/v92/github"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// ArgSecrets is the commit's own argument: the supplied secret values by
// field. They exist inside the encrypted files and nowhere else.
const ArgSecrets = "secrets"

// SopsConfig is the file of a repository that names the recipients its secret
// files are encrypted for.
const SopsConfig = ".sops.yaml"

// RemoteFactory opens the git remote a commit lands on, with the caller's
// token — never one of the manager's.
type RemoteFactory func(token string) (commit.Remote, error)

// GitHubRemote is the production remote: GitHub at apiURL (empty:
// api.github.com), as the person whose token the call carries.
func GitHubRemote(apiURL string) RemoteFactory {
	return func(token string) (commit.Remote, error) {
		if apiURL == "" {
			return commit.NewGitHub(token)
		}
		return commit.NewGitHub(token, commit.WithBaseURL(apiURL))
	}
}

// CommitResult is the answer of mode commit: the Action in pending approval
// with its pull requests, and the plan they carry (markers, never values).
type CommitResult struct {
	Caller       string            `json:"caller"`
	Tool         string            `json:"tool"`
	Capability   string            `json:"capability"`
	Hub          string            `json:"hub"`
	DryRun       bool              `json:"dryRun"`
	Installation string            `json:"installation"`
	Action       *actions.Action   `json:"action,omitempty"`
	Plan         plan.Installation `json:"plan"`
	// PullRequests are the ones opened, in dependency order.
	PullRequests []actions.PullRequest `json:"pullRequests"`
	// UnchangedRepositories had nothing to commit once the secret files on
	// record were left alone (a value on record is never generated again).
	UnchangedRepositories []string `json:"unchangedRepositories,omitempty"`
	// Next says what follows.
	Next string `json:"next"`
}

// The branch of every pull request an action opens: platform/<action>/<installation>.
const branchPrefix = "platform/"

// capabilityCommit is mode commit of both tools for one installation: the
// opt-in gate, the Action in pending approval, the files encrypted for the
// repository's recipients, the pull requests as the caller in dependency order.
func (t *Tools) capabilityCommit(ctx context.Context, tool string, args map[string]any) (any, error) {
	id, _ := identity.FromContext(ctx)
	token, _ := identity.TokenFromContext(ctx)
	if t.d.Actions == nil {
		return nil, fmt.Errorf("%s: mode commit records an Action on the hub and the manager runs without access to the hub's API server (chart actions.enabled, ACTIONS_NAMESPACE); dryRun: true answers the plan", tool)
	}
	if t.d.Remote == nil {
		return nil, fmt.Errorf("%s: mode commit has no git remote to open the pull requests on", tool)
	}
	if t.approvals == nil {
		return nil, fmt.Errorf("%s: mode commit asks the team's approval through klaus-gateway's Team review and no gateway is configured (chart approvals.gatewayURL; get_info reports approvals.configured): nothing is committed that no one can approve", tool)
	}
	one, _ := args[ArgInstallation].(string)
	set := stringSlice(args[ArgInstallations])
	switch {
	case one != "" && len(set) > 0:
		return nil, fmt.Errorf("%s: mode commit takes %s (one installation) or %s (a set), not both", tool, ArgInstallation, ArgInstallations)
	case one == "" && tool == ToolEnableCapability:
		return nil, fmt.Errorf("%s: mode commit takes one installation (%s) — an enablement supplies its secret values; a set is %s's wave", tool, ArgInstallation, ToolReconcileCapability)
	case one == "":
		return t.capabilityWave(ctx, tool, args)
	}
	secrets, err := secretValues(args[ArgSecrets])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	delete(args, ArgSecrets)
	args[ArgContent] = true
	out, env, err := t.capabilityPlan(ctx, tool, args)
	if err != nil {
		return nil, err
	}
	kind := actions.KindEnable
	if tool == ToolReconcileCapability {
		kind = actions.KindReconcile
	}
	typed, _ := args[ArgInputs].(map[string]any)
	inputs := env.inputs[one]
	if inputs == nil {
		inputs = typed
	}
	spec := actions.Spec{Actor: actions.Actor{Login: id.Login, ID: id.ID, Email: id.Email}, Capability: out.Capability, Installations: []string{one}, Inputs: inputs, Kind: kind,
		Customer: env.byName[one].Customer != env.hub.Customer}

	// The opt-in gate: the one condition the manager checks itself, read now.
	if refusal := gateRefusal(*out, env.reports[one]); refusal != "" {
		a, err := t.record(ctx, spec, actions.Status{State: actions.StateRefused, Result: &actions.Result{State: actions.StateRefused, Message: refusal, At: now()}})
		if err != nil {
			return nil, fmt.Errorf("%s: commit refused: %s; and the refusal could not be recorded: %w", tool, refusal, err)
		}
		t.d.Log.Info(tool, identity.LogAttr(ctx), "action", a.Name, "installation", one, "state", actions.StateRefused)
		return nil, fmt.Errorf("%s: commit refused: %s — recorded on action %s", tool, refusal, a.Name)
	}

	p, ok := findPlan(*out, one)
	if !ok {
		return nil, fmt.Errorf("%s: %s was not rendered", tool, one)
	}
	if p.Refused != "" {
		return nil, fmt.Errorf("%s: the definition refuses these inputs for %s: %s", tool, one, p.Refused)
	}
	spec.Change = changeSummary(p)
	if n := p.Diff[plan.ChangeUnknown]; n > 0 {
		return nil, fmt.Errorf("%s: %d file(s) of %s could not be compared against the repository as you (%s); nothing is committed blind", tool, n, one, unknownFiles(p))
	}
	if err := checkSupplied(p.SuppliedSecrets, secrets); err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	def, ok := installations.FindCapability(out.Capability)
	if !ok {
		return nil, fmt.Errorf("%s: %q is not a capability definition", tool, out.Capability)
	}
	rendered, err := def.Render(inputs, secrets)
	if err != nil {
		return nil, fmt.Errorf("%s: render with the supplied values: %w", tool, err)
	}
	if _, err := targetsOf(ctx, env.c, p, rendered.Files, env.byName[one], env.hub); err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	res := CommitResult{Caller: identity.Caller(ctx), Tool: tool, Capability: out.Capability, Hub: out.Hub, Installation: one, Plan: p, PullRequests: []actions.PullRequest{}}
	if len(p.Files)-p.Diff[plan.ChangeUnchanged] == 0 {
		res.Next = "every file is on record as the definition renders it: nothing to commit, no action recorded"
		return res, nil
	}

	a, err := t.record(ctx, spec, actions.Status{State: actions.StatePendingApproval})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	remote, err := t.d.Remote(token)
	if err != nil {
		return nil, t.fail(ctx, tool, a, nil, err)
	}
	title := fmt.Sprintf("%s %s on %s (%s)", kind, out.Capability, one, a.Name)
	prs, unchanged, err := t.openPullRequests(ctx, env, a, p, out.PullRequests, rendered.Files, remote, title, prBody(a, p, out.PullRequests))
	res.UnchangedRepositories = unchanged
	if err != nil {
		return nil, t.fail(ctx, tool, a, prs, err)
	}
	a, err = t.d.Actions.UpdateStatus(ctx, a.Name, actions.Status{State: actions.StatePendingApproval, PullRequests: prs})
	if err != nil {
		return nil, fmt.Errorf("%s: the pull requests are open (%s) and the action could not record them: %w", tool, prList(prs), err)
	}
	t.d.Log.Info(tool, identity.LogAttr(ctx), "action", a.Name, "installation", one, "state", a.Status.State, "pullRequests", len(prs))
	a, err = t.askApproval(ctx, a, tool)
	if err != nil {
		return nil, fmt.Errorf("%w — the pull requests are open (%s) and the action pends approval; %s posts the review", err, prList(prs), ToolMergeAction)
	}
	res.Action = a
	res.PullRequests = prs
	res.Next = fmt.Sprintf("the action waits for the team's approval (review %s in %s); the pull requests are open as you, and once approved and green you merge them with %s", a.Status.Approval.ReviewID, a.Status.Approval.Channel, ToolMergeAction)
	return res, nil
}

// openPullRequests opens one installation's pull requests as the caller, one
// per repository in planned's order on platform/<action>/<installation>: the
// secret files encrypted for the repository's recipients, a plain file
// byte-equal to the plan. It answers the pull requests opened (also on
// error, for the record) and the repositories left with nothing to commit.
func (t *Tools) openPullRequests(ctx context.Context, env *planned, a *actions.Action, p plan.Installation, planned []plan.PullRequest, rendered render.Fileset, remote commit.Remote, title, body string) ([]actions.PullRequest, []string, error) {
	targets, err := targetsOf(ctx, env.c, p, rendered, env.byName[p.Name], env.hub)
	if err != nil {
		return nil, nil, err
	}
	req := commit.Request{Branch: branchPrefix + a.Name + "/" + p.Name, Title: title, Body: body}
	var prs []actions.PullRequest
	var unchanged []string
	for _, pl := range planned {
		tg := targets[pl.Repository]
		if tg == nil {
			continue
		}
		files, err := encrypt(pl.Repository, tg)
		if err != nil {
			return prs, unchanged, err
		}
		if len(files) == 0 {
			unchanged = append(unchanged, pl.Repository)
			continue
		}
		if leak := placeholderLeak(files); leak != "" {
			return prs, unchanged, fmt.Errorf("%s: %s still carries a placeholder after encryption; nothing is committed", pl.Repository, leak)
		}
		owner, repo, err := gh.SplitRepo(pl.Repository)
		if err != nil {
			return prs, unchanged, err
		}
		base, err := gh.DefaultBranch(ctx, env.c, owner, repo)
		if err != nil {
			return prs, unchanged, err
		}
		loc, err := provenance.Explicit(pl.Repository, base, "")
		if err != nil {
			return prs, unchanged, err
		}
		opened, err := commit.Open(ctx, remote, req, []commit.Change{{Location: loc, Files: files}})
		for _, o := range opened {
			prs = append(prs, actions.PullRequest{Installation: p.Name, Repository: o.Repository.String(), Number: o.Number, URL: o.URL, State: actions.PullRequestOpen, Head: o.Head, HeadSHA: o.HeadSHA})
		}
		if err != nil {
			return prs, unchanged, remoteError(pl.Repository, err)
		}
	}
	return prs, unchanged, nil
}

// record creates the Action with its initial status.
func (t *Tools) record(ctx context.Context, spec actions.Spec, status actions.Status) (*actions.Action, error) {
	name, err := actions.NewName(spec.Kind, spec.Installations[0])
	if err != nil {
		return nil, err
	}
	return t.d.Actions.Create(ctx, actions.Action{Name: name, Spec: spec, Status: status})
}

// fail moves the Action to failed with the pull requests opened so far and
// answers the error naming the action.
func (t *Tools) fail(ctx context.Context, tool string, a *actions.Action, prs []actions.PullRequest, cause error) error {
	status := actions.Status{State: actions.StateFailed, PullRequests: prs, Result: &actions.Result{State: actions.StateFailed, Message: cause.Error(), At: now()}}
	if _, err := t.d.Actions.UpdateStatus(ctx, a.Name, status); err != nil {
		return fmt.Errorf("%s: %w; and action %s could not record the failure: %v", tool, cause, a.Name, err)
	}
	t.d.Log.Info(tool, "action", a.Name, "state", actions.StateFailed, "pullRequests", len(prs))
	return fmt.Errorf("%s: %w — action %s is failed with %d pull request(s) open (%s)", tool, cause, a.Name, len(prs), prList(prs))
}

// gateRefusal is why the commit for r is refused, or "": the installation is
// not on record readably, or its opt-in declaration is absent, false or
// unreadable. It names the installation and the file every time.
func gateRefusal(out CapabilityResult, r installations.Report) string {
	for _, s := range out.Skipped {
		if s.Name == r.Name {
			return fmt.Sprintf("%s is %s (%s); %s is read before any write and nothing is written blind", r.Name, s.Reason, strings.Join(s.Errors, "; "), installations.OptInPath(r.Name))
		}
	}
	o := r.OptIn
	if o == nil || o.State == installations.OptedIn {
		return ""
	}
	var why string
	switch {
	case o.State == installations.OptInUnreadable:
		why = "could not be read as you: " + o.Error
	case !o.Present:
		why = "is absent"
	default:
		why = "says optIn: false"
	}
	return fmt.Sprintf("%s is %s: %s in %s %s — %s", r.Name, o.State, o.Path, o.Repository, why, o.HowToOptIn)
}

// target is one repository's share of the commit: the files that change,
// with the supplied values in, which of them exist on record, and the
// encrypter built from the repository's .sops.yaml — whose rules decide,
// by path, which files are secret files.
type target struct {
	enc    *sopsenc.Encryptor
	files  []sopsenc.File
	exists map[string]bool
}

// targetAt is repository's target, made on first use with its encrypter,
// read as the caller.
func targetAt(ctx context.Context, c *github.Client, targets map[string]*target, repository string) (*target, error) {
	if tg := targets[repository]; tg != nil {
		return tg, nil
	}
	enc, err := encrypter(ctx, c, repository)
	if err != nil {
		return nil, err
	}
	tg := &target{enc: enc, exists: map[string]bool{}}
	targets[repository] = tg
	return tg, nil
}

// encrypter reads repository's .sops.yaml as the caller and builds the
// encrypter for its recipients and its rules.
func encrypter(ctx context.Context, c *github.Client, repository string) (*sopsenc.Encryptor, error) {
	owner, repo, err := gh.SplitRepo(repository)
	if err != nil {
		return nil, err
	}
	sopsYAML, err := gh.ReadFile(ctx, c, owner, repo, SopsConfig)
	if err != nil {
		return nil, fmt.Errorf("%s has no %s readable as you (%w): the generated secrets are encrypted for the repository's recipients and nothing is written without them", repository, SopsConfig, err)
	}
	enc, err := sopsenc.New([]byte(sopsYAML))
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", repository, SopsConfig, err)
	}
	return enc, nil
}

// targetsOf pairs the render with the supplied values against the plan (the
// render with markers): only files that change are committed, a secret file
// (by the repository's .sops.yaml rules) that exists on record is never
// generated again, and a plain file must be byte-identical to the plan — a
// supplied value never lands outside a secret file. A kustomization is
// committed as the plan wrote it: the includes landed in it, or the entries
// of other owners kept in it, read as the caller now.
func targetsOf(ctx context.Context, c *github.Client, p plan.Installation, rendered render.Fileset, inst, hub installations.Installation) (map[string]*target, error) {
	planned := map[string]plan.File{}
	for _, f := range p.Files {
		planned[f.Repository+":"+f.Path] = f
	}
	out := map[string]*target{}
	for _, repo := range plan.SortedRepositories(rendered) {
		resolved := plan.ResolveRepository(string(repo), inst, hub)
		for path, f := range rendered[repo] {
			pf, ok := planned[resolved+":"+path]
			if !ok {
				return nil, fmt.Errorf("%s:%s is rendered but not in the plan", resolved, path)
			}
			if pf.Change == plan.ChangeUnchanged {
				continue
			}
			tg, err := targetAt(ctx, c, out, resolved)
			if err != nil {
				return nil, err
			}
			content := f.Content
			if !tg.enc.IsSecretFile(path) {
				if len(pf.Kept) == 0 && string(f.Content) != pf.Content {
					return nil, fmt.Errorf("%s:%s is a plain file and a supplied value would land in it; nothing is committed", resolved, path)
				}
				content = []byte(pf.Content)
			}
			tg.exists[path] = pf.Change == plan.ChangeUpdate
			sf := sopsenc.File{Path: path, Content: content}
			for _, g := range f.Generated {
				sf.Generated = append(sf.Generated, sopsenc.Generated{Name: g.Name, Placeholder: g.Placeholder, Kind: sopsenc.Kind(g.Kind), Length: g.Length, Half: sopsenc.Half(g.Half)})
			}
			tg.files = append(tg.files, sf)
		}
	}
	for _, inc := range p.Includes {
		pf, ok := planned[inc.Repository+":"+inc.Path]
		if !ok {
			return nil, fmt.Errorf("%s:%s lists %s but the kustomization is not in the plan", inc.Repository, inc.Path, inc.Resource)
		}
		if pf.Change != plan.ChangeUpdate {
			continue
		}
		tg, err := targetAt(ctx, c, out, inc.Repository)
		if err != nil {
			return nil, err
		}
		if !tg.exists[inc.Path] {
			tg.exists[inc.Path] = true
			tg.files = append(tg.files, sopsenc.File{Path: inc.Path, Content: []byte(pf.Content)})
		}
	}
	for _, tg := range out {
		sort.Slice(tg.files, func(i, j int) bool { return tg.files[i].Path < tg.files[j].Path })
	}
	return out, nil
}

// encrypt fills the generated values in and encrypts every secret file of
// repository for the recipients its .sops.yaml names; plain files pass
// through. A secret file on record is left out.
func encrypt(repository string, tg *target) (map[string][]byte, error) {
	files, err := tg.enc.Encrypt(tg.files, func(path string) bool { return tg.exists[path] })
	if err != nil {
		return nil, fmt.Errorf("%s: encrypt: %w", repository, err)
	}
	return files, nil
}

// The prefixes a generated and a supplied placeholder open with.
var placeholderPrefixes = []string{strings.TrimSuffix(render.Placeholder(""), ")"), strings.TrimSuffix(render.Supplied(""), ")")}

// placeholderLeak names a file whose outgoing content still carries a
// generated or supplied placeholder, or "".
func placeholderLeak(files map[string][]byte) string {
	for path, content := range files {
		for _, prefix := range placeholderPrefixes {
			if strings.Contains(string(content), prefix) {
				return path
			}
		}
	}
	return ""
}

// secretValues reads the secrets argument: an object of field → value.
func secretValues(v any) (map[string]string, error) {
	if v == nil {
		return map[string]string{}, nil
	}
	raw, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object of field: value", ArgSecrets)
	}
	out := make(map[string]string, len(raw))
	for field, value := range raw {
		s, ok := value.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("%s.%s must be a non-empty string", ArgSecrets, field)
		}
		out[field] = s
	}
	return out, nil
}

// checkSupplied refuses a commit whose supplied values do not match the
// fields the plan names: one missing, or one the plan does not ask for.
func checkSupplied(needed []string, secrets map[string]string) error {
	var missing, unknown []string
	for _, f := range needed {
		if secrets[f] == "" {
			missing = append(missing, f)
		}
	}
	for f := range secrets {
		if !slices.Contains(needed, f) {
			unknown = append(unknown, f)
		}
	}
	sort.Strings(unknown)
	switch {
	case len(missing) > 0:
		return fmt.Errorf("%s misses the value(s) of %s: the plan's suppliedSecrets name every field", ArgSecrets, strings.Join(missing, ", "))
	case len(unknown) > 0:
		return fmt.Errorf("%s names %s, which the plan does not ask for", ArgSecrets, strings.Join(unknown, ", "))
	}
	return nil
}

// remoteError says what GitHub refused and what the person can do about it.
func remoteError(repository string, err error) error {
	var auth *commit.AuthError
	if errors.As(err, &auth) {
		return fmt.Errorf("GitHub refused your token on %s (%s, HTTP %d): the App %s must be installed on the repository with contents: write and pull requests: write, and you must be able to write there — %w", repository, auth.Op, auth.Status, ToolPrefix, err)
	}
	return fmt.Errorf("%s: %w", repository, err)
}

// prBody is the text of every pull request of the action: the action id, the
// installation, the files and the generated secrets by name — never a value.
func prBody(a *actions.Action, p plan.Installation, prs []plan.PullRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Action `%s`: %s %s on %s, opened by %s as the person.\n\n", a.Name, a.Spec.Kind, a.Spec.Capability, p.Name, ToolPrefix)
	fmt.Fprintf(&b, "Pull requests of this action, in dependency order (configs before management-clusters):\n")
	for _, pr := range prs {
		fmt.Fprintf(&b, "%d. %s — %d file(s)\n", pr.Order, pr.Repository, pr.Changes)
	}
	if len(p.GeneratedSecrets) > 0 {
		b.WriteString("\nGenerated secrets, by name; the values exist only inside the encrypted files:\n")
		for _, g := range p.GeneratedSecrets {
			fmt.Fprintf(&b, "- %s (%s, %d)\n", g.Name, g.Kind, g.Length)
		}
	}
	if len(p.SuppliedSecrets) > 0 {
		fmt.Fprintf(&b, "\nSupplied secrets, by field: %s.\n", strings.Join(p.SuppliedSecrets, ", "))
	}
	b.WriteString("\nThe action waits for the team's approval; merge follows it in this order.\n")
	return b.String()
}

func findPlan(out CapabilityResult, name string) (plan.Installation, bool) {
	for _, p := range out.Installations {
		if p.Name == name {
			return p, true
		}
	}
	return plan.Installation{}, false
}

func unknownFiles(p plan.Installation) string {
	var names []string
	for _, f := range p.Files {
		if f.Change == plan.ChangeUnknown {
			names = append(names, f.Repository+":"+f.Path+": "+f.Error)
		}
	}
	return strings.Join(names, "; ")
}

func prList(prs []actions.PullRequest) string {
	names := make([]string, 0, len(prs))
	for _, pr := range prs {
		names = append(names, fmt.Sprintf("%s#%d", pr.Repository, pr.Number))
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

func now() *time.Time {
	t := time.Now().UTC()
	return &t
}
