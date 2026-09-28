package agentplatform

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
	"github.com/giantswarm/giantswarm-platform-manager/render/customerportal"
)

// TestRenderConsumption renders the charts that consume the definitions' files —
// the fleet bases' HelmReleases over the emitted extras (kustomize build), the
// portal's tree with backstage, the agent-platform meta chart and the children
// it renders, dex-app — at the versions pinned in testdata/consumption/charts.yaml
// (the meta chart on both of its lines, each shape at the line its record
// selects), and checks every emitted Secret against what the rendered workloads read: a
// Secret nobody reads, a key a chart reads that the Secret does not carry, a Dex
// client whose secret the Deployment does not load. Values a shared template
// supplies on an installation (a Konfiguration ConfigMap) come from a stand-in
// file per chart in the same directory. The test needs helm, kustomize and the
// network and runs only with RENDER_CONSUMPTION=1.
func TestRenderConsumption(t *testing.T) {
	if os.Getenv("RENDER_CONSUMPTION") != "1" {
		t.Skip("set RENDER_CONSUMPTION=1 to render the consuming charts (needs helm, kustomize and the network)")
	}
	charts := &chartStore{dir: t.TempDir(), pins: loadPins(t)}
	for _, shape := range consumptionShapes {
		t.Run(shape.name, func(t *testing.T) { consume(t, shape, charts) })
	}
}

// consumptionShape is one installation the test proves: the customer-portal
// definition's input under testdata/consumption/ whose files compose the
// portal's tree, and the agent-platform shape (testdata/<platform>) whose
// files the portal's tree lists as its Component — "" for a portal-only
// installation, where the portal's files alone compose the backstage release
// and its own dex patch carries its Dex client.
type consumptionShape struct {
	name     string
	platform string
	portal   string
}

// portalInputSuffix names a shape's portal input under testdata/consumption/.
const portalInputSuffix = ".portal.yaml"

var consumptionShapes = []consumptionShape{
	{name: shapePublicCustomer, platform: shapePublicCustomer, portal: shapePublicCustomer + portalInputSuffix},
	{name: shapeGiantswarmOwned, platform: shapeGiantswarmOwned, portal: shapeGiantswarmOwned + portalInputSuffix},
	{name: shapeGiantswarmSlackApp, platform: shapeGiantswarmSlackApp, portal: shapeGiantswarmSlackApp + portalInputSuffix},
	{name: "customer-portal", portal: "customer-portal" + portalInputSuffix},
}

const (
	consumptionDir = "testdata/consumption"
	kindSecret     = "Secret"
	kindConfigMap  = "ConfigMap"
	kindHelmRel    = "HelmRelease"
	kindOCIRepo    = "OCIRepository"
	fieldSpec      = "spec"
	fieldName      = "name"
	fieldKey       = "key"
	fieldKind      = "kind"
	fieldOptional  = "optional"
)

// pin is one entry of charts.yaml: a consuming chart, its OCI repository and the
// version the test renders — for the meta chart and the children whose range
// differs by line, one entry per meta chart line (line), found by the shape's
// installation.chartLine; an entry without a line serves every shape.
type pin struct {
	Name     string `yaml:"name"`
	Line     string `yaml:"line"`
	Registry string `yaml:"registry"`
	Version  string `yaml:"version"`
}

// key names a pin in the pin map: the chart, and the line where it has one.
func (p pin) key() string { return pinKey(p.Name, p.Line) }

func pinKey(chart, line string) string {
	if line == "" {
		return chart
	}
	return chart + "@" + line
}

func loadPins(t *testing.T) map[string]pin {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(consumptionDir, "charts.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Charts []pin `yaml:"charts"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	pins := map[string]pin{}
	for _, p := range file.Charts {
		if _, err := semver.NewVersion(p.Version); err != nil || p.Registry == "" {
			t.Fatalf("charts.yaml: %s: needs a registry and a semver version, got %q %q", p.Name, p.Registry, p.Version)
		}
		if p.Line != "" && p.Line != lineThree && p.Line != lineFour {
			t.Fatalf("charts.yaml: %s: line %q is not a meta chart line the record selects (%s or %s)", p.Name, p.Line, lineThree, lineFour)
		}
		if _, dup := pins[p.key()]; dup {
			t.Fatalf("charts.yaml: %s is pinned twice", p.key())
		}
		pins[p.key()] = p
	}
	return pins
}

// chartStore pulls every pinned chart once.
type chartStore struct {
	dir  string
	pins map[string]pin
}

func (c *chartStore) pull(t *testing.T, p pin) string {
	t.Helper()
	tgz := filepath.Join(c.dir, p.Name+"-"+p.Version+".tgz")
	if _, err := os.Stat(tgz); errors.Is(err, os.ErrNotExist) {
		run(t, "helm", "pull", "oci://"+p.Registry, "--version", p.Version, "-d", c.dir)
	}
	return tgz
}

// run executes a fixed binary with test-controlled arguments and fails the test
// with its stderr when it does not exit 0.
func run(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...) // #nosec G204 -- fixed binary, arguments are test-controlled paths and pins
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return out
}

// object is one decoded manifest.
type object map[string]any

func (o object) kind() string { return str(o[fieldKind]) }
func (o object) name() string { return str(get(o, "metadata", fieldName)) }
func (o object) namespace(release string) string {
	if ns := str(get(o, "metadata", "namespace")); ns != "" {
		return ns
	}
	return release
}

