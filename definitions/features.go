package definitions

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// The kinds of a dimension: where it is observed. The file kinds are compared
// against the render, probe is an anonymous HTTP request, live is a read on
// the installation with the person's authority.
const (
	KindConfigMap    = "configmap"
	KindDexSecret    = "dex-secret"
	KindDexConfigMap = "dex-configmap"
	KindExtras       = "extras"
	KindBackstage    = "backstage"
	KindLive         = "live"
	KindProbe        = "probe"
)

// Feature is one consistency feature of a definition: what the verify rolls
// its dimensions up into.
type Feature struct {
	ID          string      `yaml:"-" json:"id"`
	Title       string      `yaml:"title" json:"title"`
	Description string      `yaml:"description" json:"description"`
	Dimensions  []Dimension `yaml:"dimensions" json:"dimensions"`
}

// Dimension is one observed aspect of a feature. A file dimension's key
// names the leaves it observes in words the comparison routes by (the
// grammar is documented in each features.yaml); a live dimension's key is
// prose. CatchAll marks the one dimension of its kind that observes every
// leaf of the kind's files no other dimension names; its key is prose.
type Dimension struct {
	ID       string `yaml:"id" json:"id"`
	Kind     string `yaml:"kind" json:"kind"`
	Key      string `yaml:"key" json:"key"`
	CatchAll bool   `yaml:"catchAll" json:"catchAll,omitempty"`
}

// Probe is one anonymous HTTP probe of a definition (probes.yaml).
type Probe struct {
	ID      string `yaml:"id"`
	Feature string `yaml:"feature"`
	Key     string `yaml:"key"`
	// URL is a Go template over BaseDomain and Installation (the
	// installation's), PortalDomain (the portal's hostname, where the
	// definition has one) and, per Dex client, ClientID and RedirectURI.
	URL string `yaml:"url"`
	// PerDexClient runs the probe once per client of the rendered dex patch.
	PerDexClient bool `yaml:"perDexClient"`
	// Expect are the status codes that mean as defined.
	Expect []int `yaml:"expect"`
}

// Features reads a capability's features in file order.
func Features(capability string) ([]Feature, error) {
	raw, err := FS.ReadFile(capability + "/features.yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Features yaml.Node `yaml:"features"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s/features.yaml: %w", capability, err)
	}
	out := make([]Feature, 0, len(doc.Features.Content)/2)
	for i := 0; i+1 < len(doc.Features.Content); i += 2 {
		var f Feature
		if err := doc.Features.Content[i+1].Decode(&f); err != nil {
			return nil, fmt.Errorf("%s/features.yaml: feature %s: %w", capability, doc.Features.Content[i].Value, err)
		}
		f.ID = doc.Features.Content[i].Value
		out = append(out, f)
	}
	return out, nil
}

// Probes reads a capability's anonymous probes in file order.
func Probes(capability string) ([]Probe, error) {
	raw, err := FS.ReadFile(capability + "/probes.yaml")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Probes []Probe `yaml:"probes"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s/probes.yaml: %w", capability, err)
	}
	return doc.Probes, nil
}
