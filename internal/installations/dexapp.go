package installations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// DexAppBasePath is where the fleet's shared collection base keeps dex-app:
// an App, or a Flux OCIRepository and HelmRelease, at the version every
// installation runs unless it pins its own.
const DexAppBasePath = "bases/collections/shared/base/dex-app.yaml"

// dexAppName is what every installation's Dex is installed from; kindApp and
// kindOCIRepository the kinds that carry its version.
const dexAppName, kindApp, kindOCIRepository = "dex-app", "App", "OCIRepository"

// dexAppChart is the dex-app chart a range resolves against when the record
// names no OCIRepository URL (an installation's range pin over an App base).
const dexAppChart = "oci://gsoci.azurecr.io/charts/giantswarm/dex-app"

// CollectionsKustomizationPath is where the installation's management-clusters
// repository composes its app collection: the fleet's base as a remote
// resource, at a ref, and the installation's own patches on it — among them a
// pin of the App dex-app's version.
func CollectionsKustomizationPath(name string) string {
	return "management-clusters/" + name + "/collections/kustomization.yaml"
}

// DexAppVersion is the dex-app on record: the App's spec.version, or the
// OCIRepository's tag or semver, a range resolved to the tag Flux picks
// (Range keeps the range), and the file it was read from.
type DexAppVersion struct {
	Version    string
	Range      string
	Repository string
	Path       string
}

// Source names where the version was read, repository:path.
func (v DexAppVersion) Source() string { return v.Repository + ":" + v.Path }

// refReader reads path of repository at ref as the person; the default branch
// when ref is empty.
type refReader func(ctx context.Context, repository, path, ref string) (string, error)

// readAt reads through the person's GitHub client.
func readAt(c *gh.Client) refReader {
	return func(ctx context.Context, repository, path, ref string) (string, error) {
		owner, repo, err := gh.SplitRepo(repository)
		if err != nil {
			return "", err
		}
		return gh.ReadFileAt(ctx, c, owner, repo, path, ref)
	}
}

// readDexAppVersion reads the dex-app inst runs, from the record as the
// person: the installation's own pin — a patch on the App dex-app in its
// collections kustomization (col) — wins; without one, the App of the
// fleet's base (DexAppBasePath in the repository the kustomization's remote
// resource names, at the ref it names). An installation without the
// kustomization, or a base without the App, has no version on record: the
// empty answer, no error. A base that cannot be read or parsed, a patch that
// does not decode, a kustomization that names no base and a version that is
// no semantic version are errors of the report.
func readDexAppVersion(ctx context.Context, read refReader, tags TagLister, inst Installation, col collections) (DexAppVersion, error) {
	if !col.found {
		return DexAppVersion{}, nil
	}
	kustomizationPath := CollectionsKustomizationPath(inst.Name)
	pin, base, err := dexAppPin(col.kustomization)
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s: %w", kustomizationPath, inst.Repositories.ManagementClusters, err)
	}
	if pin != "" {
		return resolveDexApp(ctx, tags, dexAppRef{version: pin, url: dexAppChart}, inst.Repositories.ManagementClusters, kustomizationPath)
	}
	if base.Repository == "" {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s names no remote base to read the App from", kustomizationPath, inst.Repositories.ManagementClusters)
	}
	data, err := read(ctx, base.Repository, DexAppBasePath, base.Ref)
	if errors.Is(err, gh.ErrNotFound) {
		return DexAppVersion{}, nil
	}
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %w", err)
	}
	ref, err := dexAppVersion(data)
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s at %s: %w", DexAppBasePath, base.Repository, base.refOrDefault(), err)
	}
	return resolveDexApp(ctx, tags, ref, base.Repository, DexAppBasePath)
}