// get walks a decoded document by keys; nil where the path does not exist.
func get(v any, path ...string) any {
	for _, p := range path {
		switch m := v.(type) {
		case object:
			v = m[p]
		case map[string]any:
			v = m[p]
		default:
			return nil
		}
	}
	return v
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func list(v any) []any { l, _ := v.([]any); return l }

func decode(t *testing.T, raw []byte) []object {
	t.Helper()
	var out []object
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var o map[string]any
		err := dec.Decode(&o)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("decoding rendered manifests: %v", err)
		}
		if o != nil {
			out = append(out, object(o))
		}
	}
}

// emittedSecret is a Secret the definition rendered, by namespace/name.
type emittedSecret struct {
	ns, name string
	keys     map[string]bool
}

func (s emittedSecret) String() string { return s.ns + "/" + s.name }

// release is one rendered Helm release: the chart at its pin, the values files
// in Helm order, and what it rendered.
type release struct {
	chart   pin
	name    string
	ns      string
	values  []string
	objects []object
	text    string
	// targets are the release's valuesFrom entries that read a key of an
	// emitted Secret into one value (targetPath), each at the index of values
	// where Flux merges it; the render takes none of them, since the Secret's
	// value is the commit step's.
	targets []target
}

// target is one valuesFrom entry with a targetPath: the Secret and the value.
type target struct {
	at     int
	secret string
	path   string
}

func (r *release) String() string {
	return r.chart.Name + " " + r.chart.Version + " (release " + r.name + ")"
}

// secretRef is one reference a rendered object makes to a key of a Secret, or
// to the whole Secret through envFrom.
type secretRef struct {
	rel      *release
	consumer string
	ns       string
	secret   string
	key      string
	env      string
	envFrom  bool
	optional bool
}

// volumeRef is a Secret-backed volume: a plain secret volume or a projected
// volume with several Secret sources, and where the pod mounts it.
type volumeRef struct {
	rel       *release
	consumer  string
	ns        string
	mountPath string
	sources   []volumeSource
}

// volumeSource is one Secret of a volume; items nil means the whole Secret.
type volumeSource struct {
	secret string
	items  []string
}

// consumption is everything collected for one shape.
type consumption struct {
	t *testing.T
	// installation is the installation both definitions render for; platform
	// is the agent-platform definition's input, nil for a portal-only shape.
	installation string
	platform     *Input
	dir          string
	charts       *chartStore
	gaps         map[string]knownGap
	emitted      map[string]emittedSecret
	refs         []secretRef
	volumes      []volumeRef
	// rendered are the releases templated so far; configMaps the emitted
	// ConfigMaps releases took values from.
	rendered   []*release
	configMaps []object
}

// knownGap is one entry of known-gaps.yaml: an emitted Secret a consuming chart
// does not read yet because of a tracked defect in the consumer.
type knownGap struct {
	Secret string `yaml:"secret"`
	Issue  string `yaml:"issue"`
	Reason string `yaml:"reason"`
}

