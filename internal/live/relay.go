package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/giantswarm/mcp-toolkit/urlelicitation"
	"github.com/mark3labs/mcp-go/mcp"
)

// urlElicitation is what a read on a context from WithRelay declares to
// muster.
var urlElicitation = mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{URL: &struct{}{}}}

// Relay carries muster's sign-ins between one tool call and that call's
// caller. Reads through muster on a context from WithRelay declare URL mode
// elicitation, so muster asks for a sign-in as an InputRequiredResult; the
// first one is kept for the caller, and the caller's answer goes once to the
// read it was asked for, the same tool with the same arguments. A read
// muster asked a sign-in for answers *verify.AuthRequired, as muster's
// auth_required does.
type Relay struct {
	mu       sync.Mutex
	retry    *urlelicitation.Retry
	answered bool
	signIn   *SignIn
}

// SignIn is a sign-in muster asked a read for.
type SignIn struct {
	// Target names the read: the tool and a digest of its arguments.
	Target string
	// Relayed is muster's URL elicitation and requestState.
	Relayed *urlelicitation.Relayed
}

type relayKey struct{}

// WithRelay returns a context whose reads through muster relay sign-ins,
// and the Relay that collects them. retry is the caller's answer to a
// sign-in relayed earlier; nil on a first call.
func WithRelay(ctx context.Context, retry *urlelicitation.Retry) (context.Context, *Relay) {
	r := &Relay{retry: retry}
	return context.WithValue(ctx, relayKey{}, r), r
}

func relayFrom(ctx context.Context) *Relay {
	r, _ := ctx.Value(relayKey{}).(*Relay)
	return r
}

// SignIn is the first sign-in muster asked for; nil when none.
func (r *Relay) SignIn() *SignIn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.signIn
}

// answer is the caller's answer for the read target, once.
func (r *Relay) answer(target string) mcp.MultiRoundTripParams {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retry == nil || r.answered || r.retry.Target != target {
		return mcp.MultiRoundTripParams{}
	}
	r.answered = true
	return r.retry.Params
}

func (r *Relay) record(target string, relayed *urlelicitation.Relayed) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.signIn == nil {
		r.signIn = &SignIn{Target: target, Relayed: relayed}
	}
}

// callTarget names a read through muster: the tool and a digest of its
// arguments. encoding/json orders map keys, so equal arguments name alike.
func callTarget(tool string, args map[string]any) string {
	raw, _ := json.Marshal(args)
	digest := sha256.Sum256(raw)
	return tool + "\x00" + hex.EncodeToString(digest[:])
}
