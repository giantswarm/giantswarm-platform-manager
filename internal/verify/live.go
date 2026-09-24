package verify

// The live comparison: the definition's probes of the running installation
// (render.Result.Probes), executed as the person through a Cluster, grouped
// into the live dimensions of features.yaml with the same three marks. This
// is the second registration's tool, verify_installation; the repository
// comparison stays with verify_capability, and Merge joins the two.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Cluster reads one installation's objects as the person: the loop-back
// through muster's kubernetes tools in production, a fake in tests. resource
// is a kind or kind.group as the render's probes name it; shape is how much
// of the object the check reads, and tail how many of a pod's last log lines.
// Serves is API discovery: whether the apiserver serves the resource (plural)
// of the group at the version.
type Cluster interface {
	Get(ctx context.Context, namespace, resource, name string, shape Shape) (map[string]any, error)
	List(ctx context.Context, namespace, resource, labelSelector string, shape Shape) ([]map[string]any, error)
	Logs(ctx context.Context, namespace, pod string, tail int) (string, error)
	Serves(ctx context.Context, group, version, resource string) (bool, error)
}

// Shape is how much of an object a check reads, and so how much a read asks
// the installation's kubernetes tool for. mcp-kubernetes answers a call up to
// a limit (128 KiB) and refuses a larger answer whole, and an object as the
// apiserver holds it — a HelmRelease with its history, its values and the
// last-applied configuration — is past it; a check asks for what it reads.
type Shape string

const (
	// Readiness is the object without its bulk: the conditions and the
	// revision, a workload's selector, a pod's phase, the keys of a Secret's
	// data — what every check but a drift probe reads. A HelmRelease's values
	// and history, a workload's long environment and every object's managed
	// fields and last-applied configuration are not part of it.
	Readiness Shape = "readiness"
	// Configuration is the object with every value it carries — a
	// HelmRelease's spec.values and valuesFrom, a ConfigMap's data, a
	// workload's arguments — and only the apiserver's bookkeeping (managed
	// fields, the last-applied configuration, condition timestamps) left out:
	// what a drift probe compares.
	Configuration Shape = "configuration"
	// Manifest is the object whole, its managed fields included: when each
	// field manager last changed what it owns — what a check of when a
	// Secret's data changed reads. The kubernetes tool masks a Secret's
	// values in every shape; a HelmRelease with its history is past the
	// tool's answer in this one, so only small objects are read so.
	Manifest Shape = "manifest"
)

// LogTail is how many of a pod's last log lines an absence check reads: a
// window that stays within what mcp-kubernetes answers on a busy workload,
// where the tool's maximum of 1000 lines does not.
const LogTail = 200

// logTailNote is the sentence next to an absence check: how much of the log it reads.
var logTailNote = fmt.Sprintf("the last %d lines of each pod's log are read", LogTail)

// Forbidden is the apiserver refusing a read as the person: a result, never
// a failure of the installation.
type Forbidden struct {
	Person string
	Reason string
}

func (e *Forbidden) Error() string { return "forbidden for " + e.Person + ": " + e.Reason }

// AuthRequired is muster's answer when the person's session is not
// connected to the installation's kubernetes server: no account there, or
// the exchange at its Dex failed. Relayed inside the result as muster said it.
type AuthRequired struct {
	Server  string `json:"server"`
	URL     string `json:"authUrl,omitempty"`
	Message string `json:"message"`
}

func (e *AuthRequired) Error() string { return e.Message }

// ErrNotFound is an object the installation does not have.
var ErrNotFound = errors.New("not found")

// TooLarge is the installation's kubernetes tool refusing to answer a read
// whole: mcp-kubernetes caps a tool's answer and answers response_too_large
// in place of a truncated object or log. A result of how much the check
// asked for, never of the installation.
type TooLarge struct {
	Bytes int
	Limit int
}

func (e *TooLarge) Error() string {
	return fmt.Sprintf("the object or log is larger than mcp-kubernetes answers (%s, the limit is %s): the check asks for too much", kib(e.Bytes), kib(e.Limit))
}

// kib is n bytes in KiB, rounded.
func kib(n int) string { return strconv.Itoa((n+512)/1024) + " KiB" }

// The bounds of one live verify's reads through muster. A read answers in a
// second or two when the installation's kubernetes tool is up; one that
// waits on a server between sessions, a tunnel or an apiserver that does not
// answer would otherwise hold the whole call until the caller's deadline
// (platformctl's default of 5 minutes) with no word about what hung. A read
// that does not answer within ReadTimeout is not checked, naming the object
// and the bound; once ReadBudget of one verify has gone into reads, the
// checks left are not read, naming the last read that did not answer — so
// every call answers what it has, well within the caller's deadline.
const (
	ReadTimeout = 20 * time.Second
	ReadBudget  = 2 * time.Minute
)

// Timeout is a read that did not answer within its bound: a result of the
// path to the installation — muster, the tunnel, its kubernetes tool, the
// apiserver — never of the installation's objects.
type Timeout struct {
	// What names the read: the object, the pod's log, the discovery.
	What  string
	After time.Duration
}

func (e *Timeout) Error() string {
	return fmt.Sprintf("no answer within %s from %s", e.After.Round(time.Millisecond), e.What)
}

// BudgetSpent is a read not started: the live verify's read budget went into
// reads that did not answer, and this check is left unread rather than
// holding the call.
type BudgetSpent struct {
	Budget time.Duration
	// Hung names the last read that did not answer.
	Hung string
}

func (e *BudgetSpent) Error() string {
	return fmt.Sprintf("not read: the live verify's read budget of %s is spent; the last read that did not answer: %s", e.Budget.Round(time.Millisecond), e.Hung)
}

