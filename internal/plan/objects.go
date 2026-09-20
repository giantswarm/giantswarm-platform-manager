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
}

// kustomizationKind is kustomize's own document: it lists objects and is
// none; helmReleaseKind is the object whose deletion uninstalls a chart.
const (
	kustomizationKind = "Kustomization"
	helmReleaseKind   = "HelmRelease"
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
