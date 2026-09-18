// Package installations is the installations registry and what the manager
// reads about each installation at call time, as the person: the registry
// entries, the opt-in declaration, the facts on record in the installation's
// config.yaml.patch and the enabled markers of the capabilities. Nothing here
// is cached: every answer is what the repositories say now.
package installations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/go-github/v92/github"
	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// Location is a file in a repository, read on its default branch.
type Location struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
}

func (l Location) String() string { return l.Repository + ":" + l.Path }

// Sources are the two registry sources: the installations catalog (a
// Backstage catalog file: one Resource of type installation per installation,
// with its customer, provider, pipeline, region, base domain, account engineer
// and the links to its two GitOps repositories) and the hub's Dev Portal
// app-config, whose gs.installations block carries the base domain, providers
// and auth provider. The portal config's location derives from the hub's
// management-clusters repository as the catalog names it.
type Sources struct {
	Catalog Location
	// Hub is the installation the manager runs on — its management-clusters
	// repository holds the portal's app-config, and its repositories are
	// where the hub side of every capability lands.
	Hub string
}

// PortalConfigPath is where the hub's management-clusters repository keeps the
// Dev Portal's app-config.
func PortalConfigPath(hub string) string {
	return "management-clusters/" + hub + "/extras/backstage/backstage/app-config.yaml"
}

// Installation is one registry entry: the facts the two sources hold about it.
type Installation struct {
	Name     string `json:"name"`
	Customer string `json:"customer,omitempty"`
	Provider string `json:"provider,omitempty"`
	Pipeline string `json:"pipeline,omitempty"`
	Region   string `json:"region,omitempty"`
	// BaseDomain is the installation's own base domain: <name>.<base> from
	// the catalog, or the portal's baseDomain when the catalog has none.
	BaseDomain      string `json:"baseDomain,omitempty"`
	AccountEngineer string `json:"accountEngineer,omitempty"`
	// AuthProvider is how the portal signs a person in to the installation
	// (the portal source only).
	AuthProvider string `json:"authProvider,omitempty"`
	// Hub marks the installation the manager runs on.
	Hub bool `json:"hub"`
	// Repositories are the installation's two GitOps repositories, from the
	// catalog's links (types CCR and CMC). Empty when the catalog does not
	// name them: the installation is then listed, but nothing of it is read.
	Repositories Repositories `json:"repositories"`
	// Sources names the registry sources the entry came from.
	Sources []string `json:"sources"`
}

// Repositories are the installation's GitOps repositories, owner/repo.
type Repositories struct {
	// Configs is the <customer>-configs repository: installations/<name>/.
	Configs string `json:"configs,omitempty"`
	// ManagementClusters is the <customer>-management-clusters repository:
	// management-clusters/<name>/, where the opt-in declaration lives.
	ManagementClusters string `json:"managementClusters,omitempty"`
}

// Known says whether both repositories are on record.
func (r Repositories) Known() bool { return r.Configs != "" && r.ManagementClusters != "" }

// Registry source names.
const (
	SourceCatalog = "catalog"
	SourcePortal  = "portal"
)

// Registry is the merged registry as read now.
type Registry struct {
	Hub           string
	Catalog       Location
	Portal        Location
	Installations []Installation
}

// Load reads both sources as the person and merges them by installation
// name. The catalog is required: without it there is no registry. The portal
// config is read from the hub's management-clusters repository; a hub the
// catalog does not know is an error, as is a portal config that cannot be
// read — the registry is complete or it is not.
func Load(ctx context.Context, c *github.Client, s Sources) (*Registry, error) {
	if s.Hub == "" {
		return nil, errors.New("registry: the hub installation is not configured (--hub / HUB_INSTALLATION)")
	}
	owner, repo, err := gh.SplitRepo(s.Catalog.Repository)
	if err != nil {
		return nil, fmt.Errorf("registry: catalog: %w", err)
	}
	catalogYAML, err := gh.ReadFile(ctx, c, owner, repo, s.Catalog.Path)
	if err != nil {
		return nil, fmt.Errorf("registry: read the catalog %s as the caller: %w", s.Catalog, err)
	}
	entries, err := parseCatalog(catalogYAML)
	if err != nil {
		return nil, fmt.Errorf("registry: catalog %s: %w", s.Catalog, err)
	}
	byName := make(map[string]*Installation, len(entries))
	for i := range entries {
		byName[entries[i].Name] = &entries[i]
	}
	hub, ok := byName[s.Hub]
	if !ok {
		return nil, fmt.Errorf("registry: the hub %q is not in the catalog %s", s.Hub, s.Catalog)
	}
	if hub.Repositories.ManagementClusters == "" {
		return nil, fmt.Errorf("registry: the catalog names no management-clusters repository for the hub %q", s.Hub)
	}
	hub.Hub = true
	portal := Location{Repository: hub.Repositories.ManagementClusters, Path: PortalConfigPath(s.Hub)}
	owner, repo, err = gh.SplitRepo(portal.Repository)
	if err != nil {
		return nil, fmt.Errorf("registry: portal config: %w", err)
	}
	portalYAML, err := gh.ReadFile(ctx, c, owner, repo, portal.Path)
	if err != nil {
		return nil, fmt.Errorf("registry: read the portal config %s as the caller: %w", portal, err)
	}
	portalEntries, err := parsePortal(portalYAML)
	if err != nil {
		return nil, fmt.Errorf("registry: portal config %s: %w", portal, err)
	}
	for name, p := range portalEntries {
		inst, ok := byName[name]
		if !ok {
			entries = append(entries, Installation{Name: name})
			inst = &entries[len(entries)-1]
			byName[name] = inst
		}
		inst.Sources = append(inst.Sources, SourcePortal)
		inst.AuthProvider = p.AuthProvider
		if inst.BaseDomain == "" {
			inst.BaseDomain = p.BaseDomain
		}
		if inst.Provider == "" && len(p.Providers) == 1 {
			inst.Provider = p.Providers[0]
		}
		if inst.Pipeline == "" {
			inst.Pipeline = p.Pipeline
		}
		if inst.Region == "" {
			inst.Region = p.Region
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return &Registry{Hub: s.Hub, Catalog: s.Catalog, Portal: portal, Installations: entries}, nil
}

// The catalog's shape: Backstage entities, one document each.
type catalogEntity struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Labels      map[string]string `yaml:"labels"`
		Annotations map[string]string `yaml:"annotations"`
		Links       []struct {
			URL  string `yaml:"url"`
			Type string `yaml:"type"`
		} `yaml:"links"`
	} `yaml:"metadata"`
	Spec struct {
		Type  string `yaml:"type"`
		Owner string `yaml:"owner"`
	} `yaml:"spec"`
}

