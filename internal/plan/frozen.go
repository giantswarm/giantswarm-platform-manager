package plan

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// holder is one file of the plan as the commit step's encryption sees it:
// which generated names it holds as a secret — a whole value or a key pair's
// private half, only ever in a secret file — and which public halves it
// carries in plaintext. lacks are the secret names the file on record has no
// key for: the render puts their marker under a key the record lacks (a
// file to create lacks every one). fillable says the file on record is the
// render's but for the keys it lacks: a value put under each by the caller's
// vault leaves nothing for the commit to write there. paths are the key
// paths the file holds each name at.
type holder struct {
	file     string
	change   Change
	shared   bool
	secret   []string
	public   []string
	lacks    []string
	fillable bool
	paths    map[string]string
}

// names are every generated name the file holds.
func (h holder) names() []string {
	return append(slices.Clone(h.secret), h.public...)
}

// onRecord are the secret names the file on record holds a value of.
func (h holder) onRecord() []string {
	if h.change != ChangeUnchanged && h.change != ChangeUpdate {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(h.secret), func(n string) bool { return slices.Contains(h.lacks, n) })
}

// lacking are the secret names a file to write has no value of on record:
// every one for a file to create.
func (h holder) lacking() []string {
	if h.change == ChangeCreate {
		return h.secret
	}
	return h.lacks
}

// fills says the caller's vault can put the values the file lacks under
// their keys and leave the file as the render has it: a file to create, or
// a file on record whose only differences from the render are those keys.
func (h holder) fills() bool {
	return h.change == ChangeCreate || h.change == ChangeUpdate && h.fillable
}

// pending says the file on record stands without name: a credentials
// revision the record lacks joins the file with the next rotation asked for,
// which rewrites it from the render.
func (h holder) pending(name string, revisions map[string]bool) bool {
	return h.change != ChangeCreate && revisions[name] && slices.Contains(h.lacks, name)
}

// at is the name's place in the file: "<repository>:<path>#<key path>".
func (h holder) at(name string) string {
	if p := h.paths[name]; p != "" {
		return h.file + "#" + p
	}
	return h.file
}

// Carry is one value the caller's vault puts into a file before the commit,
// the manager decrypting nothing: To is the file and key path to write,
// "<repository>:<path>#<key path>" — a file to create (Create) or a file on
// record that lacks the key — and From the record's file and key path whose
// value it takes, empty for a value the vault draws there because no file on
// record holds it.
type Carry struct {
	From   string `json:"from,omitempty"`
	To     string `json:"to"`
	Create bool   `json:"create,omitempty"`
}

// causeSkeleton is why the commit writes a file on record anew whole: the
// render changes it beyond the keys it lacks (or it is a plain file).
const causeSkeleton = "the render changes it beyond the keys it lacks"