// resolveDexApp turns the version on record into the one the installation
// runs: an exact version is itself; a range is the highest tag of ref.url
// that satisfies it, prereleases only where the range names one — the tag
// Flux's OCIRepository picks.
func resolveDexApp(ctx context.Context, tags TagLister, ref dexAppRef, repository, path string) (DexAppVersion, error) {
	v := DexAppVersion{Version: ref.version, Repository: repository, Path: path}
	if _, err := semver.StrictNewVersion(strings.TrimPrefix(ref.version, "v")); err == nil || ref.exact {
		return v, v.check()
	}
	constraint, err := semver.NewConstraint(ref.version)
	if err != nil {
		return v, v.check()
	}
	chart := ref.url
	if chart == "" {
		chart = dexAppChart
	}
	list, err := tags(ctx, chart)
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s: the tags of %s for %q: %w", path, repository, chart, ref.version, err)
	}
	var best *semver.Version
	for _, tag := range list {
		candidate, err := semver.NewVersion(tag)
		if err != nil || !constraint.Check(candidate) {
			continue
		}
		if best == nil || candidate.GreaterThan(best) {
			best = candidate
		}
	}
	if best == nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s: no tag of %s satisfies %q", path, repository, chart, ref.version)
	}
	return DexAppVersion{Version: best.Original(), Range: ref.version, Repository: repository, Path: path}, nil
}

// DexAppVersionIn reads the dex-app installation name runs from kustomization,
// the content of its collections kustomization in the management-clusters
// repository: the installation's own pin, or the fleet base's App read
// through read where it pins none. A checkout's copy reads the same as the
// record (platformctl installation dex-split reads one).
func DexAppVersionIn(ctx context.Context, read func(ctx context.Context, repository, path, ref string) (string, error),
	name, repository, kustomization string,
) (DexAppVersion, error) {
	var k collectionsKustomization
	if err := yaml.Unmarshal([]byte(kustomization), &k); err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s: decode: %w", CollectionsKustomizationPath(name), repository, err)
	}
	inst := Installation{Name: name, Repositories: Repositories{ManagementClusters: repository}}
	return readDexAppVersion(ctx, read, RegistryTags(registryClient), inst, collections{found: true, kustomization: k})
}

// check refuses a version that is no semantic version: the record says
// nothing a prerequisite can be compared against.
func (v DexAppVersion) check() error {
	if _, err := semver.NewVersion(v.Version); err != nil {
		return fmt.Errorf("the dex-app on record: %s in %s: spec.version %q is no semantic version", v.Path, v.Repository, v.Version)
	}
	return nil
}

// remoteBase is the fleet's base as the kustomization's resources name it: a
// GitHub repository and the ref after ?ref=.
type remoteBase struct {
	Repository string
	Ref        string
}

func (b remoteBase) refOrDefault() string {
	if b.Ref == "" {
		return "the default branch"
	}
	return b.Ref
}

// collectionsKustomization is the part of the installation's collections
// kustomization the record reads: the remote resources and the patches.
type collectionsKustomization struct {
	Resources []string `yaml:"resources"`
	Patches   []struct {
		Patch  string `yaml:"patch"`
		Target struct {
			Kind string `yaml:"kind"`
			Name string `yaml:"name"`
		} `yaml:"target"`
	} `yaml:"patches"`
}

// dexAppPin reads the installation's own pin of the App dex-app from its
// collections kustomization k — a JSON 6902 patch on the App that replaces or
// adds /spec/version, or a strategic merge patch of the App with
// spec.version — and the remote base its resources name: the first resource
// that is a GitHub repository URL, with its ref. No pin is the empty string.
func dexAppPin(k collectionsKustomization) (pin string, base remoteBase, err error) {
	for _, r := range k.Resources {
		if b, ok := parseRemoteBase(r); ok {
			base = b
			break
		}
	}
	for _, p := range k.Patches {
		if p.Patch == "" {
			continue
		}
		v, err := pinnedVersion(p.Patch, p.Target.Name == dexAppName && (p.Target.Kind == kindApp || p.Target.Kind == kindOCIRepository))
		if err != nil {
			return "", remoteBase{}, fmt.Errorf("decode the patch on %s: %w", dexAppName, err)
		}
		if v != "" {
			pin = v
		}
	}
	return pin, base, nil
}

