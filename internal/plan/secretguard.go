package plan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// ReferenceRefusal holds a hub commit whose Dex or kustomization references
// have no manifest in the plan or the Git record.
func (p Installation) ReferenceRefusal() string {
	return strings.Join(p.UnresolvedReferences, "; ")
}

type referenceGuard struct {
	ctx     context.Context
	read    Reader
	planned map[string]string
	secrets map[string]bool
	visited map[string]bool
}

func (p *Installation) guardReferences(ctx context.Context, installation installations.Installation, result *render.Result, read Reader) {
	guard := referenceGuard{ctx: ctx, read: read,
		planned: map[string]string{}, secrets: map[string]bool{}, visited: map[string]bool{}}
	for _, file := range p.Files {
		if file.Change != ChangeUnknown {
			guard.planned[file.Repository+":"+file.Path] = file.Content
		}
	}
	for repository, files := range result.Files {
		resolved := ResolveRepository(string(repository), installation, installation)
		for name, file := range files {
			key := resolved + ":" + name
			if _, exists := guard.planned[key]; exists && guard.planned[key] == "" {
				guard.planned[key] = string(file.Content)
			}
			if resolved == installation.Repositories.ManagementClusters && strings.HasPrefix(name, "management-clusters/"+installation.Name+"/") {
				guard.indexSecrets(file.Content, "")
			}
			if path.Base(name) == kustomizationFile {
				content := file.Content
				if merged := guard.planned[key]; merged != "" {
					content = []byte(merged)
				}
				p.UnresolvedReferences = append(p.UnresolvedReferences, guard.resources(resolved, name, content)...)
			}
		}
	}
	for _, include := range result.Includes {
		repository := ResolveRepository(string(include.Repository), installation, installation)
		key := repository + ":" + include.Path
		if content, planned := guard.planned[key]; planned && content == "" {
			if current, err := read(ctx, repository, include.Path); err == nil {
				guard.planned[key] = current
			}
		}
		if current, planned := guard.planned[key]; planned && current != "" {
			list := ListResources
			if include.Component {
				list = ListComponents
			}
			if edited, _, err := ListEntry([]byte(current), list, include.Resource); err == nil {
				guard.planned[key] = string(edited)
			}
		}
		if err := guard.resource(repository, include.Path, include.Resource); err != nil {
			p.UnresolvedReferences = append(p.UnresolvedReferences, err.Error())
		}
	}
	var missing []string
	for _, file := range p.Files {
		if !strings.HasSuffix(file.Path, dexPatchFile) {
			continue
		}
		for _, reference := range file.References {
			if name, secret := strings.CutPrefix(reference, kindSecret+"/"); secret && !guard.secrets[name] {
				missing = append(missing, name)
			}
		}
	}
	if len(missing) > 0 {
		guard.indexRecord(installation.Repositories.ManagementClusters, installations.PortalClientSecretPath(installation.Name), "")
		missing = slices.DeleteFunc(missing, func(name string) bool { return guard.secrets[name] })
	}
	if len(missing) > 0 {
		root := "management-clusters/" + installation.Name + "/extras/kustomization.yaml"
		guard.indexRecord(installation.Repositories.ManagementClusters, root, "")
		for key := range guard.planned {
			repository, file, _ := strings.Cut(key, ":")
			if repository == installation.Repositories.ManagementClusters && strings.HasPrefix(file, "management-clusters/"+installation.Name+"/") && path.Base(file) == kustomizationFile {
				guard.indexRecord(repository, file, "")
			}
		}
		for _, name := range missing {
			if !guard.secrets[name] {
				p.UnresolvedReferences = append(p.UnresolvedReferences, fmt.Sprintf("Dex references Secret/%s in namespace giantswarm, but no matching Secret manifest is in this plan or the installation's recorded extras; add and include its Secret file first", name))
			}
		}
	}
	slices.Sort(p.UnresolvedReferences)
	p.UnresolvedReferences = slices.Compact(p.UnresolvedReferences)
}

func (guard *referenceGuard) indexSecrets(content []byte, namespace string) {
	for _, object := range manifests(content) {
		if namespace != "" {
			object.Namespace = namespace
		}
		if object.Kind == kindSecret && object.Namespace == "giantswarm" {
			guard.secrets[object.Name] = true
		}
	}
}

func (guard *referenceGuard) resource(repository, source, resource string) error {
	if remoteResource(resource) {
		return nil
	}
	for _, candidate := range resourcePaths(source, resource) {
		if _, planned := guard.planned[repository+":"+candidate]; planned {
			return nil
		}
		if _, err := guard.read(guard.ctx, repository, candidate); err == nil {
			return nil
		} else if !errors.Is(err, gh.ErrNotFound) {
			return fmt.Errorf("%s:%s references %s, which could not be read: %w", repository, source, resource, err)
		}
	}
	return fmt.Errorf("%s:%s references %s, whose file is neither in this plan nor on record; add the resource first", repository, source, resource)
}

func (guard *referenceGuard) resources(repository, source string, content []byte) []string {
	var document struct {
		Resources  []string `yaml:"resources"`
		Components []string `yaml:"components"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return []string{fmt.Sprintf("%s:%s: cannot check resource references: %v", repository, source, err)}
	}
	var unresolved []string
	for _, resource := range append(document.Resources, document.Components...) {
		if err := guard.resource(repository, source, resource); err != nil {
			unresolved = append(unresolved, err.Error())
		}
	}
	return unresolved
}

func remoteResource(resource string) bool {
	return strings.Contains(resource, "://") || strings.HasPrefix(resource, "github.com/") || strings.HasPrefix(resource, "git@")
}

func resourcePaths(source, resource string) []string {
	if path.IsAbs(resource) {
		return nil
	}
	resolved := path.Clean(path.Join(path.Dir(source), resource))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return nil
	}
	if path.Ext(resolved) != "" {
		return []string{resolved}
	}
	return []string{path.Join(resolved, "kustomization.yaml"), path.Join(resolved, "kustomization.yml"), path.Join(resolved, "Kustomization")}
}

func (guard *referenceGuard) indexRecord(repository, file, namespace string) {
	key := repository + ":" + file + ":" + namespace
	if guard.visited[key] {
		return
	}
	guard.visited[key] = true
	content, planned := guard.planned[repository+":"+file]
	if !planned || content == "" {
		var err error
		content, err = guard.read(guard.ctx, repository, file)
		if err != nil {
			return
		}
	}
	decoder := yaml.NewDecoder(bytes.NewBufferString(content))
	for {
		var document struct {
			Kind       string                           `yaml:"kind"`
			Metadata   struct{ Name, Namespace string } `yaml:"metadata"`
			Namespace  string                           `yaml:"namespace"`
			Resources  []string                         `yaml:"resources"`
			Components []string                         `yaml:"components"`
		}
		if err := decoder.Decode(&document); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			return
		}
		if namespace == "" && document.Namespace != "" {
			namespace = document.Namespace
		}
		secretNamespace := document.Metadata.Namespace
		if namespace != "" {
			secretNamespace = namespace
		}
		if document.Kind == kindSecret && secretNamespace == "giantswarm" {
			guard.secrets[document.Metadata.Name] = true
		}
		for _, resource := range append(document.Resources, document.Components...) {
			if remoteResource(resource) {
				continue
			}
			for _, candidate := range resourcePaths(file, resource) {
				guard.indexRecord(repository, candidate, namespace)
			}
		}
	}
}