// frozen decides, for every generated name, what the commit step does with
// the files that hold it, and answers the files the commit writes whole —
// drawing every value they hold anew — by file. The manager decrypts
// nothing: a secret file on record is never read, so a value it holds cannot
// be written into a second file by the commit. A file on record the render
// leaves as it is (unchanged: the same outside the values) keeps its names:
// the value on record stands and nothing is written. A file that needs a
// value some file on record holds — a file to create, or a file on record
// whose only differences from the render are the keys it lacks (fillable) —
// takes it by the caller's vault before the commit, a Carry from the record's
// file and key path, and the value stays kept: the commit writes no file for
// it, and is refused until the carry is on record (CarryRefusal). A value no
// file on record holds that a file on record lacks is drawn by the vault the
// same way (a Carry without a From) and every other file to write takes it
// from there; a credentials revision a file on record lacks waits instead
// (pending): the file stands, the revision joins it with the next rotation
// asked for. A file to create whose values no record holds is written by the
// commit. A file the commit writes whole — a file on record whose skeleton
// the render changes beyond the keys it lacks, a plain file to write that
// carries a key pair's public half — gives every name it holds a new value:
// a rotation, forced by the file and naming the cause (Cause), that reaches
// every file holding one of those names, rewritten whole as well, down to
// the files that share those (a valkey password held by the server's
// credentials and by its Valkey's). A name the person asked to rotate
// (requested) needs a new value whatever its files, forced by the request
// (ForcedByRequest), and its files are rewritten the same way. A rotation
// through a file the definition does not own whole would write over the
// other owners' values: refused, naming the file — a requested one alike.
// Where the capability is on record (onRecord), a rotation no request
// reaches is refused as well, naming the file that forces it: a new value
// ends every session and client that holds the old one, so a reconcile
// rotates only what a person asked for — the names asked for and every name
// of a file their rotation rewrites. revisions names the credentials
// revisions the render draws.
func frozen(generated map[string]*GeneratedSecret, holders []holder, requested []string, onRecord bool, revisions map[string]bool) map[string]bool {
	frozenIn := map[string][]string{} // name → the secret files on record that hold its value
	filesOf := map[string][]string{}  // name → every file that holds it, on record or to write, a rotation rewrites them
	byFile := map[string]holder{}
	whole := map[string]string{} // a file the commit writes whole → why
	var writes []string          // the fillable files to write, in order
	for _, h := range holders {
		if h.change == ChangeUnknown {
			continue
		}
		byFile[h.file] = h
		for _, n := range h.onRecord() {
			frozenIn[n] = append(frozenIn[n], h.file)
		}
		for _, n := range h.names() {
			if h.pending(n, revisions) {
				if gs := generated[n]; gs != nil {
					gs.PendingIn = append(gs.PendingIn, h.file)
				}
				continue
			}
			filesOf[n] = append(filesOf[n], h.file)
		}
		switch {
		case h.change == ChangeUnchanged:
		case h.shared || !h.fills():
			whole[h.file] = causeSkeleton
		default:
			writes = append(writes, h.file)
		}
	}
	drawnIn := carries(writes, byFile, frozenIn, revisions, whole)
	needing := map[string]bool{}
	forcedBy := map[string]string{}
	need := func(names []string, by string) {
		for _, n := range names {
			if !needing[n] {
				needing[n] = true
				forcedBy[n] = by
			}
		}
	}
	need(requested, ForcedByRequest)
	for _, f := range slices.Sorted(maps.Keys(whole)) {
		need(byFile[f].names(), f)
	}
	for changed := true; changed; {
		changed = false
		for _, n := range keys(needing) {
			for _, f := range filesOf[n] {
				if _, ok := whole[f]; ok {
					continue
				}
				whole[f] = "rewritten with " + n
				changed = true
				need(byFile[f].names(), f)
			}
		}
	}
	asked := reached(requested, filesOf, byFile)
	for name, gs := range generated {
		files := frozenIn[name]
		sort.Strings(files)
		gs.FrozenIn = files
		sort.Strings(gs.PendingIn)
		if !needing[name] || len(files) == 0 {
			gs.Kept = len(files) > 0
			gs.Carries = carriesOf(name, writes, byFile, frozenIn, drawnIn, whole, revisions)
			continue
		}
		if onRecord && !asked[name] {
			gs.Refusal = fmt.Sprintf("%s would rotate, forced by %s: the capability is on record, and a new value ends every session and client that holds the old one; a reconcile rotates a value only on request (rotate %s)", name, forced(forcedBy[name], whole), name)
			continue
		}
		gs.Rotates = true
		gs.ForcedBy = forcedBy[name]
		gs.Cause = whole[gs.ForcedBy]
		for _, f := range files {
			if byFile[f].shared {
				gs.Rotates = false
				gs.ForcedBy, gs.Cause = "", ""
				gs.Refusal = fmt.Sprintf("%s is frozen in %s, a file the definition does not own whole: rotating it would write over the other owners' values, and the manager decrypts nothing", name, f)
				break
			}
		}
	}
	out := make(map[string]bool, len(whole))
	for f := range whole {
		out[f] = true
	}
	return out
}

// forced is the file a rotation is forced by with its cause, or the request.
func forced(by string, whole map[string]string) string {
	if cause := whole[by]; cause != "" {
		return by + " (" + cause + ")"
	}
	return by
}

// carries decides which of the fillable files to write the caller's vault
// fills and answers, for every value no file on record holds that the vault
// draws, the file it is drawn into. A file on record that lacks a value takes
// it from the record where a file holds it, has it drawn by the vault where
// none does (a credentials revision waits instead), and the vault fills a
// file to create that shares a value with the record or with a file the
// vault draws one into. A file to create whose values no record holds and no
// vault draws is written by the commit (whole); the files on record are
// settled first, so a file to create never draws what one of them does, and
// a file to create holding a value another draws takes it from there.
func carries(writes []string, byFile map[string]holder, frozenIn map[string][]string, revisions map[string]bool, whole map[string]string) map[string]string {
	drawnIn := map[string]string{} // a value no file on record holds → the file the vault draws it into
	onRecord := func(n string) bool { return len(frozenIn[n]) > 0 || drawnIn[n] != "" }
	files := slices.Clone(writes)
	sort.SliceStable(files, func(i, j int) bool {
		return byFile[files[i]].change == ChangeUpdate && byFile[files[j]].change == ChangeCreate
	})
	for changed := true; changed; {
		changed = false
		for _, f := range files {
			h := byFile[f]
			if _, ok := whole[f]; ok || h.change == ChangeCreate && !slices.ContainsFunc(h.secret, onRecord) {
				continue
			}
			for _, n := range h.lacking() {
				if !onRecord(n) && !h.pending(n, revisions) {
					drawnIn[n] = f
					changed = true
				}
			}
		}
	}
	for _, f := range files {
		if h := byFile[f]; h.change == ChangeCreate && !slices.ContainsFunc(h.secret, onRecord) {
			whole[f] = "a new file whose values no record holds"
		}
	}
	return drawnIn
}

