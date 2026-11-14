package installations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// DexAppBasePath holds the fleet's dex-app source and release manifests.
const DexAppBasePath = "bases/collections/shared/base/dex-app.yaml"

// dexAppName is the App every installation's Dex is installed from; kindApp
// the kind of an App document.
const dexAppName, kindApp, kindOCIRepository = "dex-app", "App", "OCIRepository"

// CollectionsKustomizationPath is where the installation's management-clusters
// repository composes its app collection: the fleet's base as a remote
// resource, at a ref, and the installation's own patches on it — among them a
// pin of the App dex-app's version.
func CollectionsKustomizationPath(name string) string {
	return "management-clusters/" + name + "/collections/kustomization.yaml"
}

// DexAppVersion is the exact dex-app version and the file it was read from.
type DexAppVersion struct {
	Version    string
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

// readDexAppVersion reads the base's dex-app source at the referenced ref,
// with the installation's patches applied to that resource kind.
func readDexAppVersion(ctx context.Context, read refReader, inst Installation, col collections) (DexAppVersion, error) {
	if !col.found {
		return DexAppVersion{}, nil
	}
	kustomizationPath := CollectionsKustomizationPath(inst.Name)
	var base remoteBase
	for _, resource := range col.kustomization.Resources {
		if candidate, ok := parseRemoteBase(resource); ok {
			base = candidate
			break
		}
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
	source, err := dexAppSource(data)
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s at %s: %w", DexAppBasePath, base.Repository, base.refOrDefault(), err)
	}
	v := DexAppVersion{Repository: base.Repository, Path: DexAppBasePath}
	var overlays []dexCollectionPatch
	if source.Kind == kindOCIRepository {
		overlays, err = dexCollectionPatches(ctx, read, base)
		if err != nil {
			return DexAppVersion{}, fmt.Errorf("the dex-app on record: %w", err)
		}
	}
	reader := dexCollectionReader{read: read, active: map[string]bool{}}
	local, err := reader.overlays(ctx, remoteBase{Repository: inst.Repositories.ManagementClusters, Path: path.Dir(kustomizationPath)}, kustomizationPath, col.kustomization)
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %w", err)
	}
	overlays = append(overlays, local...)
	for _, overlay := range overlays {
		changed, err := source.apply(overlay.patch)
		if err != nil {
			return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s:%s: %w", overlay.repository, overlay.path, err)
		}
		if changed {
			v.Repository, v.Path = overlay.repository, overlay.path
		}
	}
	v.Version, err = source.version()
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s: %w", v.Source(), err)
	}
	return v, v.check()
}

// DexAppVersionIn reads the dex-app installation name runs from kustomization,
// the content of its collections kustomization in the management-clusters
// repository: the installation's own pin, or the fleet base's source read
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
	return readDexAppVersion(ctx, read, inst, collections{found: true, kustomization: k})
}

// check refuses a version that is no semantic version: the record says
// nothing a prerequisite can be compared against.
func (v DexAppVersion) check() error {
	if _, err := semver.NewVersion(v.Version); err != nil {
		return fmt.Errorf("the dex-app on record: %s in %s: version %q is no semantic version", v.Path, v.Repository, v.Version)
	}
	return nil
}

// remoteBase is the fleet's base as the kustomization's resources name it: a
// GitHub repository and the ref after ?ref=.
type remoteBase struct {
	Repository string
	Ref        string
	Path       string
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
	Resources          []string          `yaml:"resources"`
	Components         []string          `yaml:"components"`
	Patches            []collectionPatch `yaml:"patches"`
	Replacements       []yaml.Node       `yaml:"replacements"`
	Transformers       []yaml.Node       `yaml:"transformers"`
	LegacyMergePatches []yaml.Node       `yaml:"patchesStrategicMerge"`
	LegacyJSONPatches  []yaml.Node       `yaml:"patchesJson6902"`
}

type collectionPatch struct {
	Patch  string `yaml:"patch"`
	Path   string `yaml:"path"`
	Target struct {
		Kind      string         `yaml:"kind"`
		Name      string         `yaml:"name"`
		Namespace string         `yaml:"namespace"`
		Group     string         `yaml:"group"`
		Version   string         `yaml:"version"`
		Other     map[string]any `yaml:",inline"`
	} `yaml:"target"`
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
	repoPath, directory, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "//")
	parts := strings.Split(strings.TrimSuffix(repoPath, ".git"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return remoteBase{}, false
	}
	return remoteBase{Repository: parts[0] + "/" + parts[1], Ref: u.Query().Get("ref"), Path: path.Clean(directory)}, true
}

func (source *dexApp) apply(patch collectionPatch) (bool, error) {
	if patch.Target.Namespace != "" && !matchesDexName(patch.Target.Namespace, source.Metadata.Namespace) {
		return false, nil
	}
	group, version, _ := strings.Cut(source.APIVersion, "/")
	if patch.Target.Group != "" && patch.Target.Group != group || patch.Target.Version != "" && patch.Target.Version != version {
		return false, nil
	}
	changed, err := source.patch(patch.Patch, patch.Target.Kind, patch.Target.Name)
	if err == nil && changed && len(patch.Target.Other) > 0 {
		return false, fmt.Errorf("cannot establish the effective Dex version through additional patch selectors")
	}
	return changed, err
}

func matchesDexName(selector, name string) bool {
	if selector == "" {
		return true
	}
	pattern, err := regexp.Compile("^(?:" + selector + ")$")
	return err == nil && pattern.MatchString(name)
}