const (
	labelCustomer   = "giantswarm.io/customer"
	labelPipeline   = "giantswarm.io/pipeline"
	labelProvider   = "giantswarm.io/provider"
	labelRegion     = "giantswarm.io/region"
	annotationBase  = "giantswarm.io/base"
	annotationAE    = "giantswarm.io/account-engineer"
	linkTypeConfigs = "CCR"
	linkTypeMCs     = "CMC"
)

// parseCatalog reads the installation Resources out of the catalog.
func parseCatalog(data string) ([]Installation, error) {
	dec := yaml.NewDecoder(strings.NewReader(data))
	var out []Installation
	for {
		var e catalogEntity
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode: %w", err)
		}
		if e.Kind != "Resource" || e.Spec.Type != "installation" || e.Metadata.Name == "" {
			continue
		}
		inst := Installation{
			Name:            e.Metadata.Name,
			Customer:        e.Metadata.Labels[labelCustomer],
			Provider:        e.Metadata.Labels[labelProvider],
			Pipeline:        e.Metadata.Labels[labelPipeline],
			Region:          e.Metadata.Labels[labelRegion],
			AccountEngineer: e.Metadata.Annotations[annotationAE],
			Sources:         []string{SourceCatalog},
		}
		if inst.Customer == "" {
			inst.Customer = e.Spec.Owner
		}
		if base := e.Metadata.Annotations[annotationBase]; base != "" {
			inst.BaseDomain = inst.Name + "." + base
		}
		for _, l := range e.Metadata.Links {
			switch l.Type {
			case linkTypeConfigs:
				inst.Repositories.Configs = repoFromURL(l.URL)
			case linkTypeMCs:
				inst.Repositories.ManagementClusters = repoFromURL(l.URL)
			}
		}
		out = append(out, inst)
	}
	if len(out) == 0 {
		return nil, errors.New("no Resource of type installation")
	}
	return out, nil
}

// repoFromURL is owner/repo of a github.com repository URL, or "".
func repoFromURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	for _, prefix := range []string{"https://github.com/", "http://github.com/"} {
		if rest, ok := strings.CutPrefix(u, prefix); ok {
			if _, _, err := gh.SplitRepo(rest); err == nil {
				return rest
			}
		}
	}
	return ""
}

// portalEntry is one installation of the portal's gs.installations block.
type portalEntry struct {
	AuthProvider string   `yaml:"authProvider"`
	BaseDomain   string   `yaml:"baseDomain"`
	Pipeline     string   `yaml:"pipeline"`
	Providers    []string `yaml:"providers"`
	Region       string   `yaml:"region"`
}

// parsePortal reads gs.installations out of the hub's app-config: a ConfigMap
// whose data.values is the chart's values as YAML text, whose
// backstage.appConfig is the app-config as YAML text.
func parsePortal(data string) (map[string]portalEntry, error) {
	var cm struct {
		Data struct {
			Values string `yaml:"values"`
		} `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(data), &cm); err != nil {
		return nil, fmt.Errorf("decode the ConfigMap: %w", err)
	}
	if cm.Data.Values == "" {
		return nil, errors.New("the ConfigMap has no data.values")
	}
	var values struct {
		Backstage struct {
			AppConfig string `yaml:"appConfig"`
		} `yaml:"backstage"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data.Values), &values); err != nil {
		return nil, fmt.Errorf("decode data.values: %w", err)
	}
	if values.Backstage.AppConfig == "" {
		return nil, errors.New("data.values has no backstage.appConfig")
	}
	var appConfig struct {
		GS struct {
			Installations map[string]portalEntry `yaml:"installations"`
		} `yaml:"gs"`
	}
	if err := yaml.Unmarshal([]byte(values.Backstage.AppConfig), &appConfig); err != nil {
		return nil, fmt.Errorf("decode backstage.appConfig: %w", err)
	}
	if len(appConfig.GS.Installations) == 0 {
		return nil, errors.New("backstage.appConfig has no gs.installations")
	}
	return appConfig.GS.Installations, nil
}