// parseRemoteBase reads a kustomize remote resource — a GitHub URL, with or
// without the scheme, the path inside the repository after //, the ref after
// ?ref= — into the repository and the ref.
func parseRemoteBase(resource string) (remoteBase, bool) {
	s := strings.TrimSpace(resource)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host != "github.com" {
		return remoteBase{}, false
	}
	repoPath, _, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "//")
	parts := strings.Split(strings.TrimSuffix(repoPath, ".git"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return remoteBase{}, false
	}
	return remoteBase{Repository: parts[0] + "/" + parts[1], Ref: u.Query().Get("ref")}, true
}

// pinnedVersion reads the version a patch sets on dex-app: a JSON 6902 list
// whose op replaces or adds the App's /spec/version or the OCIRepository's
// /spec/ref/tag or /spec/ref/semver, where the patch targets dex-app
// (targeted); or a strategic merge patch that names the App or the
// OCIRepository itself and carries its version. A patch on anything else pins
// nothing.
func pinnedVersion(patch string, targeted bool) (string, error) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(patch), &node); err != nil {
		return "", err
	}
	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return "", nil
	}
	switch doc := node.Content[0]; doc.Kind {
	case yaml.SequenceNode:
		if !targeted {
			return "", nil
		}
		var ops []struct {
			Op    string `yaml:"op"`
			Path  string `yaml:"path"`
			Value string `yaml:"value"`
		}
		if err := doc.Decode(&ops); err != nil {
			return "", err
		}
		var version string
		for _, op := range ops {
			if (op.Op == "replace" || op.Op == "add") && (op.Path == "/spec/version" || op.Path == "/spec/ref/tag" || op.Path == "/spec/ref/semver") {
				version = op.Value
			}
		}
		return version, nil
	case yaml.MappingNode:
		var d dexAppDocument
		if err := doc.Decode(&d); err != nil {
			return "", err
		}
		if ref, ok := d.ref(); ok {
			return ref.version, nil
		}
	}
	return "", nil
}

// dexAppDocument is an App or an OCIRepository document: its name and the
// fields that carry dex-app's version.
type dexAppDocument struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Version string `yaml:"version"`
		URL     string `yaml:"url"`
		Ref     struct {
			Tag    string `yaml:"tag"`
			Semver string `yaml:"semver"`
		} `yaml:"ref"`
	} `yaml:"spec"`
}

// dexAppRef is the version a document gives dex-app: exact (an App's
// spec.version, an OCIRepository's tag) or a semver that may be a range, and
// the chart's OCI URL a range resolves against.
type dexAppRef struct {
	version string
	exact   bool
	url     string
}

// ref reads dex-app's version from d: the App's spec.version, or the
// OCIRepository's tag, else its semver. ok is false for any other document.
func (d dexAppDocument) ref() (dexAppRef, bool) {
	if d.Metadata.Name != dexAppName {
		return dexAppRef{}, false
	}
	switch d.Kind {
	case kindApp:
		return dexAppRef{version: d.Spec.Version, exact: true}, true
	case kindOCIRepository:
		if d.Spec.Ref.Tag != "" {
			return dexAppRef{version: d.Spec.Ref.Tag, exact: true, url: d.Spec.URL}, true
		}
		return dexAppRef{version: d.Spec.Ref.Semver, url: d.Spec.URL}, true
	}
	return dexAppRef{}, false
}

// dexAppVersion reads dex-app's version from the base's documents: the App,
// or the OCIRepository next to the HelmRelease.
func dexAppVersion(manifest string) (dexAppRef, error) {
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var d dexAppDocument
		err := dec.Decode(&d)
		if errors.Is(err, io.EOF) {
			return dexAppRef{}, fmt.Errorf("no App or OCIRepository %s in the document", dexAppName)
		}
		if err != nil {
			return dexAppRef{}, fmt.Errorf("decode: %w", err)
		}
		ref, ok := d.ref()
		if !ok {
			continue
		}
		if ref.version == "" {
			return dexAppRef{}, fmt.Errorf("the %s %s sets no version", d.Kind, dexAppName)
		}
		return ref, nil
	}
}
