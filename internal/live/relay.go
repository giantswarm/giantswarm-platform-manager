package live

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/giantswarm/mcp-toolkit/urlelicitation"
	"github.com/mark3labs/mcp-go/mcp"
)

// urlElicitation is what a read on a context from WithRelay declares to
// muster.
var urlElicitation = mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{URL: &struct{}{}}}

// Relay binds the first URL elicitation to its originating call and sends
// the caller's answer only to that call, once per handler invocation.
type Relay struct {
	mu          sync.Mutex
	retry       *urlelicitation.Retry
	answered    bool
	elicitation *Elicitation
}

// Elicitation is a URL interaction requested by muster.
type Elicitation struct {
	// Target names the read: the tool and a digest of its arguments.
	Target string
	// Relayed is muster's URL elicitation and requestState.
	Relayed *urlelicitation.Relayed
}

type relayKey struct{}

// WithRelay records URL elicitations and forwards a matching retry once.
func WithRelay(ctx context.Context, retry *urlelicitation.Retry) (context.Context, *Relay) {
	r := &Relay{retry: retry}
	return context.WithValue(ctx, relayKey{}, r), r
}

func relayFrom(ctx context.Context) *Relay {
	r, _ := ctx.Value(relayKey{}).(*Relay)
	return r
}

// ErrURLInputPending prevents effects while a URL interaction is pending.
var ErrURLInputPending = errors.New("muster requires URL input")

// Pending refuses effects while a URL interaction awaits the caller.
func Pending(ctx context.Context) error {
	r := relayFrom(ctx)
	if r == nil {
		return nil
	}
	elicitation := r.Elicitation()
	if elicitation == nil {
		return nil
	}
	return fmt.Errorf("%w; nothing was changed, complete the URL interaction before retrying: %s %s", ErrURLInputPending, elicitation.Relayed.Message, elicitation.Relayed.URL)
}

// Elicitation returns the first pending URL interaction, or nil.
func (r *Relay) Elicitation() *Elicitation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.elicitation
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
	if r.elicitation == nil {
		r.elicitation = &Elicitation{Target: target, Relayed: relayed}
	}
}
