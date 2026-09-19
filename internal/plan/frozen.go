package plan

import (
	"fmt"
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

// frozen decides, for every generated name, what the commit step does with
// the files on record that hold it. The manager decrypts nothing: a secret
// file that exists is never read, so its value cannot be written into a
// second file — the name is frozen in it. A file to create (or a plain file
// to write) that needs a frozen name makes the commit rotate: a new value is
// drawn and written into every file of the name, the frozen ones rewritten.
// A rewritten file gives every name it holds a new value, so a name frozen in
// it rotates as well — down to the files that share those (a valkey password
// held by the server's credentials and by its Valkey's). A frozen name needed
// by no file keeps its value; the file on record is left alone. A rotation
// through a file the definition does not own whole would write over the
// other owners' values: refused, naming the file.
func frozen(generated map[string]*GeneratedSecret, holders []holder) {
	frozenIn := map[string][]string{}
	needing := map[string]bool{}
	byFile := map[string]holder{}
	for _, h := range holders {
		byFile[h.file] = h
		switch {
		case h.change == ChangeUnchanged || h.change == ChangeUnknown:
		case h.change == ChangeUpdate && len(h.secret) > 0:
			for _, n := range h.secret {
				frozenIn[n] = append(frozenIn[n], h.file)
			}
		default:
			for _, n := range append(h.secret, h.public...) {
				needing[n] = true
			}
		}
	}
	rewrite := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for n := range needing {
			for _, f := range frozenIn[n] {
				if rewrite[f] {
					continue
				}
				rewrite[f] = true
				changed = true
				for _, m := range byFile[f].secret {
					needing[m] = true
				}
			}
		}
	}
	for name, gs := range generated {
		files := frozenIn[name]
		sort.Strings(files)
		gs.FrozenIn = files
		if len(files) == 0 || !needing[name] {
			continue
		}
		gs.Rotates = true
		for _, f := range files {
			if byFile[f].shared {
				gs.Rotates = false
				gs.Refusal = fmt.Sprintf("%s is frozen in %s, a file the definition does not own whole: rotating it would write over the other owners' values, and the manager decrypts nothing", name, f)
				break
			}
		}
	}
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