// reader bounds the reads of one live verify through a Cluster: each within
// the read timeout, all within the budget, and remembers what did not answer
// for the checks that follow.
type reader struct {
	c        Cluster
	timeout  time.Duration
	budget   time.Duration
	deadline time.Time
	mu       sync.Mutex
	hung     string
}

// newReader bounds c with opts' bounds, the defaults where opts leave them.
func newReader(c Cluster, opts LiveOptions) *reader {
	timeout, budget := opts.ReadTimeout, opts.ReadBudget
	if timeout <= 0 {
		timeout = ReadTimeout
	}
	if budget <= 0 {
		budget = ReadBudget
	}
	return &reader{c: c, timeout: timeout, budget: budget, deadline: time.Now().Add(budget)}
}

// read runs one read of what under the bounds: the smaller of the read
// timeout and what is left of the budget; none once the budget is spent.
func (r *reader) read(ctx context.Context, what string, do func(context.Context) error) error {
	remaining := time.Until(r.deadline)
	if remaining <= 0 {
		return &BudgetSpent{Budget: r.budget, Hung: r.lastHung()}
	}
	bound := min(r.timeout, remaining)
	rctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	err := do(rctx)
	if err != nil && errors.Is(rctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		r.mu.Lock()
		r.hung = what
		r.mu.Unlock()
		return &Timeout{What: what, After: bound}
	}
	return err
}

func (r *reader) lastHung() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hung == "" {
		return "none: the reads answered and the budget went into them"
	}
	return r.hung
}

func (r *reader) Get(ctx context.Context, namespace, resource, name string, shape Shape) (map[string]any, error) {
	var out map[string]any
	err := r.read(ctx, resource+" "+namespace+"/"+name, func(ctx context.Context) (err error) {
		out, err = r.c.Get(ctx, namespace, resource, name, shape)
		return err
	})
	return out, err
}

func (r *reader) List(ctx context.Context, namespace, resource, labelSelector string, shape Shape) ([]map[string]any, error) {
	var out []map[string]any
	what := "the " + resource + " objects in " + namespace
	if labelSelector != "" {
		what += " matching " + labelSelector
	}
	err := r.read(ctx, what, func(ctx context.Context) (err error) {
		out, err = r.c.List(ctx, namespace, resource, labelSelector, shape)
		return err
	})
	return out, err
}

func (r *reader) Logs(ctx context.Context, namespace, pod string, tail int) (string, error) {
	var out string
	err := r.read(ctx, "the log of pod "+namespace+"/"+pod, func(ctx context.Context) (err error) {
		out, err = r.c.Logs(ctx, namespace, pod, tail)
		return err
	})
	return out, err
}

func (r *reader) Serves(ctx context.Context, group, version, resource string) (bool, error) {
	var out bool
	err := r.read(ctx, "the discovery of "+group+"/"+version+" "+resource, func(ctx context.Context) (err error) {
		out, err = r.c.Serves(ctx, group, version, resource)
		return err
	})
	return out, err
}

// The reasons a dimension is not checked on the live path.
const (
	// ReasonRepositorySide: a dimension of the files or an anonymous probe,
	// which verify_capability answers.
	ReasonRepositorySide = "compared against the repositories by verify_capability"
	// ReasonNoProbe: the render declares no probe for the dimension with the
	// inputs on record (kagent's dimensions with kagent off).
	ReasonNoProbe = "the render declares no probe for this dimension with the inputs on record"
	// ReasonNoValuesFile: a drift probe needs the rendered values file and
	// the definition renders none.
	ReasonNoValuesFile = "the definition renders no values file to compare the live values against"
)

// Check is one probe's outcome on one object or URL of the installation.
type Check struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Name      string `json:"name,omitempty"`
	URL       string `json:"url,omitempty"`
	Mark      Mark   `json:"mark"`
	// Message is what was seen, in one line: the condition and its message,
	// the status code, the apiserver's refusal, the number of differences.
	Message string `json:"message,omitempty"`
	// Detail is what the message rests on when that is more than the one
	// line: the transport's error behind a target unreachable from the manager.
	Detail string `json:"detail,omitempty"`
	// Note is the definition's sentence next to the probe.
	Note string `json:"note,omitempty"`
	// Revision is the revision a Flux object reports as applied or attempted
	// (a Kustomization's source revision, a HelmRelease's chart version),
	// when the object carries one: the rollout watch reads it.
	Revision string `json:"revision,omitempty"`
}

// LiveResult is what a live dimension's probes answered.
type LiveResult struct {
	Checks []Check `json:"checks"`
	// AuthRequired is muster's answer for the installation when the person's
	// session is not connected to it, as muster said it.
	AuthRequired *AuthRequired `json:"authRequired,omitempty"`
}

// LiveOptions shape one live verify.
type LiveOptions struct {
	Definition installations.Capability
	// Installation is the management cluster verified, by name.
	Installation string
	// State is the state on record the result starts from; drifted and
	// waiting for the customer replace it.
	State  installations.State
	Inputs Inputs
	// Cluster reads the installation as the person; nil when there are no
	// inputs on record (nothing is read then).
	Cluster Cluster
	// Probes sends the HTTP probes; nil is a client that does not follow redirects.
	Probes *http.Client
	// Person names who the reads run as, in a forbidden result.
	Person string
	// AnonymousProbes runs the definition's anonymous HTTP probes here too,
	// direct — the rollout watch's whole picture in one result. Off, they
	// read not checked: verify_capability's, on the repository side.
	AnonymousProbes bool
	// ReadTimeout and ReadBudget bound the reads through Cluster (ReadTimeout,
	// ReadBudget); zero is the default.
	ReadTimeout time.Duration
	ReadBudget  time.Duration
	// Log takes one line per check with its duration (live_check), so a
	// slow call is attributable after the fact; nil logs nothing.
	Log *slog.Logger
}

