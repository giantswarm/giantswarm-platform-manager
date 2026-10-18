package plan

import (
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// GitHubApp is a GitHub App of the installation's own the render runs on
// (render.AppSpec): its name and what the record holds of it, the secret
// tooling's step that creates it where its credentials are not on record,
// and, once the App is due a successor, the rotation: the step that
// replaces it and the commit that swaps its credentials.
type GitHubApp struct {
	render.AppSpec
	// NotOnRecord says the App's credentials are not on record: the commit
	// supplies them, after Create made the App.
	NotOnRecord bool `json:"notOnRecord,omitempty"`
	// RotationDue is the date the App is due a successor
	// (render.GitHubAppRotation months after its creation date on record);
	// empty without one.
	RotationDue string `json:"rotationDue,omitempty"`
	// Rotation is the rotation action the manager raises once RotationDue
	// has come: the successor step and the commit that swaps the
	// credentials; empty before.
	Rotation string `json:"rotation,omitempty"`
	// Replacing says the plan swaps the App's credentials on request (the
	// rotate argument names the App): the commit asks for every field of
	// it again and writes the files holding them anew.
	Replacing bool `json:"replacing,omitempty"`
}

// clock is the time a plan reads an App's age at; a test sets it.
var clock = time.Now

// githubApps are the definition's Apps for the plan: what is not on record
// by the plan's supplied fields, the rotation by the creation date on
// record and now.
func githubApps(in render.Input, p Installation, capability string, now time.Time) []GitHubApp {
	declared, ok := in.(render.GitHubAppsInput)
	if !ok {
		return nil
	}
	var out []GitHubApp
	for _, spec := range declared.GitHubApps() {
		a := GitHubApp{AppSpec: spec}
		for _, f := range p.SuppliedSecrets {
			if f == spec.IDField {
				a.NotOnRecord = true
			}
		}
		if due, ok := spec.OnRecord.RotationDue(now); due != "" {
			a.RotationDue = due
			if ok {
				a.Rotation = spec.Replace + "; then platformctl installation reconcile " + p.Name + " " + capability + " --rotate " + spec.Name + " --commit " + strings.Join(spec.Secrets, " ")
			}
		}
		out = append(out, a)
	}
	return out
}

// apps settles what the plan's Apps change: an App the rotation names
// (rotate) has every field supplied at commit again, its files on record
// written anew; and the record, whose entry of an App takes the id and the
// date a commit learns, is written where the commit supplies an App's id.
// suppliedIn are the indexes into Files that hold each supplied field.
func (p *Installation) apps(rotate []string, suppliedIn map[string][]int, recordPath string) {
	for i := range p.GitHubApps {
		a := &p.GitHubApps[i]
		if slices.Contains(rotate, a.Name) {
			a.Replacing = true
			for _, field := range a.Fields {
				if j := slices.Index(p.SuppliedOnRecord, field); j >= 0 {
					p.SuppliedOnRecord = slices.Delete(p.SuppliedOnRecord, j, j+1)
					p.SuppliedSecrets = append(p.SuppliedSecrets, field)
				}
				for _, k := range suppliedIn[field] {
					p.rewrite(k)
				}
			}
			sort.Strings(p.SuppliedSecrets)
		}
		if slices.Contains(p.SuppliedSecrets, a.IDField) {
			for k := range p.Files {
				if p.Files[k].Path == recordPath {
					p.rewrite(k)
				}
			}
		}
	}
}

// rewrite marks the file at k written by the commit: an unchanged file on
// record is an update after all, written anew whole.
func (p *Installation) rewrite(k int) {
	f := &p.Files[k]
	if f.Change != ChangeUnchanged {
		return
	}
	f.Change, f.Rewritten = ChangeUpdate, true
	p.Diff[ChangeUnchanged]--
	p.Diff[ChangeUpdate]++
}

// SharedAppRefusal is the refusal of a plan whose App runs on an id another
// installation of a different owning organisation has on record, naming
// both; "" where none does. The id is the one supplied at commit (supplied,
// by field; nil for a dry run) where it is one, else the one on record. A
// GitHub App's credentials are never shared between owners.
func SharedAppRefusal(p Installation, customer string, holders map[int][]installations.AppHolder, supplied map[string]string) string {
	for _, a := range p.GitHubApps {
		id := render.AppID(supplied[a.IDField])
		if id == 0 {
			id = a.OnRecord.ID
		}
		for _, h := range holders[id] {
			if h.Installation == p.Name || h.Customer == customer {
				continue
			}
			return "GitHub App id " + strconv.Itoa(id) + " for " + a.Name + " on " + p.Name + " (" + customer + ") is on record for " + h.Installation + " (" + h.Customer + ") as " + h.App.Name +
				": an App's credentials are never shared between owners — create " + p.Name + "'s own App (" + a.Create + ") and commit its credentials"
		}
	}
	return ""
}
