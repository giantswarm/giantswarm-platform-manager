package plan

import (
	"context"
	"errors"
	"fmt"
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

// refuse refuses gs where the commit would draw it here — its files are
// created, or it rotates, forced or requested — and not kept: the commit
// draws a value per installation and the manager decrypts nothing, so the
// value drawn here never reaches the peer's file, and the two sides of the
// pair would disagree (the token exchange fails with invalid_client). A
// value kept on both sides stands. A peer that cannot be read is a refusal
// as well: nothing says the pair would agree.
func (p peer) refuse(ctx context.Context, gs *GeneratedSecret, read Reader) {
	gs.Peer = p.file()
	if gs.Kept {
		return
	}
	err := p.err
	if err == nil {
		_, err = read(ctx, p.ref.repository, p.ref.path)
	}
	pair := fmt.Sprintf("%s is one value with %s's %s, in %s's plan", gs.Name, p.installation, gs.Peer, p.installation)
	var why string
	switch {
	case err == nil:
		why = fmt.Sprintf("the value drawn here (%s) would not reach %s's file, which keeps its own", strings.Join(gs.Files, ", "), p.installation)
	case errors.Is(err, gh.ErrNotFound):
		why = fmt.Sprintf("%s's file is not on record, so each side would draw its own value", p.installation)
	default:
		why = fmt.Sprintf("%s's file could not be read (%v), so nothing says the two sides would agree", p.installation, err)
	}
	gs.Rotates, gs.ForcedBy = false, ""
	gs.Refusal = pair + ": " + why + " and the token exchange would fail with invalid_client — the manager decrypts nothing, so a person puts the one value on both sides"
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
