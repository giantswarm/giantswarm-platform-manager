package plan

import (
	"bytes"
	"errors"
	"io"
	"sort"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// Object is one Kubernetes object the definition renders on the
// installation: a manifest among its files (a Secret, an OCIRepository, a
// HelmRelease, a RemoteApp) or a HelmRelease its probes name — what the
// fleet's Kustomization applies from the tree, and what stays when the tree
// leaves the record and the Kustomization does not prune.
type Object struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	// Takes is what deleting the object takes with it besides itself, on a
	// checklist line (Checklist); empty for an object that takes nothing.
	Takes string `json:"takes,omitempty"`
	// Installation, on a reconcile's checklist, is the installation the
	// object is on where that is not the plan's own: its file is in another
	// installation's tree (a portal host's extras/backstage/). Empty for the
	// plan's own installation.
	Installation string `json:"installation,omitempty"`
	// Repository, on a reconcile's checklist, is the repository whose pull
	// request takes the object off the record.
	Repository string `json:"repository,omitempty"`
}

// on is the object on its installation: what names it on a checklist,
// without what its deletion takes or the repository it leaves.
func (o Object) on() Object {
	return Object{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, Installation: o.Installation}
}

// identity is the object as a render names it: kind, namespace and name.
func (o Object) identity() Object {
	return Object{Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}
}

// listed says whether the object goes on a checklist: a named object,
// kustomize's own documents aside.
func (o Object) listed() bool {
	return o.Kind != "" && o.Name != "" && o.Kind != kustomizationKind && o.Kind != "Component"
}

// kustomizationKind is kustomize's own document: it lists objects and is
// none; helmReleaseKind is the object whose deletion uninstalls a chart.
const (
	kustomizationKind = "Kustomization"
	helmReleaseKind   = "HelmRelease"
	namespaceKind     = "Namespace"
)

// Objects are the objects res renders on the installation, the HelmReleases
// first (deleting one uninstalls its chart, and with it what the chart made),
// then by kind, namespace and name; each once.
func Objects(res *render.Result) []Object {
	seen := map[Object]bool{}
	var out []Object
	add := func(o Object) {
		if o.Kind == "" || o.Name == "" || o.Kind == kustomizationKind || seen[o] {
			return
		}
		seen[o] = true
		out = append(out, o)
	}
	for _, p := range res.Probes {
		if p.Kind == render.HelmReleaseReady {
			add(Object{Kind: helmReleaseKind, Namespace: p.Namespace, Name: p.Name})
		}
	}
	for _, files := range res.Files {
		for _, f := range files {
			for _, o := range manifests(f.Content) {
				add(o)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Kind == helmReleaseKind) != (b.Kind == helmReleaseKind) {
			return a.Kind == helmReleaseKind
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	return out
}

// manifests reads the Kubernetes objects out of a rendered file: every YAML
// document with a kind and a metadata.name. A values file, a patch or a
// document that does not parse contributes none.
func manifests(content []byte) []Object {
	var out []Object
	dec := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			return out
		}
		out = append(out, Object{Kind: doc.Kind, Namespace: doc.Metadata.Namespace, Name: doc.Metadata.Name})
	}
}

// InstallationChecklist is the part of a checklist on one installation.
type InstallationChecklist struct {
	Installation string
	Objects      []Object
}

// ChecklistByInstallation splits a plan's checklist by the installation
// each object is on — the plan's own (installation) first, then the others
// in the checklist's order — each part in the checklist's order.
func ChecklistByInstallation(installation string, checklist []Object) []InstallationChecklist {
	var out []InstallationChecklist
	add := func(on string, o Object) {
		for i := range out {
			if out[i].Installation == on {
				out[i].Objects = append(out[i].Objects, o)
				return
			}
		}
		out = append(out, InstallationChecklist{Installation: on, Objects: []Object{o}})
	}
	for _, own := range []bool{true, false} {
		for _, o := range checklist {
			if (o.Installation == "" || o.Installation == installation) == own {
				on := o.Installation
				if own {
					on = installation
				}
				add(on, o)
			}
		}
	}
	return out
}