// CompareLive answers the live verify of opts' installation: every live
// dimension from its probes, every other dimension not checked here.
func CompareLive(ctx context.Context, opts LiveOptions) Result {
	r := Result{Installation: opts.Installation, Capability: opts.Definition.Name,
		State: opts.State, Inputs: opts.Inputs, Features: []Feature{}, Summary: map[Mark]int{}}
	feats, err := definitions.Features(opts.Definition.Name)
	if err != nil {
		r.Refused = err.Error()
		return r
	}
	anonymous, err := definitions.Probes(opts.Definition.Name)
	if err != nil {
		r.Refused = err.Error()
		return r
	}
	var lv *liveRender
	if opts.Inputs.Values != nil {
		if lv, err = renderLive(opts); err != nil {
			r.Refused = render.Reason(err)
			lv = nil
		}
	}
	x := &executor{opts: opts, lv: lv, refused: r.Refused, pr: newProber(opts.Probes)}
	x.sendHTTP(ctx)
	held := map[string]bool{}
	var clients []plan.DexClient
	if lv != nil {
		clients = lv.dexClients
		for _, a := range lv.actions {
			if a.State == render.WaitingForCustomer && a.Dimension != "" {
				held[a.Dimension] = true
			}
		}
	}
	var probed []Dimension
	if opts.AnonymousProbes {
		probed = x.pr.probeAll(ctx, probeData(opts.Installation, inputString(opts.Inputs.Values, "installation", "baseDomain"), opts.Inputs.Values), clients, lv != nil, anonymous)
	}
	drifted, heldDrift := 0, 0
	for _, fd := range feats {
		f := Feature{ID: fd.ID, Title: fd.Title, Marks: map[Mark]int{}, Dimensions: []Dimension{}}
		for _, d := range fd.Dimensions {
			dim := Dimension{ID: d.ID, Kind: d.Kind, Key: d.Key, Mark: NotChecked, Reason: ReasonRepositorySide}
			if d.Kind == definitions.KindLive {
				dim = x.dimension(ctx, d)
			}
			if dim.Mark == Drifted {
				drifted++
				if held[dim.ID] {
					heldDrift++
				}
			}
			f.Dimensions = append(f.Dimensions, dim)
		}
		for i, p := range anonymous {
			if p.Feature != fd.ID {
				continue
			}
			dim := Dimension{ID: p.ID, Kind: definitions.KindProbe, Key: p.Key, Mark: NotChecked, Reason: ReasonRepositorySide}
			if probed != nil {
				dim = probed[i]
				if dim.Mark == Drifted {
					drifted++
				}
			}
			f.Dimensions = append(f.Dimensions, dim)
		}
		for _, d := range f.Dimensions {
			f.Marks[d.Mark]++
			r.Summary[d.Mark]++
		}
		f.Mark = rollUp(f.Dimensions)
		r.Features = append(r.Features, f)
	}
	switch {
	case drifted > 0 && drifted == heldDrift:
		r.State = installations.StateWaitingForCustomer
	case drifted > 0:
		r.State = installations.StateDrifted
	}
	return r
}

// Merge joins the repository comparison and the live comparison of one
// installation into the one result a person reads: per dimension the one
// that checked it, the live result's word on a live dimension; drifted and
// waiting for the customer from either side.
func Merge(repo, live Result) Result {
	out := repo
	out.Features, out.Summary = []Feature{}, map[Mark]int{}
	out.LiveCaller = live.Caller
	byID := map[string]Dimension{}
	for _, f := range live.Features {
		for _, d := range f.Dimensions {
			byID[d.ID] = d
		}
	}
	for _, f := range repo.Features {
		nf := Feature{ID: f.ID, Title: f.Title, Marks: map[Mark]int{}, Dimensions: []Dimension{}}
		for _, d := range f.Dimensions {
			if ld, ok := byID[d.ID]; ok && d.Mark == NotChecked && (ld.Mark != NotChecked || d.Kind == definitions.KindLive) {
				d = ld
			}
			nf.Dimensions = append(nf.Dimensions, d)
			nf.Marks[d.Mark]++
			out.Summary[d.Mark]++
		}
		nf.Mark = rollUp(nf.Dimensions)
		out.Features = append(out.Features, nf)
	}
	switch {
	case live.State == installations.StateDrifted:
		out.State = live.State
	case live.State == installations.StateWaitingForCustomer && out.State != installations.StateDrifted:
		out.State = live.State
	}
	if out.Refused == "" {
		out.Refused = live.Refused
	}
	return out
}

// liveRender is the render from the inputs on record as the live comparison
// reads it: the probes and actions, and the values file flattened with the
// input every leaf is driven by.
type liveRender struct {
	probes  []render.Probe
	actions []render.Action
	// values is the rendered values file (the capability's
	// configmap-values.yaml.patch) flattened; nil when the definition
	// renders none. driven names the input behind each of its leaves.
	values map[string]string
	driven map[string]string
	// dexClients are the clients the rendered dex patch declares: what the
	// anonymous per-client probes run for.
	dexClients []plan.DexClient
	// file is the values file as the capability's removals and migrations
	// name it (the configmap: keys), with the keys read under the
	// installation's facts: a rendered leaf the live object lacks under a
	// key the migrations name is the planned addition it is on the record —
	// a definition released after the values on the installation were
	// written, the next reconcile's — not drift. nil without a values file.
	file      *fileDiff
	rms, migs plannedKeys
}

// plannedChange is the reason a live difference is a planned change, the
// way planned reads one on the record: a leaf the live object lacks under a
// key the migrations name, or a scalar merged as a set whose only change is
// entries the migrations add; nothing for a leaf the live object holds with
// another value, or lacks under no key.
func (lv *liveRender) plannedChange(d *Difference) string {
	if lv.file == nil {
		return ""
	}
	return planned(lv.file, d, lv.rms, lv.migs)
}

