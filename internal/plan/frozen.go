package plan

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// holder is one file of the plan as the commit step's encryption sees it:
// which generated names it holds as a secret — a whole value or a key pair's
// private half, only ever in a secret file — and which public halves it
// carries in plaintext.
type holder struct {
	file   string
	change Change
	shared bool
	secret []string
	public []string
}

// names are every generated name the file holds.
func (h holder) names() []string {
	return append(slices.Clone(h.secret), h.public...)
}

// frozen decides, for every generated name, what the commit step does with
// the files on record that hold it, and answers the files on record a
// rotation rewrites. The manager decrypts nothing: a secret file that exists
// is never read, so its value cannot be written into a second file — the
// name is frozen in it. A file on record the render leaves as it is
// (unchanged: the same outside the values) keeps its names: the value on
// record stands and nothing is written. A file that has to be written and
// holds a frozen name forces a rotation — a file to create, an existing file
// whose plaintext skeleton the render changes, a plain file to write that
// carries a key pair's public half: a new value is drawn and written into
// every file of the name, the kept ones rewritten. A rewritten file gives
// every name it holds a new value, so a name kept in it rotates as well —
// down to the files that share those (a valkey password held by the server's
// credentials and by its Valkey's). Every rotating name says which file
// forced it. A rotation through a file the definition does not own whole
// would write over the other owners' values: refused, naming the file.
func frozen(generated map[string]*GeneratedSecret, holders []holder) map[string]bool {
	frozenIn := map[string][]string{} // name → the secret files on record that hold it
	onRecord := map[string][]string{} // name → every file on record that holds it, a rotation rewrites them
	byFile := map[string]holder{}
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
	for _, h := range holders {
		byFile[h.file] = h
		switch h.change {
		case ChangeUnknown:
			continue
		case ChangeUnchanged, ChangeUpdate:
			for _, n := range h.secret {
				frozenIn[n] = append(frozenIn[n], h.file)
			}
			for _, n := range h.names() {
				onRecord[n] = append(onRecord[n], h.file)
			}
		}
		if h.change != ChangeUnchanged {
			need(h.names(), h.file)
		}
	}
	rewrite := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, n := range keys(needing) {
			for _, f := range onRecord[n] {
				if rewrite[f] {
					continue
				}
				rewrite[f] = true
				changed = true
				need(byFile[f].names(), f)
			}
		}
	}
	for name, gs := range generated {
		files := frozenIn[name]
		sort.Strings(files)
		gs.FrozenIn = files
		if len(files) == 0 {
			continue
		}
		if !needing[name] {
			gs.Kept = true
			continue
		}
		gs.Rotates = true
		gs.ForcedBy = forcedBy[name]
		for _, f := range files {
			if byFile[f].shared {
				gs.Rotates = false
				gs.ForcedBy = ""
				gs.Refusal = fmt.Sprintf("%s is frozen in %s, a file the definition does not own whole: rotating it would write over the other owners' values, and the manager decrypts nothing", name, f)
				break
			}
		}
	}
	return rewrite
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
	var out []string
	for _, g := range p.GeneratedSecrets {
		if g.Rotates {
			out = append(out, g.Name)
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
