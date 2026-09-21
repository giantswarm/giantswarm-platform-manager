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
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Cluster reads one installation's objects as the person: the loop-back
// through muster's kubernetes tools in production, a fake in tests. resource
// is a kind or kind.group as the render's probes name it. Serves is API
// discovery: whether the apiserver serves the resource (plural) of the group
// at the version.
type Cluster interface {
	Get(ctx context.Context, namespace, resource, name string) (map[string]any, error)
	List(ctx context.Context, namespace, resource, labelSelector string) ([]map[string]any, error)
	Logs(ctx context.Context, namespace, pod string) (string, error)
	Serves(ctx context.Context, group, version, resource string) (bool, error)
}

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
			r.Refused = err.Error()
			lv = nil
		}
	}
	x := &executor{opts: opts, lv: lv, refused: r.Refused}
	held := map[string]bool{}
	if lv != nil {
		for _, a := range lv.actions {
			if a.State == render.WaitingForCustomer && a.Dimension != "" {
				held[a.Dimension] = true
			}
		}
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
		for _, p := range anonymous {
			if p.Feature != fd.ID {
				continue
			}
			dim := Dimension{ID: p.ID, Kind: definitions.KindProbe, Key: p.Key, Mark: NotChecked, Reason: ReasonRepositorySide}
			if opts.AnonymousProbes {
				var clients []plan.DexClient
				if lv != nil {
					clients = lv.dexClients
				}
				dim = probe(ctx, opts.Probes, inputString(opts.Inputs.Values, "installation", "baseDomain"), clients, lv != nil, p)
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
		}
	}
	if lv.values != nil {
		lv.driven = drivenPaths(opts.Inputs.Values, flat, func(values map[string]any) (map[string]map[string]string, error) {
			_, _, other, err := renderValues(opts.Definition, values)
			return other, err
		})
	}
	return lv, nil
}

// dexPatchSuffix ends the path of the rendered dex-app values patch, the
// file that declares the Dex clients.
const dexPatchSuffix = "/apps/dex-app/configmap-values.yaml.patch"

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
	suffix := "/apps/" + def.Name + "/configmap-values.yaml.patch"
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
	for _, p := range x.lv.probes {
		if p.ID != d.ID {
			continue
		}
		c, diffs, auth := x.run(ctx, p)
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
	dim.Mark, dim.Reason = checkRollUp(live.Checks)
	sort.Slice(dim.Differences, func(i, j int) bool {
		return dim.Differences[i].Object+dim.Differences[i].Path < dim.Differences[j].Object+dim.Differences[j].Path
	})
	return dim
}

// checkRollUp is a dimension's mark from its checks: drifted when any is,
// differs by input when any does, else not checked when any object could not
// be read or probe target reached — a dimension only partly read never claims
// as defined — with that check's message as the reason, else as defined.
func checkRollUp(checks []Check) (Mark, string) {
	seen := map[Mark]*Check{}
	for i := range checks {
		if _, ok := seen[checks[i].Mark]; !ok {
			seen[checks[i].Mark] = &checks[i]
		}
	}
	for _, m := range []Mark{Drifted, DiffersByInput, Planned} {
		if _, ok := seen[m]; ok {
			return m, ""
		}
	}
	if c, ok := seen[NotChecked]; ok {
		return NotChecked, c.Message
	}
	return AsDefined, ""
}