// valuesKey stands for the values file in the flat maps attribution works on.
const valuesKey = "values"

// renderLive renders the inputs on record for the live comparison.
func renderLive(opts LiveOptions) (*liveRender, error) {
	res, in, flat, err := renderValues(opts.Definition, opts.Inputs.Values)
	if err != nil {
		return nil, err
	}
	lv := &liveRender{probes: res.Probes, actions: res.Actions, values: flat[valuesKey]}
	for _, files := range res.Files {
		for path, f := range files {
			if strings.HasSuffix(path, dexPatchSuffix) {
				lv.dexClients = plan.DexClients(f.Content, in)
			}
			if strings.HasSuffix(path, valuesSuffix(opts.Definition.Name)) {
				lv.file = &fileDiff{path: path, kind: kindOf(path), documents: flattenLines(string(f.Content)).documents}
			}
		}
	}
	if lv.file != nil {
		if lv.rms, lv.migs, err = plannedKeysOf(opts.Definition.Name, liveFacts(opts.Inputs.Values)); err != nil {
			return nil, err
		}
	}
	if lv.values != nil {
		inputs, err := drivenInputs(opts.Definition, opts.Inputs)
		if err != nil {
			return nil, err
		}
		lv.driven = drivenPaths(opts.Inputs.Values, inputs, flat, func(values map[string]any) (map[string]map[string]string, error) {
			_, _, other, err := renderValues(opts.Definition, values)
			return other, err
		})
	}
	return lv, nil
}

// dexPatchSuffix ends the path of the rendered dex-app values patch, the
// file that declares the Dex clients.
const dexPatchSuffix = "/apps/dex-app/configmap-values.yaml.patch"

// valuesSuffix ends the path of the capability's rendered values file, the
// one the drift probes hold the live values against.
func valuesSuffix(capability string) string {
	return "/apps/" + capability + "/" + configMapPatch
}

// liveFacts are the installation's facts the planned keys are read under
// on the live path, from the inputs on record: the base domain.
func liveFacts(values map[string]any) facts {
	return facts{factDomain: inputString(values, "installation", "baseDomain")}
}

// plannedKeysOf reads the capability's removals and migrations under the
// installation's facts.
func plannedKeysOf(capability string, f facts) (rms, migs plannedKeys, err error) {
	removals, err := definitions.Removals(capability)
	if err != nil {
		return nil, nil, err
	}
	migrations, err := definitions.Migrations(capability)
	if err != nil {
		return nil, nil, err
	}
	return readRemovals(removals, f), readMigrations(migrations, f), nil
}

// renderValues renders values and flattens the definition's values file
// under valuesKey; a definition without one flattens nothing.
func renderValues(def installations.Capability, values map[string]any) (*render.Result, render.Input, map[string]map[string]string, error) {
	in, err := def.Parse(values)
	if err != nil {
		return nil, nil, nil, err
	}
	res, err := def.Render(values, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		return nil, nil, nil, err
	}
	suffix := valuesSuffix(def.Name)
	flat := map[string]map[string]string{}
	for _, files := range res.Files {
		for path, f := range files {
			if strings.HasSuffix(path, suffix) {
				flat[valuesKey] = flattenYAML(string(f.Content))
			}
		}
	}
	return res, in, flat, nil
}

// inputString reads a string leaf of the inputs on record by path.
func inputString(values map[string]any, path ...string) string {
	s, _ := dig(values, path...).(string)
	return s
}

// executor runs the probes of one live verify.
type executor struct {
	opts    LiveOptions
	lv      *liveRender
	refused string
	pr      *prober
	// reads bounds the reads through opts.Cluster (reader); nil without a
	// cluster.
	reads *reader
	// http holds the render's HTTP probes' checks by index into lv.probes,
	// sent all at once by sendHTTP before any dimension is built: a host
	// unreachable from the manager is waited for once, not in every
	// dimension in turn. The zero Check at the index of every other probe.
	http []Check
}

// sendHTTP runs the render's HTTP probes, all at once and under one deadline
// for the phase, into x.http.
func (x *executor) sendHTTP(ctx context.Context) {
	if x.lv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, ProbePhaseTimeout)
	defer cancel()
	x.http = make([]Check, len(x.lv.probes))
	var wg sync.WaitGroup
	for i, p := range x.lv.probes {
		if p.Kind != render.HTTP {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			x.http[i] = check(p)
			x.pr.answer(ctx, &x.http[i], p)
		}()
	}
	wg.Wait()
}

// check is the check of probe p before it ran.
func check(p render.Probe) Check {
	return Check{Kind: string(p.Kind), Namespace: p.Namespace, Resource: p.Resource, Name: p.Name, URL: p.URL, Mark: NotChecked, Note: p.Expect.Note}
}

// dimension runs every probe of the live dimension d and rolls them up.
func (x *executor) dimension(ctx context.Context, d definitions.Dimension) Dimension {
	dim := Dimension{ID: d.ID, Kind: d.Kind, Key: d.Key, Mark: NotChecked}
	switch {
	case x.refused != "":
		dim.Reason = x.refused
		return dim
	case x.lv == nil:
		dim.Reason = ReasonNoRender
		return dim
	}
	live := &LiveResult{Checks: []Check{}}
	for i, p := range x.lv.probes {
		if p.ID != d.ID {
			continue
		}
		var c Check
		var diffs []Difference
		var auth *AuthRequired
		if p.Kind == render.HTTP {
			c = x.http[i]
		} else {
			start := time.Now()
			c, diffs, auth = x.run(ctx, p)
			x.logCheck(p, c, time.Since(start))
		}
		live.Checks = append(live.Checks, c)
		dim.Differences = append(dim.Differences, diffs...)
		if auth != nil && live.AuthRequired == nil {
			live.AuthRequired = auth
		}
	}
	if len(live.Checks) == 0 {
		dim.Reason = ReasonNoProbe
		return dim
	}
	dim.Live = live
	dim.Mark, dim.Reason, dim.Detail = checkRollUp(live.Checks)
	sort.Slice(dim.Differences, func(i, j int) bool {
		return dim.Differences[i].Object+dim.Differences[i].Path < dim.Differences[j].Object+dim.Differences[j].Path
	})
	return dim
}