func loadGaps(t *testing.T) map[string]knownGap {
	t.Helper()
	var file struct {
		Gaps []knownGap `yaml:"gaps"`
	}
	raw, err := fs.ReadFile(os.DirFS(consumptionDir), "known-gaps.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	gaps := map[string]knownGap{}
	for _, g := range file.Gaps {
		if g.Secret == "" || g.Issue == "" {
			t.Fatalf("known-gaps.yaml: every entry needs a namespace/name secret and an issue, got %+v", g)
		}
		gaps[g.Secret] = g
	}
	return gaps
}

func consume(t *testing.T, shape consumptionShape, charts *chartStore) {
	c := &consumption{t: t, dir: t.TempDir(), charts: charts, gaps: loadGaps(t), emitted: map[string]emittedSecret{}}
	portal := c.renderPortal(shape.portal)
	c.installation = portal.Installation.Name
	if shape.platform != "" {
		input, secrets := loadInput(t, shape.platform)
		in, err := Parse(input)
		if err != nil {
			t.Fatal(err)
		}
		ownPortal := slices.ContainsFunc(in.Installation.Portals, func(p PortalRef) bool {
			return p.Customer == in.Installation.Customer && p.Domain == portal.Portal.Domain
		})
		if in.Installation.Name != c.installation || !ownPortal || !portal.Installation.AgentPlatform {
			t.Fatalf("%s and %s/%s must describe one installation with the platform and its portal", shape.platform, consumptionDir, shape.portal)
		}
		result, err := Render(input, secrets, render.ModeCommit)
		if err != nil {
			t.Fatal(err)
		}
		c.write(result)
		c.platform = in
	} else if portal.Installation.AgentPlatform {
		t.Fatalf("%s/%s enables the platform but the shape renders none", consumptionDir, shape.portal)
	}
	extras, err := filepath.Glob(filepath.Join(c.dir, "giantswarm", "*-management-clusters", "management-clusters", c.installation, "extras", "*", "kustomization.yaml"))
	if err != nil || len(extras) == 0 {
		t.Fatalf("no extras directories rendered (%v)", err)
	}
	// The portal first, then every other extras directory.
	backstage := filepath.Join(c.dir, "giantswarm", portal.Installation.Customer+"-management-clusters", "management-clusters", c.installation, "extras", "backstage")
	c.backstage(backstage)
	for _, k := range extras {
		if dir := filepath.Dir(k); dir != backstage {
			c.extras(dir)
		}
	}
	c.dex()
	c.assertRead()
	c.assertKeys()
	c.assertWholeSecrets()
	if c.platform != nil {
		c.assertMusterConsumers()
	}
}

// renderPortal renders the customer-portal definition from its input under
// testdata/consumption/, the supplied values as dry-run markers, writes the
// fileset and returns the parsed input.
func (c *consumption) renderPortal(file string) *customerportal.Input {
	t := c.t
	raw, err := fs.ReadFile(os.DirFS(consumptionDir), file)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Input map[string]any `yaml:"input"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	in, err := customerportal.Parse(doc.Input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := customerportal.Render(doc.Input, in.SuppliedMarkers(), render.ModeCompare)
	if err != nil {
		t.Fatal(err)
	}
	c.write(result)
	return in
}

// write puts a definition's fileset into the shape's tree. A path already
// written is a second owner for one file, which the definitions rule out.
func (c *consumption) write(result *render.Result) {
	for repo, files := range result.Files {
		for path, f := range files {
			full := filepath.Join(c.dir, string(repo), path)
			if _, err := os.Stat(full); err == nil {
				c.t.Fatalf("%s:%s is rendered by both definitions", repo, path)
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				c.t.Fatal(err)
			}
			if err := os.WriteFile(full, f.Content, 0o600); err != nil {
				c.t.Fatal(err)
			}
		}
	}
}

// extras builds one emitted extras directory over the fleet base and renders
// every HelmRelease it yields; the meta chart's children are rendered in turn.
func (c *consumption) extras(dir string) {
	c.build(decode(c.t, run(c.t, "kustomize", "build", dir)))
}

// build records the emitted Secrets among built objects and renders every
// HelmRelease they carry, the children of a meta chart in turn.
func (c *consumption) build(objects []object) {
	for _, o := range objects {
		switch o.kind() {
		case kindSecret:
			s := emittedSecret{ns: o.namespace(""), name: o.name(), keys: map[string]bool{}}
			for _, field := range []string{"stringData", "data"} {
				for k := range mapOf(get(o, field)) {
					s.keys[k] = true
				}
			}
			c.emitted[s.String()] = s
		case kindConfigMap:
			c.configMaps = append(c.configMaps, o)
		}
	}
	pending := c.helmReleases(objects, true)
	for len(pending) > 0 {
		rel := pending[0]
		pending = pending[1:]
		c.template(rel)
		pending = append(pending, c.helmReleases(rel.objects, false)...)
	}
}

// backstage builds the portal's tree as the customer-portal definition renders
// it — extras/backstage/ of the host's management-clusters repository: the
// portal's own directory over the fleet base with the HelmRelease's values
// sources patched in and, with the platform, the agent-platform definition's
// Component listed next to it — and renders the backstage release it yields.
// The Component's patch appends the platform's values and its Vertex
// credentials to those sources; the chart then mounts the platform's
// app-config fragment and passes it as a --config file.
func (c *consumption) backstage(dir string) {
	t := c.t
	if _, err := os.Stat(filepath.Join(dir, portalDir)); c.platform != nil && err != nil {
		t.Fatalf("platform enabled but no %s directory rendered under the host's extras/backstage (%v)", portalDir, err)
	}
	c.extras(dir)
	for _, rel := range c.rendered {
		if rel.chart.Name != "backstage" {
			continue
		}
		c.assertPortalFragment(rel)
		return
	}
	t.Fatalf("the portal tree yielded no backstage release")
}

// assertPortalFragment checks the rendered portal reads the platform's
// app-config fragment when the platform is on — the Deployment mounts the
// emitted ConfigMap and passes the file as --config, and the fragment names
// this installation's kagent — and reads none without it: the portal's files
// alone compose the release.
func (c *consumption) assertPortalFragment(rel *release) {
	t := c.t
	mounted, arg := c.fragmentConsumed(rel)
	if c.platform == nil {
		if mounted || arg {
			t.Errorf("backstage %s: the portal-only shape consumes the platform's app-config fragment: ConfigMap %s mounted %v, --config %s passed %v", rel.chart.Version, portalAppConfigMap, mounted, portalAppConfigFile, arg)
		}
		return
	}
	if !mounted || !arg {
		t.Errorf("backstage %s: the platform's app-config fragment is not consumed: ConfigMap %s mounted %v, --config %s passed %v", rel.chart.Version, portalAppConfigMap, mounted, portalAppConfigFile, arg)
	}
	fragment := c.renderedConfigMap(portalAppConfigMap)
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(str(get(fragment, "data", portalAppConfigFile))), &cfg); err != nil {
		t.Fatalf("the platform's app-config fragment is not YAML: %v", err)
	}
	if c.platform.kagent() && get(cfg, "agentPlatform", "kagent", "installations", c.installation) == nil {
		t.Errorf("the platform's app-config fragment does not list this installation under agentPlatform.kagent.installations")
	}
}

// fragmentConsumed says whether the rendered portal mounts the platform's
// app-config ConfigMap and passes its file as --config.
func (c *consumption) fragmentConsumed(rel *release) (mounted, arg bool) {
	for _, o := range rel.objects {
		for _, pod := range podSpecs(map[string]any(o)) {
			for _, v := range list(pod["volumes"]) {
				if str(get(v, "configMap", fieldName)) == portalAppConfigMap {
					mounted = true
				}
			}
			for _, ctr := range list(pod["containers"]) {
				for _, a := range list(get(ctr, "args")) {
					if str(a) == portalAppConfigFile {
						arg = true
					}
				}
			}
		}
	}
	return mounted, arg
}

// renderedConfigMap is an emitted ConfigMap the fileset must carry.
func (c *consumption) renderedConfigMap(name string) object {
	o := c.emittedConfigMap(name)
	if o == nil {
		c.t.Fatalf("ConfigMap %s was not rendered", name)
	}
	return o
}

func mapOf(v any) map[string]any { m, _ := v.(map[string]any); return m }

// emittedConfigMap is an emitted ConfigMap by name — a HelmRelease's values
// source of the definition's own, rendered with its data — or nil when the
// ConfigMap is a shared template's, not in the fileset.
func (c *consumption) emittedConfigMap(name string) object {
	for _, o := range c.configMaps {
		if o.name() == name {
			return o
		}
	}
	return nil
}

// helmReleases turns the HelmReleases among objects into releases to render:
// chart and pin from the OCIRepository they reference, values from spec.values
// under the stand-in of a Konfiguration ConfigMap and the emitted patch over
// it. A release the fleet base declares must be pinned; a child of the meta
// chart must be pinned when its values name an emitted Secret and is skipped
// otherwise (it reads none of the definition's Secrets).
func (c *consumption) helmReleases(objects []object, fleet bool) []*release {
	t := c.t
	repos := map[string]object{}
	for _, o := range objects {
		if o.kind() == kindOCIRepo {
			repos[o.name()] = o
		}
	}
	var out []*release
	for _, hr := range objects {
		if hr.kind() != kindHelmRel {
			continue
		}
		repo, ok := repos[str(get(hr, fieldSpec, "chartRef", fieldName))]
		if !ok {
			t.Fatalf("HelmRelease %s: chartRef names no OCIRepository in the same render", hr.name())
		}
		url := str(get(repo, fieldSpec, "url"))
		chart := url[strings.LastIndex(url, "/")+1:]
		var values []byte
		if get(hr, fieldSpec, "values") != nil {
			var err error
			if values, err = yaml.Marshal(get(hr, fieldSpec, "values")); err != nil {
				t.Fatal(err)
			}
		}
		p, pinned := c.pin(chart)
		if !pinned {
			if fleet || c.mentionsEmitted(string(values)) {
				t.Fatalf("HelmRelease %s: chart %s (%s) is not pinned in %s/charts.yaml", hr.name(), chart, url, consumptionDir)
			}
			t.Logf("HelmRelease %s: chart %s reads none of the emitted Secrets; not rendered", hr.name(), chart)
			continue
		}
		if p.Registry != strings.TrimPrefix(url, "oci://") {
			t.Fatalf("chart %s: pinned registry %s, OCIRepository %s says %s", chart, p.Registry, repo.name(), url)
		}
		c.checkRange(p, hr.name(), str(get(repo, fieldSpec, "ref", "semver")))
		rel := &release{chart: p, name: str(get(hr, fieldSpec, "releaseName")), ns: str(get(hr, fieldSpec, "targetNamespace"))}
		if rel.name == "" {
			rel.name = hr.name()
		}
		if rel.ns == "" {
			rel.ns = hr.namespace("")
		}
		for _, vf := range list(get(hr, fieldSpec, "valuesFrom")) {
			if str(get(vf, fieldKind)) == kindSecret && str(get(vf, "targetPath")) != "" {
				rel.targets = append(rel.targets, target{at: len(rel.values), secret: str(get(vf, fieldName)), path: str(get(vf, "targetPath"))})
			}
			if str(get(vf, fieldKind)) != kindConfigMap {
				continue
			}
			if emitted := c.emittedConfigMap(str(get(vf, fieldName))); emitted != nil {
				key := str(get(vf, "valuesKey"))
				if key == "" {
					key = "values.yaml"
				}
				rel.values = append(rel.values, c.writeValues(emitted.name(), []byte(str(get(emitted, "data", key)))))
				continue
			}
			standIns := c.standIns(chart)
			if !fileExists(standIns[0]) {
				t.Fatalf("HelmRelease %s takes values from ConfigMap %s (a shared template, not in this repository): add %s with the Secret-selecting values it carries", hr.name(), str(get(vf, fieldName)), standIns[0])
			}
			rel.values = append(rel.values, standIns...)
			patch := c.configsFile(hr.name())
			if patch != "" {
				rel.values = append(rel.values, patch)
			}
		}
		if values != nil {
			rel.values = append(rel.values, c.writeValues(rel.name, values))
		}
		// A fleet HelmRelease's own Secret sources (the portal's valuesFrom)
		// are references of the release; a child's were collected with the
		// meta chart's render.
		if fleet {
			c.collect(rel, hr)
		}
		out = append(out, rel)
	}
	return out
}

// pin is chart's pin for this shape: the entry of the shape's meta chart line
// where charts.yaml carries one, else the entry without a line.
func (c *consumption) pin(chart string) (pin, bool) {
	if c.platform != nil {
		if p, ok := c.charts.pins[pinKey(chart, c.platform.Installation.ChartLine)]; ok {
			return p, true
		}
	}
	p, ok := c.charts.pins[chart]
	return p, ok
}

// checkRange asserts the pin lies in the range the OCIRepository follows. The
// meta chart's own range is the record's line (the 4 line pins itself, the 3
// line runs the base's range), and a child's range is the line's: a pin outside
// it is an entry of charts.yaml for the other line, or none for this one.
func (c *consumption) checkRange(p pin, hr, rng string) {
	if rng == "" {
		return
	}
	constraint, err := semver.NewConstraint(rng)
	if err != nil {
		c.t.Fatalf("HelmRelease %s: OCIRepository range %q: %v", hr, rng, err)
	}
	if constraint.Check(semver.MustParse(p.Version)) {
		return
	}
	line := ""
	if c.platform != nil {
		line = c.platform.Installation.ChartLine
	}
	c.t.Fatalf("chart %s: pinned %s is outside the range %s that HelmRelease %s follows on the %s line — %s/charts.yaml needs an entry for it with line: %q", p.Name, p.Version, rng, hr, line, consumptionDir, line)
}

// standIns are the files standing in for the shared template's ConfigMap of a
// chart: <chart>.values.yaml and, over it, <chart>.<line>.values.yaml where the
// template carries values on one meta chart line alone.
func (c *consumption) standIns(chart string) []string {
	files := []string{filepath.Join(consumptionDir, chart+".values.yaml")}
	if c.platform != nil {
		if byLine := filepath.Join(consumptionDir, chart+"."+c.platform.Installation.ChartLine+".values.yaml"); fileExists(byLine) {
			files = append(files, byLine)
		}
	}
	return files
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (c *consumption) mentionsEmitted(text string) bool {
	for _, s := range c.emitted {
		if strings.Contains(text, s.name) {
			return true
		}
	}
	return false
}

// configsFile is the emitted configmap patch of an app in the installation's
// configs repository, or "" when the definition renders none for it.
func (c *consumption) configsFile(app string) string {
	matches, _ := filepath.Glob(filepath.Join(c.dir, "giantswarm", "*-configs", "installations", c.installation, "apps", app, "configmap-values.yaml.patch"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func (c *consumption) writeValues(name string, values []byte) string {
	path := filepath.Join(c.dir, "values", name+".yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		c.t.Fatal(err)
	}
	if err := os.WriteFile(path, values, 0o600); err != nil {
		c.t.Fatal(err)
	}
	return path
}

// template renders a release with helm and collects the Secret references of
// everything it rendered.
func (c *consumption) template(rel *release) {
	out := c.helmTemplate(rel, rel.values)
	rel.text = string(out)
	rel.objects = decode(c.t, out)
	c.rendered = append(c.rendered, rel)
	for _, o := range rel.objects {
		c.collect(rel, o)
	}
}

// helmTemplate renders rel's chart at its pin, as rel, with the values files.
func (c *consumption) helmTemplate(rel *release, values []string) []byte {
	return run(c.t, "helm", append([]string{"template", rel.name, c.charts.pull(c.t, rel.chart), "-n", rel.ns}, flags(values)...)...)
}

// collect records every Secret reference of one rendered object: the pod
// templates' env, envFrom and volumes wherever a pod spec sits, a
// HelmRelease's valuesFrom, a kagent ModelConfig's API key.
func (c *consumption) collect(rel *release, o object) {
	consumer := o.kind() + "/" + o.name()
	ns := o.namespace(rel.ns)
	ref := func(secret, key, env string, envFrom, optional bool) {
		if secret != "" {
			c.refs = append(c.refs, secretRef{rel: rel, consumer: consumer, ns: ns, secret: secret, key: key, env: env, envFrom: envFrom, optional: optional})
		}
	}
	switch o.kind() {
	case kindHelmRel:
		for _, vf := range list(get(o, fieldSpec, "valuesFrom")) {
			if str(get(vf, fieldKind)) == kindSecret {
				key := str(get(vf, "valuesKey"))
				if key == "" {
					key = "values.yaml"
				}
				ref(str(get(vf, fieldName)), key, "", false, get(vf, fieldOptional) == true)
			}
		}
	case "ModelConfig":
		ref(str(get(o, fieldSpec, "apiKeySecret")), str(get(o, fieldSpec, "apiKeySecretKey")), "", false, false)
	}
	for _, pod := range podSpecs(map[string]any(o)) {
		mounts := map[string]string{}
		containers := append(list(pod["initContainers"]), list(pod["containers"])...)
		for _, ctr := range containers {
			for _, m := range list(get(ctr, "volumeMounts")) {
				mounts[str(get(m, fieldName))] = str(get(m, "mountPath"))
			}
			for _, e := range list(get(ctr, "env")) {
				if skr := get(e, "valueFrom", "secretKeyRef"); skr != nil {
					ref(str(get(skr, fieldName)), str(get(skr, fieldKey)), str(get(e, fieldName)), false, get(skr, fieldOptional) == true)
				}
			}
			for _, e := range list(get(ctr, "envFrom")) {
				ref(str(get(e, "secretRef", fieldName)), "", "", true, get(e, "secretRef", fieldOptional) == true)
			}
		}
		for _, v := range list(pod["volumes"]) {
			vol := volumeRef{rel: rel, consumer: consumer, ns: ns, mountPath: mounts[str(get(v, fieldName))]}
			if s := get(v, "secret"); s != nil {
				vol.sources = append(vol.sources, source(str(get(s, "secretName")), list(get(s, "items"))))
			}
			for _, src := range list(get(v, "projected", "sources")) {
				if s := get(src, "secret"); s != nil {
					vol.sources = append(vol.sources, source(str(get(s, fieldName)), list(get(s, "items"))))
				}
			}
			if len(vol.sources) > 0 {
				c.volumes = append(c.volumes, vol)
			}
		}
	}
}

// source is one Secret of a volume; an item's key is what the pod reads, its
// path (default: the key) the file name under the mount path.
func source(secret string, items []any) volumeSource {
	s := volumeSource{secret: secret}
	if items != nil {
		s.items = []string{}
	}
	for _, it := range items {
		s.items = append(s.items, str(get(it, fieldKey)))
	}
	return s
}

// podSpecs finds every pod spec in an object: a mapping with a containers
// list, wherever the kind puts it.
func podSpecs(v any) []map[string]any {
	var out []map[string]any
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["containers"].([]any); ok {
			return []map[string]any{x}
		}
		for _, child := range x {
			out = append(out, podSpecs(child)...)
		}
	case []any:
		for _, child := range x {
			out = append(out, podSpecs(child)...)
		}
	}
	return out
}

// dex renders dex-app with the stand-in of its shared template — the
// installation's Konfiguration with the platform's built-in clients, or
// without them on a portal-only installation — and the emitted patch, and
// checks every static client whose secret is a reference: the rendered
// configuration names an environment variable for it and the Deployment loads
// that variable from an emitted dex-client Secret's key.
func (c *consumption) dex() {
	t := c.t
	p, ok := c.charts.pins["dex-app"]
	if !ok {
		t.Fatalf("chart dex-app is not pinned in %s/charts.yaml", consumptionDir)
	}
	standIn := filepath.Join(consumptionDir, "dex-app.values.yaml")
	if c.platform == nil {
		standIn = filepath.Join(consumptionDir, "dex-app.without-platform.values.yaml")
	}
	patch := c.configsFile("dex-app")
	if patch == "" {
		t.Fatal("dex-app: the definition rendered no configmap-values.yaml.patch for it")
	}
	rel := &release{chart: p, name: "dex-app", ns: dexNamespace, values: []string{standIn, patch}}
	c.template(rel)

	var config map[string]any
	for _, o := range rel.objects {
		if o.kind() == kindSecret && o.name() == "dex" {
			if err := yaml.Unmarshal([]byte(str(get(o, "stringData", "config.yaml"))), &config); err != nil {
				t.Fatalf("dex-app: Secret dex config.yaml: %v", err)
			}
		}
	}
	envs := map[string]secretRef{}
	for _, r := range c.refs {
		if r.rel == rel && r.env != "" {
			envs[r.env] = r
		}
	}
	rendered := map[string]bool{}
	for _, client := range list(get(config, "staticClients")) {
		id, env := str(get(client, "id")), str(get(client, "secretEnv"))
		if env == "" {
			continue
		}
		rendered[id] = true
		r, loaded := envs[env]
		if !loaded {
			t.Errorf("dex-app: static client %s: config names %s, the Deployment sets no such variable", id, env)
			continue
		}
		s, emitted := c.emitted[dexNamespace+"/"+r.secret]
		if !emitted || !strings.HasPrefix(r.secret, dexClientSecretName("")) || r.key != dexSecretKey || !s.keys[r.key] {
			t.Errorf("dex-app: static client %s: the Deployment reads %s from %s/%s, not an emitted dex-client Secret's %q", id, env, r.secret, r.key, dexSecretKey)
		}
	}
	var expected []string
	standInValues, patchValues := readYAML(t, standIn), readYAML(t, patch)
	for name, client := range mapOf(get(patchValues, "oidc", "staticClients")) {
		if get(client, "clientSecretRef") == nil {
			continue
		}
		id := str(get(standInValues, "oidc", "staticClients", name, "clientID"))
		if id == "" {
			t.Fatalf("dex-app: the patch references built-in client %s; %s carries no clientID for it", name, standIn)
		}
		expected = append(expected, id)
	}
	for _, client := range list(get(patchValues, "oidc", "extraStaticClients")) {
		if get(client, "secretRef") != nil {
			expected = append(expected, str(get(client, "id")))
		}
	}
	for _, id := range expected {
		if !rendered[id] {
			t.Errorf("dex-app: client %s is referenced by the emitted patch but the rendered configuration has no client with a secret reference by that id", id)
		}
	}
	c.dexPods(rel)
}

// dexPods holds the SecretLoaded probes' pods and container to the rendered
// dex-app: render.DexPodSelector matches the pods of one workload, Dex's
// Deployment, and not the authenticator's, and its container
// render.DexContainer is the one that loads the client secrets' variables. A
// chart that relabels Dex's pods or renames the container fails here, not as
// "no pod matches" or an unread check on every installation.
func (c *consumption) dexPods(rel *release) {
	t := c.t
	want := map[string]string{}
	for _, term := range strings.Split(render.DexPodSelector, ",") {
		k, v, _ := strings.Cut(term, "=")
		want[k] = v
	}
	var matched []string
	for _, o := range rel.objects {
		labels := mapOf(get(o, "spec", "template", "metadata", "labels"))
		if labels == nil {
			continue
		}
		matches := true
		for k, v := range want {
			matches = matches && str(labels[k]) == v
		}
		if !matches {
			continue
		}
		matched = append(matched, o.kind()+" "+o.name())
		var loads bool
		for _, container := range list(get(o, "spec", "template", "spec", "containers")) {
			if str(get(container, fieldName)) != render.DexContainer {
				continue
			}
			for _, env := range list(get(container, "env")) {
				loads = loads || strings.HasPrefix(str(get(env, fieldName)), "DEX_CLIENT_SECRET_")
			}
		}
		if !loads {
			t.Errorf("dex-app: %s %s: no container %s loads a DEX_CLIENT_SECRET_ variable", o.kind(), o.name(), render.DexContainer)
		}
	}
	if len(matched) != 1 || !strings.HasPrefix(matched[0], "Deployment ") {
		t.Errorf("dex-app: %s must select Dex's Deployment and nothing else, matched %v", render.DexPodSelector, matched)
	}
}

// assertRead fails for every emitted Secret no rendered consumer references in
// its namespace. A Secret listed in known-gaps.yaml is reported with its issue
// instead; once a consumer reads it, the entry has to go with the fix.
func (c *consumption) assertRead() {
	read := map[string][]string{}
	for _, r := range c.refs {
		read[r.ns+"/"+r.secret] = append(read[r.ns+"/"+r.secret], r.rel.String())
	}
	for _, v := range c.volumes {
		for _, s := range v.sources {
			read[v.ns+"/"+s.secret] = append(read[v.ns+"/"+s.secret], v.rel.String())
		}
	}
	for _, name := range sortedKeys(c.emitted) {
		gap, known := c.gaps[name]
		switch {
		case len(read[name]) > 0 && known:
			c.t.Errorf("Secret %s: the known gap tracked by %s has closed (read by %v): remove the entry from %s/known-gaps.yaml", name, gap.Issue, read[name], consumptionDir)
		case len(read[name]) == 0 && known:
			c.t.Logf("Secret %s: emitted but nothing reads it — known gap, %s (%s)", name, gap.Reason, gap.Issue)
		case len(read[name]) == 0:
			c.t.Errorf("Secret %s: emitted but nothing reads it — no rendered chart references it in its namespace", name)
		}
	}
}

// assertKeys fails for every key a consumer reads from an emitted Secret that
// the Secret does not carry, optional references included: an emitted Secret is
// ours, and a missing optional key is a silently disabled feature.
func (c *consumption) assertKeys() {
	for _, r := range c.refs {
		s, emitted := c.emitted[r.ns+"/"+r.secret]
		if !emitted || r.key == "" {
			continue
		}
		if !s.keys[r.key] {
			c.t.Errorf("Secret %s: key %q is read by %s (%s%s) but the Secret carries %v", s, r.key, r.consumer, r.rel, optionalNote(r.optional), sortedKeys(s.keys))
		}
	}
	for _, v := range c.volumes {
		for _, src := range v.sources {
			s, emitted := c.emitted[v.ns+"/"+src.secret]
			if !emitted {
				continue
			}
			for _, key := range src.items {
				if !s.keys[key] {
					c.t.Errorf("Secret %s: key %q is projected by %s (%s) but the Secret carries %v", s, key, v.consumer, v.rel, sortedKeys(s.keys))
				}
			}
		}
	}
}

func optionalNote(optional bool) string {
	if optional {
		return ", optional"
	}
	return ""
}

// assertWholeSecrets checks the consumers that take a Secret as a whole. envFrom:
// the chart's own Secret — rendered once more without existingSecret and with
// dummy inline credentials from <chart>.inline-secret.values.yaml — names the
// keys it reads, and each must exist in the emitted Secret. A volume without
// items: every <mountPath>/<name> the release's manifests mention must be a
// key of one of the volume's emitted Secrets or an item of one of its sources.
func (c *consumption) assertWholeSecrets() {
	t := c.t
	for _, r := range c.refs {
		s, emitted := c.emitted[r.ns+"/"+r.secret]
		if !emitted || !r.envFrom {
			continue
		}
		inline := filepath.Join(consumptionDir, r.rel.chart.Name+".inline-secret.values.yaml")
		if _, err := os.Stat(inline); err != nil {
			t.Fatalf("%s reads Secret %s whole (envFrom): add %s so the chart renders its own Secret and names the keys it reads", r.rel, s, inline)
		}
		own := &release{chart: r.rel.chart, name: r.rel.name, ns: r.rel.ns, values: r.rel.values}
		own.objects = decode(t, c.helmTemplate(own, append(slices.Clone(own.values), inline)))
		var contract []string
		for _, o := range own.objects {
			if o.kind() == kindSecret {
				contract = append(contract, sortedKeys(mapOf(get(o, "data")))...)
				contract = append(contract, sortedKeys(mapOf(get(o, "stringData")))...)
			}
		}
		if len(contract) == 0 {
			t.Fatalf("%s: rendered with %s it still renders no Secret of its own; the envFrom contract cannot be read", r.rel, inline)
		}
		for _, key := range contract {
			if !s.keys[key] {
				t.Errorf("Secret %s: %s reads it with envFrom and its chart names the key %q, but the Secret carries %v", s, r.rel, key, sortedKeys(s.keys))
			}
		}
	}
	for _, v := range c.volumes {
		var whole []emittedSecret
		provided := map[string]bool{}
		for _, src := range v.sources {
			s, emitted := c.emitted[v.ns+"/"+src.secret]
			switch {
			case src.items != nil:
				for _, key := range src.items {
					provided[key] = true
				}
			case emitted:
				whole = append(whole, s)
				for key := range s.keys {
					provided[key] = true
				}
			default:
				continue
			}
		}
		if len(whole) == 0 || v.mountPath == "" {
			continue
		}
		paths := regexp.MustCompile(regexp.QuoteMeta(v.mountPath) + `/([A-Za-z0-9._-]+)(?:[^A-Za-z0-9._/-]|$)`)
		for _, m := range paths.FindAllStringSubmatch(v.rel.text, -1) {
			if !provided[m[1]] {
				t.Errorf("Secret %v: %s mounts it whole at %s and the release's manifests read %s/%s, a key it does not carry", whole, v.consumer, v.mountPath, v.mountPath, m[1])
			}
		}
	}
}

// assertMusterConsumers holds musterConsumers to the rendered charts: every
// workload whose pods read muster's credentials Secrets — an environment
// variable, envFrom or a volume — is the Deployment of a consumer that runs,
// and every consumer that runs has its Deployment rendered reading them (the
// runtime feature probes it by that name). On a line with the credentials
// revision each consumer's HelmRelease reads it, and the revision reaches the
// pod template: the child rendered once more with the revision Secret's
// targetPaths set, where Flux merges them, renders a different pod template
// for the Deployment. A consumer the list misses, a chart that renders none of
// the values, or a spec.values that shadows them fails here, not as a
// workload left on rotated-away credentials.
func (c *consumption) assertMusterConsumers() {
	t := c.t
	running := map[string]bool{}
	for _, mc := range c.platform.runningMusterConsumers() {
		running[mc.deployment] = true
	}
	credentials := func(ns, secret string) bool {
		return ns == platformNamespace && (secret == musterOAuthSecret || secret == musterValkeySecret)
	}
	readers := map[string]*release{}
	for _, r := range c.refs {
		// A HelmRelease's valuesFrom is Flux's, read again on every reconcile.
		if credentials(r.ns, r.secret) && !strings.HasPrefix(r.consumer, kindHelmRel+"/") {
			readers[r.consumer] = r.rel
		}
	}
	for _, v := range c.volumes {
		for _, s := range v.sources {
			if credentials(v.ns, s.secret) {
				readers[v.consumer] = v.rel
			}
		}
	}
	for _, consumer := range sortedKeys(readers) {
		rel := readers[consumer]
		if helmTest(rel, consumer) {
			continue
		}
		name, deployment := strings.CutPrefix(consumer, "Deployment/")
		if !deployment || !running[name] {
			t.Errorf("%s (%s) reads muster's credentials, but musterConsumers has no running consumer with that Deployment: a rotation leaves it on the old values", consumer, rel)
			continue
		}
		delete(running, name)
		if c.platform.musterRevision() {
			c.assertRolled(rel, consumer)
		}
	}
	for _, name := range sortedKeys(running) {
		t.Errorf("musterConsumers runs Deployment %s, but no rendered workload of that name reads muster's credentials in namespace %s: the list is stale or the Deployment is named otherwise, and its probe reads nothing", name, platformNamespace)
	}
}

// helmTest says whether the consumer is a chart's test hook, which Flux runs
// only when a HelmRelease asks for its tests, and none of these does.
func helmTest(rel *release, consumer string) bool {
	for _, o := range rel.objects {
		if o.kind()+"/"+o.name() == consumer {
			return strings.Contains(str(get(o, "metadata", "annotations", "helm.sh/hook")), "test")
		}
	}
	return false
}

// revisionMarker stands in for the revision the commit step generates.
const revisionMarker = "rolled"

// assertRolled renders rel once more with every targetPath its HelmRelease
// reads from the revision Secret set to revisionMarker, at the place in the
// values Flux merges it, and fails unless the consumer's pod template changes.
func (c *consumption) assertRolled(rel *release, consumer string) {
	t := c.t
	var values []string
	next, revisions := 0, 0
	for _, tg := range rel.targets {
		if tg.secret != musterRevisionSecret {
			continue
		}
		if strings.Contains(tg.path, `\`) {
			t.Fatalf("%s: targetPath %q escapes a key; the test sets plain dotted paths only", rel, tg.path)
		}
		var set any = revisionMarker
		keys := strings.Split(tg.path, ".")
		for i := len(keys) - 1; i >= 0; i-- {
			set = map[string]any{keys[i]: set}
		}
		raw, err := yaml.Marshal(set)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, rel.values[next:tg.at]...)
		values = append(values, c.writeValues(fmt.Sprintf("%s-revision-%d", rel.name, revisions), raw))
		next = tg.at
		revisions++
	}
	if revisions == 0 {
		t.Errorf("%s reads muster's credentials, but its HelmRelease (%s) takes no credentials revision from %s: a rotation leaves it on the old values", consumer, rel, musterRevisionSecret)
		return
	}
	values = append(values, rel.values[next:]...)
	rolled := decode(t, c.helmTemplate(rel, values))
	template := func(objects []object) string {
		for _, o := range objects {
			if o.kind()+"/"+o.name() == consumer {
				out, err := yaml.Marshal(get(o, fieldSpec, "template"))
				if err != nil {
					t.Fatal(err)
				}
				return string(out)
			}
		}
		t.Fatalf("%s: %s is not rendered", rel, consumer)
		return ""
	}
	if template(rel.objects) == template(rolled) {
		t.Errorf("%s: its HelmRelease reads the credentials revision, but %s's pod template does not change with it: the chart renders none of the target values onto the pod, or spec.values shadows them", rel, consumer)
	}
}

// readYAML decodes one YAML file of the test's own (a stand-in or an emitted file).
func readYAML(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return out
}

func flags(values []string) []string {
	var out []string
	for _, v := range values {
		out = append(out, "-f", v)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
