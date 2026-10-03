package verify

import (
	"net/url"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// PlannedDexClient is the kind of a planned Dex client, beside the
// manifests' own kinds: a Dex client is no object of the installation's API
// but an entry of the dex patch.
const PlannedDexClient = "DexClient"

// PlannedObject is an object of the installation a migration creates and
// the record does not carry yet: an object of a file the plan creates (the
// Secret of a Dex client's referenced secret), a Dex client the rendered
// dex patch declares that the dex patch on record lacks. Reason is the
// migration's, as the repository half names the leaves that add it. A live
// check of such an object reads planned while it is missing; once the
// record carries it, the repository half names it no more and a missing
// one is drift again.
type PlannedObject struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

// PlannedObjects are the objects a plan creates that the record lacks.
type PlannedObjects []PlannedObject

// object is the reason of the planned object of the kind (a resource as a
// probe names it, Secret or Deployment.apps) by namespace and name; an
// object whose manifest names no namespace takes the one its kustomization
// sets, so it matches by name. Empty for an object no migration creates,
// and for no name.
func (ps PlannedObjects) object(resource, namespace, name string) string {
	if name == "" {
		return ""
	}
	kind, _, _ := strings.Cut(resource, ".")
	for _, o := range ps {
		if o.Kind == kind && o.Name == name && (o.Namespace == "" || o.Namespace == namespace) {
			return o.Reason
		}
	}
	return ""
}

// client is the reason of the planned Dex client by id; empty for a client
// the record carries.
func (ps PlannedObjects) client(id string) string {
	return ps.object(PlannedDexClient, "", id)
}

// probeClient is the reason of the planned Dex client a Dex auth request
// names in its client_id; empty for any other request.
func (ps PlannedObjects) probeClient(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	if id := p.Query().Get("client_id"); id != "" {
		return ps.client(id)
	}
	return ""
}

// plannedObjects are the objects the plan creates that the record lacks,
// each with the migration that adds it: the objects of every file the plan
// creates whose leaves a migration names, and the extra clients of the
// rendered dex patch the patch on record has no entry for, a migration
// naming their leaves. A built-in client is the shared template's, which
// the patch only adds to (its secret's reference): the plan never creates
// one, so a built-in client Dex does not know is drift. A created file or client no migration names is drift, not
// planned, and is left out. current is a file's content on record by key.
func plannedObjects(files []plan.File, diffs map[string]*fileDiff, clients []plan.DexClient, current func(key string) string) PlannedObjects {
	var out PlannedObjects
	for _, f := range files {
		fd := diffs[fileKey(f.Repository, f.Path)]
		if fd == nil {
			continue
		}
		switch {
		case strings.HasSuffix(f.Path, dexPatchSuffix) && (f.Change == plan.ChangeCreate || f.Change == plan.ChangeUpdate):
			record := flattenLines(current(fd.key))
			for _, c := range clients {
				if c.Client != "" || c.ID == "" {
					continue
				}
				entry := entryPath("oidc.extraStaticClients", c.ID)
				if carries(record, entry) {
					continue
				}
				if reason := firstPlanned(fd, entry); reason != "" {
					out = append(out, PlannedObject{Kind: PlannedDexClient, Name: c.ID, Reason: reason})
				}
			}
		case f.Change == plan.ChangeCreate:
			reason := firstPlanned(fd, "")
			if reason == "" {
				continue
			}
			docs, err := parse(f.Content)
			if err != nil {
				continue
			}
			for _, d := range docs {
				kind, _ := dig(d.value, "kind").(string)
				name, _ := dig(d.value, "metadata", "name").(string)
				namespace, _ := dig(d.value, "metadata", "namespace").(string)
				if kind != "" && name != "" {
					out = append(out, PlannedObject{Kind: kind, Namespace: namespace, Name: name, Reason: reason})
				}
			}
		}
	}
	return out
}

// firstPlanned is the migration's reason of the first leaf the record lacks
// under the path entry of fd (every leaf, for an empty entry).
func firstPlanned(fd *fileDiff, entry string) string {
	for _, d := range fd.diffs {
		if d.absent && d.Planned != "" && within(innerPath(fd.documents, d.Path), entry) {
			return d.Planned
		}
	}
	return ""
}

// carries says whether the flattened record has a leaf at or under entry,
// at any level of its path.
func carries(record flat, entry string) bool {
	for p := range record.values {
		for _, level := range levels(record.documents, p) {
			if within(level, entry) {
				return true
			}
		}
	}
	return false
}

// within says whether the path p is entry or a leaf beneath it; every path
// is within the empty entry.
func within(p, entry string) bool {
	rest, ok := strings.CutPrefix(p, entry)
	return ok && (entry == "" || rest == "" || strings.ContainsRune(".[", rune(rest[0])))
}