// checkRollUp is a dimension's mark from its checks: drifted when any is,
// differs by input when any does, else not checked when any object could not
// be read or probe target reached — a dimension only partly read never claims
// as defined — with that check's message as the reason and its detail as the
// detail, else as defined.
func checkRollUp(checks []Check) (Mark, string, string) {
	seen := map[Mark]*Check{}
	for i := range checks {
		if _, ok := seen[checks[i].Mark]; !ok {
			seen[checks[i].Mark] = &checks[i]
		}
	}
	for _, m := range []Mark{Drifted, DiffersByInput, Planned} {
		if _, ok := seen[m]; ok {
			return m, "", ""
		}
	}
	if c, ok := seen[NotChecked]; ok {
		return NotChecked, c.Message, c.Detail
	}
	return AsDefined, "", ""
}

// logCheck writes one line for a check of the installation's objects: the
// probe, what it read, its mark and how long the reads took — what a call
// that ran into a deadline is attributed by.
func (x *executor) logCheck(p render.Probe, c Check, took time.Duration) {
	if x.opts.Log == nil {
		return
	}
	x.opts.Log.Info("live_check", "installation", x.opts.Installation, "probe", p.ID, "kind", p.Kind, "resource", p.Resource, "namespace", p.Namespace, "name", p.Name, "mark", c.Mark, "message", c.Message, "duration_ms", took.Milliseconds())
}

// cluster is the installation's reads, bounded (reader); nil without one.
func (x *executor) cluster() Cluster {
	if x.reads == nil && x.opts.Cluster != nil {
		x.reads = newReader(x.opts.Cluster, x.opts)
	}
	if x.reads == nil {
		return nil
	}
	return x.reads
}