func (source *dexApp) patch(patch, targetKind, targetName string) (bool, error) {
	const remove, nullTag, specKey = "remove", "!!null", "spec"
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(patch), &node); err != nil {
		return false, err
	}
	if node.Kind != yaml.DocumentNode || len(node.Content) == 0 {
		return false, nil
	}
	var changed bool
	switch doc := node.Content[0]; doc.Kind {
	case yaml.SequenceNode:
		if targetKind != "" && targetKind != source.Kind || !matchesDexName(targetName, dexAppName) {
			return false, nil
		}
		var ops []struct {
			Op    string    `yaml:"op"`
			Path  string    `yaml:"path"`
			Value yaml.Node `yaml:"value"`
		}
		if err := doc.Decode(&ops); err != nil {
			return false, err
		}
		for _, op := range ops {
			if op.Op != "replace" && op.Op != "add" && op.Op != remove {
				continue
			}
			var destination any
			switch op.Path {
			case "/spec":
				source.Spec = dexApp{}.Spec
				destination = &source.Spec
			case "/spec/version":
				if source.Kind == kindApp {
					destination = &source.Spec.Version
				}
			case "/spec/ref":
				if source.Kind == kindOCIRepository {
					source.Spec.Ref = dexAppRef{}
					destination = &source.Spec.Ref
				}
			case "/spec/ref/tag", "/spec/ref/semver", "/spec/ref/digest":
				if source.Kind == kindOCIRepository {
					destination = source.Spec.Ref.field(strings.TrimPrefix(op.Path, "/spec/ref/"))
				}
			}
			if destination != nil {
				value := op.Value
				if op.Op == remove || value.Tag == nullTag {
					value = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ""}
				}
				composite := op.Path == "/spec" || op.Path == "/spec/ref"
				if !composite || (op.Op != remove && op.Value.Tag != nullTag) {
					if err := value.Decode(destination); err != nil {
						return false, err
					}
				}
				changed = true
			}
		}
	case yaml.MappingNode:
		var app dexApp
		if err := doc.Decode(&app); err != nil {
			return false, err
		}
		if targetKind != "" || targetName != "" {
			if targetKind != "" && targetKind != source.Kind || !matchesDexName(targetName, dexAppName) {
				return false, nil
			}
		} else if app.Kind != source.Kind || app.Metadata.Name != dexAppName {
			return false, nil
		}
		if spec, found := nodeAt(doc, []string{specKey}); found && spec.Tag == nullTag {
			source.Spec = dexApp{}.Spec
			return true, nil
		}
		var values struct {
			Spec struct {
				Ref map[string]yaml.Node `yaml:"ref"`
			} `yaml:"spec"`
		}
		if err := doc.Decode(&values); err != nil {
			return false, err
		}
		spec := values.Spec
		if source.Kind == kindApp {
			if version, found := nodeAt(doc, []string{specKey, "version"}); found {
				source.Spec.Version, changed = "", true
				if err := version.Decode(&source.Spec.Version); err != nil {
					return false, err
				}
			}
		}
		if source.Kind == kindOCIRepository {
			if reference, found := nodeAt(doc, []string{specKey, "ref"}); found && reference.Tag == nullTag {
				source.Spec.Ref = dexAppRef{}
				return true, nil
			}
			for key, value := range spec.Ref {
				if destination := source.Spec.Ref.field(key); destination != nil {
					*destination = ""
					if err := value.Decode(destination); err != nil {
						return false, err
					}
					changed = true
				}
			}
		}
	}
	return changed, nil
}

// dexApp holds the source fields needed for an exact version comparison.
type dexApp struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Spec struct {
		Version string    `yaml:"version"`
		Ref     dexAppRef `yaml:"ref"`
	} `yaml:"spec"`
}

type dexAppRef struct {
	Tag    string `yaml:"tag"`
	Semver string `yaml:"semver"`
	Digest string `yaml:"digest"`
}

func (reference *dexAppRef) field(key string) *string {
	switch key {
	case "tag":
		return &reference.Tag
	case "semver":
		return &reference.Semver
	case "digest":
		return &reference.Digest
	default:
		return nil
	}
}

func (source dexApp) version() (string, error) {
	if source.Kind == kindApp {
		if source.Spec.Version == "" {
			return "", fmt.Errorf("the App %s sets no spec.version", dexAppName)
		}
		return source.Spec.Version, nil
	}
	if source.Spec.Ref.Digest != "" || source.Spec.Ref.Semver != "" {
		return "", fmt.Errorf("the OCIRepository %s selects a digest or semver constraint, not an exact version; pin spec.ref.tag and clear spec.ref.digest and spec.ref.semver before comparing Dex features", dexAppName)
	}
	if source.Spec.Ref.Tag == "" {
		return "", fmt.Errorf("the OCIRepository %s sets no spec.ref.tag", dexAppName)
	}
	return source.Spec.Ref.Tag, nil
}

func dexAppSource(manifest string) (dexApp, error) {
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var source dexApp
		if err := decoder.Decode(&source); errors.Is(err, io.EOF) {
			return dexApp{}, fmt.Errorf("no App dex-app or OCIRepository dex-app in the documents")
		} else if err != nil {
			return dexApp{}, fmt.Errorf("decode: %w", err)
		}
		if source.Metadata.Name == dexAppName && (source.Kind == kindApp || source.Kind == kindOCIRepository) {
			return source, nil
		}
	}
}