// carriesOf are the carries of name into the fillable files to write that
// lack it, the vault's draw of it first, in file order; a file that stands
// without it (pending) takes none.
func carriesOf(name string, writes []string, byFile map[string]holder, frozenIn map[string][]string, drawnIn map[string]string, whole map[string]string, revisions map[string]bool) []Carry {
	var out []Carry
	for _, f := range writes {
		h := byFile[f]
		if _, ok := whole[f]; ok || !slices.Contains(h.lacking(), name) || h.pending(name, revisions) {
			continue
		}
		c := Carry{To: h.at(name), Create: h.change == ChangeCreate}
		switch {
		case len(frozenIn[name]) > 0:
			c.From = byFile[frozenIn[name][0]].at(name)
		case drawnIn[name] == f:
			out = append([]Carry{c}, out...)
			continue
		case drawnIn[name] != "":
			c.From = byFile[drawnIn[name]].at(name)
		default:
			continue
		}
		out = append(out, c)
	}
	return out
}

// reached are the names a rotation of names draws anew: the names, and every
// name a file holds that one of them is held by, down to the files that
// share those.
func reached(names []string, filesOf map[string][]string, byFile map[string]holder) map[string]bool {
	out := map[string]bool{}
	for queue := slices.Clone(names); len(queue) > 0; queue = queue[1:] {
		n := queue[0]
		if out[n] {
			continue
		}
		out[n] = true
		for _, f := range filesOf[n] {
			queue = append(queue, byFile[f].names()...)
		}
	}
	return out
}

// Rotated are the files on record the commit rewrites with a new value:
// "<repository>:<path>" of every file a rotating name is frozen in.
func (p Installation) Rotated() map[string]bool {
	out := map[string]bool{}
	for _, g := range p.GeneratedSecrets {
		if !g.Rotates {
			continue
		}
		for _, f := range g.FrozenIn {
			out[f] = true
		}
	}
	return out
}

// Rotating names the generated values the commit draws anew over a value on
// record, sorted.
func (p Installation) Rotating() []string {
	requested, forced := p.Rotations()
	return slices.Sorted(slices.Values(append(requested, forced...)))
}

// Rotations divides Rotating into the values rotated on request — asked for
// by name, or the credentials revision of one asked for — and the ones a
// file to write forced, each sorted.
func (p Installation) Rotations() (requested, forced []string) {
	for _, g := range p.GeneratedSecrets {
		switch {
		case !g.Rotates:
		case g.ForcedBy == ForcedByRequest:
			requested = append(requested, g.Name)
		default:
			forced = append(forced, g.Name)
		}
	}
	return requested, forced
}

// ForcedByRequest is the ForcedBy of a generated value that rotates because a
// person asked for it by name (reconcile_capability's rotate), or because it
// is the credentials revision of one asked for: no file forced it.
const ForcedByRequest = "request"

// requestedRotations are the generated values of the plan a rotation is asked for,
// each followed by the credentials revision its consumers roll on where the
// definition names one (revisions) — drawing the revision anew restarts every
// workload that reads the value. A name the plan does not list takes no part:
// over a set, a name applies to each installation whose plan lists it.
func requestedRotations(names []string, generated map[string]*GeneratedSecret, revisions map[string]string) []string {
	var out []string
	for _, n := range names {
		if generated[n] == nil {
			continue
		}
		out = append(out, n)
		if r := revisions[n]; generated[r] != nil {
			out = append(out, r)
		}
	}
	return out
}

// FrozenRefusal is why the commit refuses the plan's generated values, or "":
// every name frozen where no rotation is possible, in one clause.
func (p Installation) FrozenRefusal() string {
	var parts []string
	for _, g := range p.GeneratedSecrets {
		if g.Refusal != "" {
			parts = append(parts, g.Refusal)
		}
	}
	return strings.Join(parts, "; ")
}

// CarryRefusal is why the commit refuses the plan while a carry is not on
// record, or "": the manager decrypts nothing, so a value a file to write
// takes from the record, or one the vault draws into a file on record, is
// put there by the caller's vault before the commit — one clause per name.
func (p Installation) CarryRefusal() string {
	var parts []string
	for _, g := range p.GeneratedSecrets {
		if len(g.Carries) == 0 {
			continue
		}
		to := make([]string, 0, len(g.Carries))
		for _, c := range g.Carries {
			to = append(to, c.To)
		}
		if g.Kept {
			parts = append(parts, fmt.Sprintf("%s is on record in %s and %s takes it: carry the value there with your vault before the commit, the manager decrypts nothing", g.Name, strings.Join(g.FrozenIn, ", "), strings.Join(to, ", ")))
		} else {
			parts = append(parts, fmt.Sprintf("%s is drawn by your vault into %s before the commit: a file on record lacks it, and the commit rewrites no encrypted file for one key", g.Name, strings.Join(to, ", ")))
		}
	}
	return strings.Join(parts, "; ")
}