// run executes one probe of the installation's objects: the check, the
// differences a drift probe found, and muster's auth_required when the
// installation is not connected. The HTTP probes are sendHTTP's.
func (x *executor) run(ctx context.Context, p render.Probe) (Check, []Difference, *AuthRequired) {
	c := check(p)
	if x.cluster() == nil {
		c.Message = ReasonNoRender
		return c, nil, nil
	}
	var diffs []Difference
	var err error
	switch p.Kind {
	case render.HelmReleaseReady:
		err = x.condition(ctx, &c, p, "Ready", "True")
	case render.Condition:
		err = x.condition(ctx, &c, p, p.Expect.Condition, p.Expect.ConditionStatus)
	case render.ResourcePresent:
		err = x.present(ctx, &c, p)
	case render.PodsRunning:
		err = x.podsRunning(ctx, &c, p)
	case render.LogAbsent:
		err = x.logAbsent(ctx, &c, p)
	case render.Drift:
		diffs, err = x.drift(ctx, &c, p)
	case render.APIServed:
		err = x.apiServed(ctx, &c, p)
	case render.SecretLoaded:
		err = x.secretLoaded(ctx, &c, p)
	default:
		c.Message = "probe kind " + string(p.Kind) + " is not one this verify runs"
	}
	if err == nil {
		return c, diffs, nil
	}
	var forbidden *Forbidden
	var auth *AuthRequired
	switch {
	case errors.As(err, &auth):
		c.Mark, c.Message = NotChecked, "not connected to the installation in muster: "+firstLine(auth.Message)
		return c, nil, auth
	case errors.As(err, &forbidden):
		c.Mark, c.Message = NotChecked, forbidden.Error()
	case errors.Is(err, ErrNotFound):
		c.Mark, c.Message = Drifted, "does not exist: "+strings.TrimPrefix(err.Error(), ErrNotFound.Error()+": ")
	default:
		c.Mark, c.Message = NotChecked, err.Error()
	}
	return c, nil, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// condition marks the object's condition against the expected status.
func (x *executor) condition(ctx context.Context, c *Check, p render.Probe, condition, status string) error {
	obj, err := x.cluster().Get(ctx, p.Namespace, p.Resource, p.Name, Readiness)
	if err != nil {
		return err
	}
	got, message, found := conditionOf(obj, condition)
	c.Revision = revisionOf(obj)
	switch {
	case !found:
		c.Mark, c.Message = Drifted, "no "+condition+" condition"
	case got == status:
		c.Mark, c.Message = AsDefined, condition+"="+got
	default:
		c.Mark, c.Message = Drifted, condition+"="+got+": "+message
	}
	return nil
}

// present marks the object as existing, a Secret as carrying the keys, and
// an object with a status.state as reporting none the probe rules out.
func (x *executor) present(ctx context.Context, c *Check, p render.Probe) error {
	obj, err := x.cluster().Get(ctx, p.Namespace, p.Resource, p.Name, Readiness)
	if err != nil {
		return err
	}
	c.Mark, c.Message = AsDefined, "present"
	if p.Expect.NotState != "" {
		status, _ := obj["status"].(map[string]any)
		state, _ := status["state"].(string)
		switch state {
		case p.Expect.NotState:
			c.Mark, c.Message = Drifted, "state "+state
			if lastError, _ := status["lastError"].(string); lastError != "" {
				c.Message += ": " + firstLine(lastError)
			}
		case "":
			c.Message = "present, no state reported yet"
		default:
			c.Message = "present, state " + state
		}
	}
	if len(p.Expect.Keys) == 0 {
		return nil
	}
	data, _ := obj["data"].(map[string]any)
	var missing []string
	for _, k := range p.Expect.Keys {
		if _, ok := data[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		c.Mark, c.Message = Drifted, "missing keys "+strings.Join(missing, ", ")
	}
	return nil
}

// Rolling opens the message of a check that is red because the installation
// has not caught up with its record yet — an API the record enables and the
// apiserver does not serve before the control plane has rolled — and not
// because it is off its definition.
const Rolling = "rolling: "

// apiServed marks the API the probe names as served by the apiserver, found
// by discovery; not served, the check reads rolling.
func (x *executor) apiServed(ctx context.Context, c *Check, p render.Probe) error {
	resource, group, _ := strings.Cut(p.Resource, ".")
	served, err := x.cluster().Serves(ctx, group, p.Expect.Version, resource)
	if err != nil {
		return err
	}
	api := group + "/" + p.Expect.Version + " " + resource
	if served {
		c.Mark, c.Message = AsDefined, "served: "+api
		return nil
	}
	c.Mark, c.Message = Drifted, Rolling+api+" is not served yet"
	return nil
}

// podsRunning marks every pod the selector matches as Running.
func (x *executor) podsRunning(ctx context.Context, c *Check, p render.Probe) error {
	pods, err := x.cluster().List(ctx, p.Namespace, "Pod", p.Name, Readiness)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		c.Mark, c.Message = Drifted, "no pod matches "+p.Name
		return nil
	}
	var notRunning []string
	for _, pod := range pods {
		if phase, _ := dig(pod, "status", "phase").(string); phase != "Running" {
			notRunning = append(notRunning, nameOf(pod)+"="+phase)
		}
	}
	if len(notRunning) > 0 {
		c.Mark, c.Message = Drifted, strings.Join(notRunning, ", ")
		return nil
	}
	c.Mark, c.Message = AsDefined, fmt.Sprintf("%d pod(s) Running", len(pods))
	return nil
}

// secretLoaded marks every running container of the pods that read the
// Secret at start as started at or after the Secret's data last changed: the
// time of the managed-fields entry that owns the data, which a write of the
// same data leaves as it was and a change of the Secret's labels by the same
// manager moves too — so a check errs toward the container holding an older
// value, never the other way. The Secret's values are masked by the read and
// never looked at here. A container that is not running reads the Secret
// when it starts and is not counted.
func (x *executor) secretLoaded(ctx context.Context, c *Check, p render.Probe) error {
	secret, err := x.cluster().Get(ctx, p.Namespace, p.Resource, p.Name, Manifest)
	if err != nil {
		return err
	}
	changed, ok := dataChanged(secret)
	if !ok {
		c.Message = "no managed-fields entry of " + p.Resource + " " + p.Namespace + "/" + p.Name + " owns its data: when it changed is not known"
		return nil
	}
	pods, err := x.cluster().List(ctx, p.Namespace, "Pod", p.Expect.Pods, Readiness)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		c.Mark, c.Message = Drifted, "no pod matches "+p.Expect.Pods
		return nil
	}
	secretName := p.Resource + " " + p.Namespace + "/" + p.Name
	at := changed.UTC().Format(time.RFC3339)
	var running int
	var before []string
	for _, pod := range pods {
		started, ok := containerStarted(pod, p.Expect.Container)
		if !ok {
			continue
		}
		running++
		if started.Before(changed) {
			before = append(before, nameOf(pod)+" (at "+started.UTC().Format(time.RFC3339)+")")
		}
	}
	switch {
	case len(before) > 0:
		c.Mark, c.Message = Drifted, fmt.Sprintf("container %s of %s started before %s changed its data at %s: it holds the value from before", p.Expect.Container, strings.Join(before, ", "), secretName, at)
	case running == 0:
		c.Message = fmt.Sprintf("container %s runs in none of the %d pod(s) %s selects: it reads the Secret when it starts", p.Expect.Container, len(pods), p.Expect.Pods)
	default:
		c.Mark, c.Message = AsDefined, fmt.Sprintf("container %s of %d pod(s) started after %s changed its data at %s", p.Expect.Container, running, secretName, at)
	}
	return nil
}

// dataChanged is when an object's data last changed: the latest time of the
// managed-fields entries that own its data (or the stringData a Secret was
// written with).
func dataChanged(obj map[string]any) (time.Time, bool) {
	entries, _ := dig(obj, "metadata", "managedFields").([]any)
	var latest time.Time
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		fields, _ := entry["fieldsV1"].(map[string]any)
		_, data := fields["f:data"]
		_, stringData := fields["f:stringData"]
		if !data && !stringData {
			continue
		}
		s, _ := entry["time"].(string)
		if t, err := time.Parse(time.RFC3339, s); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest, !latest.IsZero()
}

// containerStarted is when the pod's container of the name started, while
// it runs.
func containerStarted(pod map[string]any, container string) (time.Time, bool) {
	statuses, _ := dig(pod, "status", "containerStatuses").([]any)
	for _, s := range statuses {
		status, _ := s.(map[string]any)
		if status["name"] != container {
			continue
		}
		startedAt, _ := dig(status, "state", "running", "startedAt").(string)
		t, err := time.Parse(time.RFC3339, startedAt)
		return t, err == nil
	}
	return time.Time{}, false
}

