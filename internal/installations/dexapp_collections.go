package installations

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

type dexCollectionPatch struct {
	repository, path string
	patch            collectionPatch
}

type dexCollectionReader struct {
	read   refReader
	active map[string]bool
}

// dexCollectionPatches follows resources containing the shared Dex source,
// then their components, then their own patches, in Kustomize's order.
func dexCollectionPatches(ctx context.Context, read refReader, base remoteBase) ([]dexCollectionPatch, error) {
	reader := dexCollectionReader{read: read, active: map[string]bool{}}
	patches, found, err := reader.collection(ctx, base, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%s:%s does not include %s", base.Repository, base.Path, DexAppBasePath)
	}
	return patches, nil
}

func (reader *dexCollectionReader) collection(ctx context.Context, location remoteBase, component bool) ([]dexCollectionPatch, bool, error) {
	if path.IsAbs(location.Path) || location.Path == ".." || strings.HasPrefix(location.Path, "../") {
		return nil, false, fmt.Errorf("collection path %q is outside the repository", location.Path)
	}
	key := location.Repository + "@" + location.Ref + ":" + location.Path
	if reader.active[key] {
		return nil, false, fmt.Errorf("cyclic collection at %s", key)
	}
	reader.active[key] = true
	defer delete(reader.active, key)
	var data, filename string
	for _, name := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		filename = path.Join(location.Path, name)
		var err error
		data, err = reader.read(ctx, location.Repository, filename, location.Ref)
		if err == nil {
			break
		}
		if !errors.Is(err, gh.ErrNotFound) {
			return nil, false, fmt.Errorf("%s:%s: %w", location.Repository, filename, err)
		}
		data = ""
	}
	if data == "" {
		return nil, false, fmt.Errorf("%s:%s: no collection kustomization on record", location.Repository, location.Path)
	}
	var document collectionsKustomization
	if err := yaml.Unmarshal([]byte(data), &document); err != nil {
		return nil, false, fmt.Errorf("%s:%s: %w", location.Repository, filename, err)
	}
	var patches []dexCollectionPatch
	found := false
	for _, resource := range document.Resources {
		child := collectionLocation(location, resource)
		if child.Repository == location.Repository && child.Path == DexAppBasePath {
			if found || component {
				return nil, false, fmt.Errorf("%s:%s: duplicate or component-owned Dex source", location.Repository, filename)
			}
			found = true
			continue
		}
		if path.Ext(child.Path) != "" {
			continue
		}
		nested, containsDex, err := reader.collection(ctx, child, false)
		if err != nil {
			return nil, false, err
		}
		if containsDex {
			if found || component {
				return nil, false, fmt.Errorf("%s:%s: duplicate or component-owned Dex source", location.Repository, filename)
			}
			found = true
			patches = append(patches, nested...)
		}
	}
	if !found && !component {
		return nil, false, nil
	}
	overlays, err := reader.overlays(ctx, location, filename, document)
	return append(patches, overlays...), found, err
}

func (reader *dexCollectionReader) overlays(ctx context.Context, location remoteBase, filename string, document collectionsKustomization) ([]dexCollectionPatch, error) {
	if len(document.Replacements)+len(document.Transformers)+len(document.LegacyMergePatches)+len(document.LegacyJSONPatches) > 0 {
		return nil, fmt.Errorf("%s:%s: collection uses replacements or external/legacy transformers; cannot establish the exact Dex version", location.Repository, filename)
	}
	var patches []dexCollectionPatch
	for _, resource := range document.Components {
		nested, _, err := reader.collection(ctx, collectionLocation(location, resource), true)
		if err != nil {
			return nil, err
		}
		patches = append(patches, nested...)
	}
	for _, patch := range document.Patches {
		origin := filename
		if patch.Path != "" {
			origin = path.Join(location.Path, patch.Path)
			if path.IsAbs(patch.Path) || origin == ".." || strings.HasPrefix(origin, "../") {
				return nil, fmt.Errorf("patch path %q is outside the repository", patch.Path)
			}
			var err error
			patch.Patch, err = reader.read(ctx, location.Repository, origin, location.Ref)
			if err != nil {
				return nil, fmt.Errorf("%s:%s: %w", location.Repository, origin, err)
			}
		}
		patches = append(patches, dexCollectionPatch{repository: location.Repository, path: origin, patch: patch})
	}
	return patches, nil
}

func collectionLocation(parent remoteBase, resource string) remoteBase {
	if remote, ok := parseRemoteBase(resource); ok {
		return remote
	}
	return remoteBase{Repository: parent.Repository, Ref: parent.Ref, Path: path.Clean(path.Join(parent.Path, strings.TrimSuffix(resource, "/")))}
}
