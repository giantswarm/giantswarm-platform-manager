package installations

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/google/go-github/v92/github"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// DexAppBasePath is where the fleet's shared collection base keeps the App
// dex-app: the version every installation runs unless it pins its own.
const DexAppBasePath = "bases/collections/shared/base/dex-app.yaml"

// dexAppName is the App every installation's Dex is installed from; kindApp
// the kind of an App document.
const dexAppName, kindApp = "dex-app", "App"

// CollectionsKustomizationPath is where the installation's management-clusters
// repository composes its app collection: the fleet's base as a remote
// resource, at a ref, and the installation's own patches on it — among them a
// pin of the App dex-app's version.
func CollectionsKustomizationPath(name string) string {
	return "management-clusters/" + name + "/collections/kustomization.yaml"
}

// DexAppVersion is the dex-app on record: spec.version of the App and the
// file it was read from.
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
func readAt(c *github.Client) refReader {
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
// collections kustomization — wins; without one, the App of the fleet's base
// (DexAppBasePath in the repository the kustomization's remote resource
// names, at the ref it names). An installation without the kustomization, or
// a base without the App, has no version on record: the empty answer, no
// error. A file that cannot be read or parsed, a kustomization that names no
// base and a version that is no semantic version are errors of the report.
func readDexAppVersion(ctx context.Context, read refReader, inst Installation) (DexAppVersion, error) {
	kustomizationPath := CollectionsKustomizationPath(inst.Name)
	data, err := read(ctx, inst.Repositories.ManagementClusters, kustomizationPath, "")
	if errors.Is(err, gh.ErrNotFound) {
		return DexAppVersion{}, nil
	}
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %w", err)
	}
	pin, base, err := dexAppPin(data)
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s: %w", kustomizationPath, inst.Repositories.ManagementClusters, err)
	}
	if pin != "" {
		v := DexAppVersion{Version: pin, Repository: inst.Repositories.ManagementClusters, Path: kustomizationPath}
		return v, v.check()
	}
	if base.Repository == "" {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s names no remote base to read the App from", kustomizationPath, inst.Repositories.ManagementClusters)
	}
	data, err = read(ctx, base.Repository, DexAppBasePath, base.Ref)
	if errors.Is(err, gh.ErrNotFound) {
		return DexAppVersion{}, nil
	}
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %w", err)
	}
	version, err := dexAppVersion(data)
	if err != nil {
		return DexAppVersion{}, fmt.Errorf("the dex-app on record: %s in %s at %s: %w", DexAppBasePath, base.Repository, base.refOrDefault(), err)
	}
	v := DexAppVersion{Version: version, Repository: base.Repository, Path: DexAppBasePath}
	return v, v.check()
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
// collections kustomization — a JSON 6902 patch on the App that replaces or
// adds /spec/version, or a strategic merge patch of the App with
// spec.version — and the remote base its resources name: the first resource
// that is a GitHub repository URL, with its ref. No pin is the empty string.
func dexAppPin(kustomization string) (pin string, base remoteBase, err error) {
	var k collectionsKustomization
	if err := yaml.Unmarshal([]byte(kustomization), &k); err != nil {
		return "", remoteBase{}, fmt.Errorf("decode: %w", err)
	}
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
		v, err := pinnedVersion(p.Patch, p.Target.Kind == kindApp && p.Target.Name == dexAppName)
		if err != nil {
			return "", remoteBase{}, fmt.Errorf("decode the patch on the App %s: %w", dexAppName, err)
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

// pinnedVersion reads the version a patch sets on the App dex-app: a JSON
// 6902 list whose op replaces or adds /spec/version, where the patch targets
// the App (targeted); or a strategic merge patch that names the App itself
// and carries spec.version. A patch on anything else pins nothing.
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
			if (op.Op == "replace" || op.Op == "add") && op.Path == "/spec/version" {
				version = op.Value
			}
		}
		return version, nil
	case yaml.MappingNode:
		var app dexApp
		if err := doc.Decode(&app); err != nil {
			return "", err
		}
		if app.Kind == kindApp && app.Metadata.Name == dexAppName {
			return app.Spec.Version, nil
		}
	}
	return "", nil
}

// dexApp is the App document of the base: its name and version.
type dexApp struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Version string `yaml:"version"`
	} `yaml:"spec"`
}

// dexAppVersion reads spec.version of the base's App dex-app.
func dexAppVersion(manifest string) (string, error) {
	var app dexApp
	if err := yaml.Unmarshal([]byte(manifest), &app); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if app.Kind != kindApp || app.Metadata.Name != dexAppName {
		return "", fmt.Errorf("no App %s in the document", dexAppName)
	}
	if app.Spec.Version == "" {
		return "", fmt.Errorf("the App %s sets no spec.version", dexAppName)
	}
	return app.Spec.Version, nil
}
