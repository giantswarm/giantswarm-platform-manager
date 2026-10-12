package plan

import (
	"fmt"
	"slices"
	"strings"
)

// VaultCopy is one value of a wave's vault copy, by key: the generated
// value's name, where the caller's vault reads it (From: the record's file
// and key path, or the peer's file where the value is supplied at commit)
// and where it puts it (To: the file and key path that takes it, or the
// commit's --secret for a supplied one). Outside marks a carry the wave does
// not copy: a generated value of the installation's own, unrelated to the
// pair the wave moves, which the wave refuses by name (CarryRefusal).
type VaultCopy struct {
	Name    string `json:"name"`
	From    string `json:"from,omitempty"`
	To      string `json:"to"`
	Outside bool   `json:"outside,omitempty"`
}

// atCommit is the To of a supplied pair value: the commit takes it.
const atCommit = "--secret %s=… at commit"

// WaveVaultCopies is the vault copy of a wave over a set for this plan,
// selected by the pair the wave moves: the generated values this plan holds
// one side of and another installation the other (Peer) that the wave
// writes a side of — supplied from the peer's file on record, carried into
// a file that lacks it, or drawn once for both sides, which copies nothing.
// Every other carry the record holds — the installation's own credentials a
// file to write lacks — is listed Outside: the wave does not copy it, and
// refuses it by name, so a cut-over commit never carries a credential its
// author did not mean to move. A plan that moves no side of a pair has no
// wave vault copy: its carries are its own reconcile's.
func (p Installation) WaveVaultCopies() []VaultCopy {
	if len(p.movedPair()) == 0 {
		return nil
	}
	var out []VaultCopy
	for _, g := range p.GeneratedSecrets {
		if !g.moved() {
			continue
		}
		if g.Supplied {
			out = append(out, VaultCopy{Name: g.Name, From: g.Peer, To: fmt.Sprintf(atCommit, g.Name)})
		}
		for _, c := range g.Carries {
			out = append(out, VaultCopy{Name: g.Name, From: c.From, To: c.To})
		}
	}
	for _, g := range p.GeneratedSecrets {
		if g.Peer != "" {
			continue
		}
		for _, c := range g.Carries {
			out = append(out, VaultCopy{Name: g.Name, From: c.From, To: c.To, Outside: true})
		}
	}
	return out
}

// moved says whether the wave writes a side of a pair value here: supplied
// from the peer's file, drawn once with the peer's plan, or carried into a
// file that lacks it. A pair kept on both sides moves nothing.
func (g GeneratedSecret) moved() bool {
	return g.Peer != "" && (g.Supplied || g.DrawnWith != "" || len(g.Carries) > 0)
}

// movedPair names the pair values the wave moves in this plan.
func (p Installation) movedPair() []string {
	var names []string
	for _, g := range p.GeneratedSecrets {
		if g.moved() && !slices.Contains(names, g.Name) {
			names = append(names, g.Name)
		}
	}
	return names
}

// outsideRefusal is why the wave refuses the plan over a carry outside the
// pair it moves, naming each key, or "": the wave's vault copy takes the
// pair's values only, and the installation's own reconcile carries the rest.
func (p Installation) outsideRefusal() string {
	var keys []string
	for _, c := range p.VaultCopies {
		if c.Outside {
			keys = append(keys, fmt.Sprintf("%s (%s takes it from %s)", c.Name, c.To, c.From))
		}
	}
	if len(keys) == 0 {
		return ""
	}
	return fmt.Sprintf("the wave's vault copy selects the pair it moves (%s) and %s is outside it: not copied by the wave — reconcile %s alone first, whose plan carries it, or narrow the set",
		strings.Join(p.movedPair(), ", "), strings.Join(keys, "; "), p.Name)
}