// logAbsent reads the last LogTail lines of the log of every pod of the
// workload and looks for the pattern that must not appear; the check's note
// says how much of the log was read.
func (x *executor) logAbsent(ctx context.Context, c *Check, p render.Probe) error {
	if c.Note != "" {
		c.Note += "; "
	}
	c.Note += logTailNote
	re, err := regexp.Compile(p.Expect.Absent)
	if err != nil {
		return fmt.Errorf("pattern %q: %w", p.Expect.Absent, err)
	}
	obj, err := x.cluster().Get(ctx, p.Namespace, p.Resource, p.Name, Readiness)
	if err != nil {
		return err
	}
	selector := selectorOf(obj)
	if selector == "" {
		return fmt.Errorf("%s %s/%s selects no pods (no spec.selector.matchLabels)", p.Resource, p.Namespace, p.Name)
	}
	pods, err := x.cluster().List(ctx, p.Namespace, "Pod", selector, Readiness)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		c.Mark, c.Message = Drifted, "no pod matches "+selector
		return nil
	}
	for _, pod := range pods {
		log, err := x.cluster().Logs(ctx, p.Namespace, nameOf(pod), LogTail)
		if err != nil {
			return err
		}
		if m := re.FindString(log); m != "" {
			c.Mark, c.Message = Drifted, nameOf(pod)+" logs "+strconv.Quote(m)
			return nil
		}
	}
	c.Mark, c.Message = AsDefined, fmt.Sprintf("%q in none of %d pod log(s), the last %d lines of each", p.Expect.Absent, len(pods), LogTail)
	return nil
}

// answer sends the anonymous request of the HTTP probe p (get) and holds the
// answer against the expectation, into c: not checked with ReasonUnreachable
// naming the host and the transport's error as the detail when there was none.
func (pr *prober) answer(ctx context.Context, c *Check, p render.Probe) {
	r, err := pr.get(ctx, p.URL, p.Expect.DexConnectorStep)
	if err != nil {
		c.Mark, c.Message, c.Detail = NotChecked, unreachable(p.URL), strings.TrimSpace(err.Error())
		return
	}
	c.Message = r.String()
	switch {
	case r.fault != "":
		c.Mark, c.Message = Drifted, fmt.Sprintf("%s: %s", r, r.fault)
	case p.Expect.Status != 0 && r.status != p.Expect.Status:
		c.Mark, c.Message = Drifted, fmt.Sprintf("%s, expected %d", r, p.Expect.Status)
	case len(p.Expect.Statuses) > 0 && !slices.Contains(p.Expect.Statuses, r.status):
		c.Mark, c.Message = Drifted, fmt.Sprintf("%s, expected one of %v", r, p.Expect.Statuses)
	case p.Expect.LocationContains != "" && !strings.Contains(r.location, p.Expect.LocationContains):
		c.Mark, c.Message = Drifted, fmt.Sprintf("%s, Location %q does not contain %q", r, r.location, p.Expect.LocationContains)
	case p.Expect.BodyContains != "" && !strings.Contains(string(r.body), p.Expect.BodyContains):
		c.Mark, c.Message = Drifted, fmt.Sprintf("%s, body does not contain %q", r, p.Expect.BodyContains)
	default:
		c.Mark = AsDefined
	}
}

// drift compares the live object with the render: the places the probe
// names, or a HelmRelease's whole user values against the values file.
func (x *executor) drift(ctx context.Context, c *Check, p render.Probe) ([]Difference, error) {
	if x.lv.values == nil {
		c.Mark, c.Message = NotChecked, ReasonNoValuesFile
		return nil, nil
	}
	obj, err := x.cluster().Get(ctx, p.Namespace, p.Resource, p.Name, Configuration)
	if err != nil {
		return nil, err
	}
	object := p.Resource + " " + p.Namespace + "/" + p.Name
	var diffs []Difference
	if len(p.Expect.Compare) == 0 {
		kind, _, _ := strings.Cut(p.Resource, ".")
		if kind != "HelmRelease" {
			c.Mark, c.Message = NotChecked, "the probe names no place of the object to compare"
			return nil, nil
		}
		live, err := x.helmReleaseValues(ctx, p.Namespace, obj)
		if err != nil {
			return nil, err
		}
		diffs = x.differences(object, "", x.lv.values, live)
	}
	var absent []string
	for _, cmp := range p.Expect.Compare {
		rendered := subtree(x.lv.values, cmp.Rendered)
		if len(rendered) == 0 {
			// A place the render leaves to the shared template for these
			// inputs: nothing of the definition's to hold the live value against.
			absent = append(absent, cmp.Rendered)
			continue
		}
		liveValue, ok := resolve(obj, cmp.Live, cmp.Prefix)
		if !ok {
			diffs = append(diffs, x.difference(object, cmp.Rendered, rendered[""], "", true))
			continue
		}
		flatLive := map[string]string{}
		flatten(liveValue, "", flatLive)
		diffs = append(diffs, x.differences(object, cmp.Rendered, rendered, flatLive)...)
	}
	switch {
	case len(absent) == len(p.Expect.Compare) && len(absent) > 0:
		c.Mark, c.Message = NotChecked, "the render carries no value at "+strings.Join(absent, ", ")
	case len(diffs) == 0:
		c.Mark, c.Message = AsDefined, "equal to the render"
	default:
		c.Mark = fileMark(diffs, true, false)
		c.Message = fmt.Sprintf("%d difference(s)", len(diffs))
		if c.Mark == Planned {
			c.Message = fmt.Sprintf("%d planned change(s)", len(diffs))
		}
	}
	if len(absent) > 0 && c.Mark != NotChecked {
		c.Message += "; not rendered for these inputs: " + strings.Join(absent, ", ")
	}
	return diffs, nil
}

// differences are the rendered leaves under prefix the live leaves do not
// carry the same way; a live leaf the render does not name is not a
// difference (the shared template carries values the patch does not).
func (x *executor) differences(object, prefix string, rendered, live map[string]string) []Difference {
	var out []Difference
	for p, want := range rendered {
		if got, ok := live[p]; !ok || got != want {
			out = append(out, x.difference(object, join(prefix, p), want, got, !ok))
		}
	}
	return out
}