// run executes one probe: the check, the differences a drift probe found,
// and muster's auth_required when the installation is not connected.
func (x *executor) run(ctx context.Context, p render.Probe) (Check, []Difference, *AuthRequired) {
	c := Check{Kind: string(p.Kind), Namespace: p.Namespace, Resource: p.Resource, Name: p.Name, URL: p.URL, Mark: NotChecked, Note: p.Expect.Note}
	if p.Kind == render.HTTP {
		x.httpProbe(&c, p)
		return c, nil, nil
	}
	if x.opts.Cluster == nil {
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
	obj, err := x.opts.Cluster.Get(ctx, p.Namespace, p.Resource, p.Name)
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

// present marks the object as existing, and a Secret as carrying the keys.
func (x *executor) present(ctx context.Context, c *Check, p render.Probe) error {
	obj, err := x.opts.Cluster.Get(ctx, p.Namespace, p.Resource, p.Name)
	if err != nil {
		return err
	}
	c.Mark, c.Message = AsDefined, "present"
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
	served, err := x.opts.Cluster.Serves(ctx, group, p.Expect.Version, resource)
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
	pods, err := x.opts.Cluster.List(ctx, p.Namespace, "Pod", p.Name)
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

// logAbsent reads the log of every pod of the workload and looks for the
// pattern that must not appear.
func (x *executor) logAbsent(ctx context.Context, c *Check, p render.Probe) error {
	re, err := regexp.Compile(p.Expect.Absent)
	if err != nil {
		return fmt.Errorf("pattern %q: %w", p.Expect.Absent, err)
	}
	obj, err := x.opts.Cluster.Get(ctx, p.Namespace, p.Resource, p.Name)
	if err != nil {
		return err
	}
	selector := selectorOf(obj)
	if selector == "" {
		return fmt.Errorf("%s %s/%s selects no pods (no spec.selector.matchLabels)", p.Resource, p.Namespace, p.Name)
	}
	pods, err := x.opts.Cluster.List(ctx, p.Namespace, "Pod", selector)
	if err != nil {
		return err
	}
	if len(pods) == 0 {
		c.Mark, c.Message = Drifted, "no pod matches "+selector
		return nil
	}
	for _, pod := range pods {
		log, err := x.opts.Cluster.Logs(ctx, p.Namespace, nameOf(pod))
		if err != nil {
			return err
		}
		if m := re.FindString(log); m != "" {
			c.Mark, c.Message = Drifted, nameOf(pod)+" logs "+strconv.Quote(m)
			return nil
		}
	}
	c.Mark, c.Message = AsDefined, fmt.Sprintf("%q in none of %d pod log(s)", p.Expect.Absent, len(pods))
	return nil
}

// httpProbe sends the anonymous request and holds the answer against the expectation.
func (x *executor) httpProbe(c *Check, p render.Probe) {
	client := x.opts.Probes
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	req, err := http.NewRequest(http.MethodGet, p.URL, nil)
	if err != nil {
		c.Mark, c.Message = NotChecked, err.Error()
		return
	}
	req.Header.Set("User-Agent", "giantswarm-platform-manager")
	resp, err := client.Do(req)
	if err != nil {
		c.Mark, c.Message = NotChecked, ReasonUnreachable+": "+strings.TrimSpace(err.Error())
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	c.Message = strconv.Itoa(resp.StatusCode)
	switch {
	case p.Expect.Status != 0 && resp.StatusCode != p.Expect.Status:
		c.Mark, c.Message = Drifted, fmt.Sprintf("%d, expected %d", resp.StatusCode, p.Expect.Status)
	case len(p.Expect.Statuses) > 0 && !slices.Contains(p.Expect.Statuses, resp.StatusCode):
		c.Mark, c.Message = Drifted, fmt.Sprintf("%d, expected one of %v", resp.StatusCode, p.Expect.Statuses)
	case p.Expect.LocationContains != "" && !strings.Contains(resp.Header.Get("Location"), p.Expect.LocationContains):
		c.Mark, c.Message = Drifted, fmt.Sprintf("%d, Location %q does not contain %q", resp.StatusCode, resp.Header.Get("Location"), p.Expect.LocationContains)
	case p.Expect.BodyContains != "" && !strings.Contains(string(body), p.Expect.BodyContains):
		c.Mark, c.Message = Drifted, fmt.Sprintf("%d, body does not contain %q", resp.StatusCode, p.Expect.BodyContains)
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
	obj, err := x.opts.Cluster.Get(ctx, p.Namespace, p.Resource, p.Name)
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
			diffs = append(diffs, Difference{Object: object, Path: cmp.Rendered, Rendered: rendered[""], Current: "", Input: x.lv.driven[valuesKey+"#"+cmp.Rendered]})
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
		c.Mark, c.Message = fileMark(diffs, true, false), fmt.Sprintf("%d difference(s)", len(diffs))
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
		full := join(prefix, p)
		if got, ok := live[p]; !ok || got != want {
			out = append(out, Difference{Object: object, Path: full, Rendered: want, Current: live[p], Input: x.lv.driven[valuesKey+"#"+full]})
		}
	}
	return out
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
		cm, err := x.opts.Cluster.Get(ctx, namespace, "ConfigMap", name)
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
