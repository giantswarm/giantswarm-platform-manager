package plan

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// peer is the other side of a generated value two installations hold (a
// hub's token-exchange credentials and the target's Dex-side copy): the
// file in the peer's management-clusters repository, or why that
// repository is not known.
type peer struct {
	installation string
	ref          fileRef
	err          error
}

// peerOf resolves p through the registry's installations by name.
func peerOf(p render.Peer, byName map[string]installations.Installation) peer {
	out := peer{installation: p.Installation, ref: fileRef{path: p.Path}}
	inst, ok := byName[p.Installation]
	switch {
	case !ok:
		out.err = errors.New("not in the registry")
	case inst.Repositories.ManagementClusters == "":
		out.err = errors.New("no management-clusters repository on record")
	default:
		out.ref.repository = inst.Repositories.ManagementClusters
	}
	return out
}

// file is the peer's file as a plan names it.
func (p peer) file() string {
	if p.ref.repository == "" {
		return p.ref.path
	}
	return p.ref.key()
}

// created says the commit would write the value here for the first time:
// no file on record holds it, nothing rotates and no vault carries it.
func (g GeneratedSecret) created() bool {
	return !g.Kept && !g.Rotates && len(g.FrozenIn) == 0 && len(g.Carries) == 0
}

// refuse settles gs against the peer's file. A value kept on both sides
// stands. A value created here while the peer's file is on record is the
// person's to supply: the manager decrypts nothing, so the value on record
// cannot be copied here by the commit, and the person puts it in from their
// vault at commit (Supplied, SuppliedSecrets). A value created here while
// the peer's file is not on record is refused: the commit draws a value per
// installation, so each side would draw its own and the two would disagree
// (the token exchange fails with invalid_client) — unless the peer's plan
// creates its file in the same wave, which draws the value once for both
// (ShareDraws). A value that rotates here, forced or requested, is refused:
// the value drawn here never reaches the peer's file. A peer that cannot be
// read is a refusal as well: nothing says the pair would agree.
func (p peer) refuse(ctx context.Context, gs *GeneratedSecret, read Reader) {
	gs.Peer = p.file()
	if gs.Kept {
		return
	}
	err := p.err
	if err == nil {
		_, err = read(ctx, p.ref.repository, p.ref.path)
	}
	created := gs.created()
	if err == nil && created {
		gs.Supplied = true
		return
	}
	pair := fmt.Sprintf("%s is one value with %s's %s, in %s's plan", gs.Name, p.installation, gs.Peer, p.installation)
	wayOut := "a person puts the one value on both sides"
	var why string
	switch {
	case err == nil:
		why = fmt.Sprintf("the value drawn here (%s) would not reach %s's file, which keeps its own", strings.Join(gs.Files, ", "), p.installation)
	case errors.Is(err, gh.ErrNotFound) && created:
		why = fmt.Sprintf("%s's file is not on record, so each side would draw its own value", p.installation)
		wayOut = fmt.Sprintf("reconcile %s in the same wave, which draws the value once for both sides, or a person puts the one value on both sides", p.installation)
	case errors.Is(err, gh.ErrNotFound):
		why = fmt.Sprintf("%s's file is not on record, so each side would draw its own value", p.installation)
	default:
		why = fmt.Sprintf("%s's file could not be read (%v), so nothing says the two sides would agree", p.installation, err)
	}
	gs.Rotates, gs.ForcedBy = false, ""
	gs.Refusal = pair + ": " + why + " and the token exchange would fail with invalid_client — the manager decrypts nothing, so " + wayOut
}

// refuseRotationHeldBy refuses a rotation of gs where another holder keeps
// the value outside this plan (gs.HeldBy): the commit writes the new value
// into this plan's files alone and the manager decrypts nothing, so the
// holder would keep the old one. Creating the value stands: the person
// supplies the drawn value to the holder, as the holder's definition asks.
func refuseRotationHeldBy(gs *GeneratedSecret) {
	if gs.HeldBy == "" || !gs.Rotates {
		return
	}
	gs.Refusal = fmt.Sprintf("%s would rotate, forced by %s, but %s holds it too and this plan does not write there: the new value would reach %s alone, and the manager decrypts nothing, so a person rotates both sides together",
		gs.Name, gs.ForcedBy, gs.HeldBy, strings.Join(gs.Files, ", "))
	gs.Rotates, gs.ForcedBy = false, ""
}

// ShareDraws pairs the generated values across the plans of one set. A value
// created in one plan whose peer's file another plan of the set creates,
// under the same name and naming this plan's created file as its peer in
// turn, is one value both sides are new for: the wave draws it once and
// writes it to both (the commit's shared draw), so neither side is drawn
// alone. Both sides lose their refusal and name the other's installation
// (DrawnWith). A pair with one side on record, or whose other side is not
// in the set, is left as each plan settled it. ShareDraws answers the
// installations whose plans changed, sorted, for their commit refusal to be
// read again.
func ShareDraws(plans []*Installation) []string {
	created := map[string][]string{} // a file a plan creates, "<repository>:<path>" → the generated names it holds
	owner := map[string]*Installation{}
	for _, p := range plans {
		for _, f := range p.Files {
			if f.Change == ChangeCreate && len(f.Generated) > 0 {
				key := f.Repository + ":" + f.Path
				created[key], owner[key] = f.Generated, p
			}
		}
	}
	changed := map[string]bool{}
	for _, p := range plans {
		for i := range p.GeneratedSecrets {
			g := &p.GeneratedSecrets[i]
			other := owner[g.Peer]
			if other == nil || other == p || !g.created() || !slices.Contains(created[g.Peer], g.Name) {
				continue
			}
			j := slices.IndexFunc(other.GeneratedSecrets, func(o GeneratedSecret) bool { return o.Name == g.Name })
			if j < 0 {
				continue
			}
			o := &other.GeneratedSecrets[j]
			if !o.created() || owner[o.Peer] != p || !slices.Contains(created[o.Peer], g.Name) {
				continue
			}
			g.DrawnWith, o.DrawnWith = other.Name, p.Name
			g.Refusal, o.Refusal = "", ""
			changed[p.Name], changed[other.Name] = true, true
		}
	}
	return slices.Sorted(maps.Keys(changed))
}