// difference is one place of the live object off the render at path: the
// input that drives the leaf, and — for a leaf the live object lacks
// (absent) under a key the capability's migrations name — the planned
// change it is, as it is on the record.
func (x *executor) difference(object, path, rendered, current string, absent bool) Difference {
	d := Difference{Object: object, Path: path, Rendered: rendered, Current: current, Input: x.lv.driven[valuesKey+"#"+path], absent: absent}
	d.Planned = x.lv.plannedChange(&d)
	return d
}

// helmReleaseValues are the user values a HelmRelease reads: its valuesFrom
// ConfigMaps in order, then spec.values on top, flattened.
func (x *executor) helmReleaseValues(ctx context.Context, namespace string, hr map[string]any) (map[string]string, error) {
	merged := map[string]any{}
	sources, _ := dig(hr, "spec", "valuesFrom").([]any)
	for _, s := range sources {
		src, _ := s.(map[string]any)
		if kind, _ := src["kind"].(string); kind != "ConfigMap" {
			continue
		}
		name, _ := src["name"].(string)
		key, _ := src["valuesKey"].(string)
		if key == "" {
			key = "values.yaml"
		}
		cm, err := x.cluster().Get(ctx, namespace, "ConfigMap", name, Configuration)
		if err != nil {
			return nil, fmt.Errorf("valuesFrom ConfigMap %s: %w", name, err)
		}
		raw, _ := dig(cm, "data", key).(string)
		var doc map[string]any
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, fmt.Errorf("valuesFrom ConfigMap %s key %s: %w", name, key, err)
		}
		deepMerge(merged, doc)
	}
	if inline, ok := dig(hr, "spec", "values").(map[string]any); ok {
		deepMerge(merged, inline)
	}
	out := map[string]string{}
	flatten(merged, "", out)
	return out, nil
}

// deepMerge puts over into base, maps merged key by key, everything else replaced.
func deepMerge(base, over map[string]any) {
	for k, v := range over {
		if ov, ok := v.(map[string]any); ok {
			if bv, ok := base[k].(map[string]any); ok {
				deepMerge(bv, ov)
				continue
			}
		}
		base[k] = v
	}
}

// subtree is the flat leaves under path, keyed relative to it ("" for the
// leaf at path itself).
func subtree(flat map[string]string, path string) map[string]string {
	out := map[string]string{}
	for p, v := range flat {
		switch {
		case p == path:
			out[""] = v
		case strings.HasPrefix(p, path+"."):
			out[p[len(path)+1:]] = v
		case strings.HasPrefix(p, path+"["):
			out[p[len(path):]] = v
		}
	}
	return out
}

func join(prefix, p string) string {
	switch {
	case prefix == "":
		return p
	case p == "":
		return prefix
	case strings.HasPrefix(p, "["):
		return prefix + p
	}
	return prefix + "." + p
}

// resolve walks a dotted path in a live object. A key that contains dots
// (data["config.yaml"]) is matched greedily; [n] indexes a list; "field:path"
// crosses into a YAML document stored in the string field; a final [args]
// picks the list element that starts with prefix and strips the prefix.
func resolve(obj any, path, prefix string) (any, bool) {
	outer, inner, crosses := strings.Cut(path, ":")
	v, ok := walk(obj, outer, prefix)
	if !ok || !crosses {
		return v, ok
	}
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	var doc any
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		return nil, false
	}
	return walk(doc, inner, prefix)
}

func walk(v any, path, prefix string) (any, bool) {
	segs := strings.Split(path, ".")
	for len(segs) > 0 {
		seg, index, isList := strings.Cut(segs[0], "[")
		if isList {
			index = strings.TrimSuffix(index, "]")
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		// Greedy: the longest run of segments that is one key.
		n := 0
		for k := len(segs); k >= 1; k-- {
			key := strings.Join(segs[:k], ".")
			if k == 1 {
				key = seg
			}
			if _, ok := m[key]; ok {
				v, n = m[key], k
				break
			}
		}
		if n == 0 {
			return nil, false
		}
		segs = segs[n:]
		if !isList {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			return nil, false
		}
		if index == "args" {
			for _, e := range list {
				if s, ok := e.(string); ok && strings.HasPrefix(s, prefix) {
					v = strings.TrimPrefix(s, prefix)
					return v, true
				}
			}
			return nil, false
		}
		i, err := strconv.Atoi(index)
		if err != nil || i < 0 || i >= len(list) {
			return nil, false
		}
		v = list[i]
	}
	return v, true
}

// conditionOf is the object's condition of that type: status, message, found.
func conditionOf(obj map[string]any, condition string) (string, string, bool) {
	conditions, _ := dig(obj, "status", "conditions").([]any)
	for _, c := range conditions {
		m, _ := c.(map[string]any)
		if t, _ := m["type"].(string); t == condition {
			status, _ := m["status"].(string)
			message, _ := m["message"].(string)
			return status, message, true
		}
	}
	return "", "", false
}

// revisionOf is the revision a Flux object reports: a Kustomization's
// lastAppliedRevision (its source, "main@sha1:…"), a HelmRelease's attempted
// chart version, else its newest history entry's; "" for anything else.
func revisionOf(obj map[string]any) string {
	for _, key := range []string{"lastAppliedRevision", "lastAttemptedRevision"} {
		if rev, _ := dig(obj, "status", key).(string); rev != "" {
			return rev
		}
	}
	if history, _ := dig(obj, "status", "history").([]any); len(history) > 0 {
		if rev, _ := dig(history[0], "chartVersion").(string); rev != "" {
			return rev
		}
	}
	return ""
}

// selectorOf is a workload's spec.selector.matchLabels as a label selector.
func selectorOf(obj map[string]any) string {
	labels, _ := dig(obj, "spec", "selector", "matchLabels").(map[string]any)
	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func nameOf(obj map[string]any) string {
	name, _ := dig(obj, "metadata", "name").(string)
	return name
}

// dig walks nested maps.
func dig(v any, path ...string) any {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[k]
	}
	return v
}
