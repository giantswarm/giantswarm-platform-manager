package agentplatform

import (
	"bytes"
	"errors"
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
)

// TestRenderConsumption renders the charts that consume the definition's files —
// the fleet bases' HelmReleases over the emitted extras (kustomize build), the
// agent-platform meta chart and the children it renders, dex-app — at the
// versions pinned in testdata/consumption/charts.yaml, and checks every emitted
// Secret against what the rendered workloads read: a Secret nobody reads, a key
// a chart reads that the Secret does not carry, a Dex client whose secret the
// Deployment does not load. Values a shared template supplies on an
// installation (a Konfiguration ConfigMap) come from a stand-in file per chart
// in the same directory. The test needs helm, kustomize and the network and
// runs only with RENDER_CONSUMPTION=1.
func TestRenderConsumption(t *testing.T) {
	if os.Getenv("RENDER_CONSUMPTION") != "1" {
		t.Skip("set RENDER_CONSUMPTION=1 to render the consuming charts (needs helm, kustomize and the network)")
	}
	charts := &chartStore{dir: t.TempDir(), pins: loadPins(t)}
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) { consume(t, shape, charts) })
	}
}

const (
	consumptionDir = "testdata/consumption"
	kindSecret     = "Secret"
	kindHelmRel    = "HelmRelease"
	kindOCIRepo    = "OCIRepository"
	fieldSpec      = "spec"
	fieldName      = "name"
	fieldKey       = "key"
	fieldKind      = "kind"
	fieldOptional  = "optional"
)

// pin is one entry of charts.yaml: a consuming chart, its OCI repository and the
// version the test renders.
type pin struct {
	Name     string `yaml:"name"`
	Registry string `yaml:"registry"`
	Version  string `yaml:"version"`
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
		pins[p.Name] = p
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
	t       *testing.T
	in      *Input
	dir     string
	charts  *chartStore
	emitted map[string]emittedSecret
	refs    []secretRef
	volumes []volumeRef
}

func consume(t *testing.T, shape string, charts *chartStore) {
	input, secrets := loadInput(t, shape)
	in, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Render(input, secrets)
	if err != nil {
		t.Fatal(err)
	}
	c := &consumption{t: t, in: in, dir: t.TempDir(), charts: charts, emitted: map[string]emittedSecret{}}
	for repo, files := range result.Files {
		for path, f := range files {
			full := filepath.Join(c.dir, string(repo), path)
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, f.Content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	extras, err := filepath.Glob(filepath.Join(c.dir, "giantswarm", "*-management-clusters", "management-clusters", in.Installation.Name, "extras", "*", "kustomization.yaml"))
	if err != nil || len(extras) == 0 {
		t.Fatalf("no extras directories rendered (%v)", err)
	}
	for _, k := range extras {
		c.extras(filepath.Dir(k))
	}
	c.dex()
	c.assertRead()
	c.assertKeys()
	c.assertWholeSecrets()
}

// extras builds one emitted extras directory over the fleet base and renders
// every HelmRelease it yields; the meta chart's children are rendered in turn.
func (c *consumption) extras(dir string) {
	objects := decode(c.t, run(c.t, "kustomize", "build", dir))
	for _, o := range objects {
		if o.kind() == kindSecret {
			s := emittedSecret{ns: o.namespace(""), name: o.name(), keys: map[string]bool{}}
			for _, field := range []string{"stringData", "data"} {
				for k := range mapOf(get(o, field)) {
					s.keys[k] = true
				}
			}
			c.emitted[s.String()] = s
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

func mapOf(v any) map[string]any { m, _ := v.(map[string]any); return m }

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
		p, pinned := c.charts.pins[chart]
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
			if str(get(vf, fieldKind)) != "ConfigMap" {
				continue
			}
			standIn := filepath.Join(consumptionDir, chart+".values.yaml")
			if _, err := os.Stat(standIn); err != nil {
				t.Fatalf("HelmRelease %s takes values from ConfigMap %s (a shared template, not in this repository): add %s with the Secret-selecting values it carries", hr.name(), str(get(vf, fieldName)), standIn)
			}
			rel.values = append(rel.values, standIn)
			patch := c.configsFile(hr.name())
			if patch != "" {
				rel.values = append(rel.values, patch)
			}
		}
		if values != nil {
			rel.values = append(rel.values, c.writeValues(rel.name, values))
		}
		out = append(out, rel)
	}
	return out
}

// checkRange asserts the pin lies in the range the OCIRepository follows. The
// one range the definition writes itself — chart.semver from the installation's
// input, on the meta chart — is the installation's choice of line; a shape that
// asks for a line the pin file does not carry renders values of that line and
// cannot be proven against the pinned one, so it is skipped naming the reason.
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
	if rng == c.in.Chart.Semver {
		c.t.Skipf("the installation asks for %s %s and the test pins %s: a line %s/charts.yaml does not carry is not proven here", p.Name, rng, p.Version, consumptionDir)
	}
	c.t.Fatalf("chart %s: pinned %s is outside the range %s that HelmRelease %s follows", p.Name, p.Version, rng, hr)
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
	matches, _ := filepath.Glob(filepath.Join(c.dir, "giantswarm", "*-configs", "installations", c.in.Installation.Name, "apps", app, "configmap-values.yaml.patch"))
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
	out := run(c.t, "helm", append([]string{"template", rel.name, c.charts.pull(c.t, rel.chart), "-n", rel.ns}, flags(rel.values)...)...)
	rel.text = string(out)
	rel.objects = decode(c.t, out)
	for _, o := range rel.objects {
		c.collect(rel, o)
	}
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

// dex renders dex-app with the stand-in of its shared template and the emitted
// patch, and checks every static client whose secret is a reference: the
// rendered configuration names an environment variable for it and the
// Deployment loads that variable from an emitted dex-client Secret's key.
func (c *consumption) dex() {
	t := c.t
	p, ok := c.charts.pins["dex-app"]
	if !ok {
		t.Fatalf("chart dex-app is not pinned in %s/charts.yaml", consumptionDir)
	}
	standIn := filepath.Join(consumptionDir, "dex-app.values.yaml")
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
}

// assertRead fails for every emitted Secret no rendered consumer references in
// its namespace.
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
		if len(read[name]) == 0 {
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
		own.objects = decode(t, run(t, "helm", append([]string{"template", own.name, c.charts.pull(t, own.chart), "-n", own.ns}, flags(append(slices.Clone(own.values), inline))...)...))
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
