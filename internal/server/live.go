package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
)

// liveRealm names the live surface in its WWW-Authenticate challenge.
const liveRealm = "giantswarm-platform-manager-live"

// liveGuard admits a request whose bearer is a forwarded ID token the check
// accepts, and puts the person and the token on the request. There is no
// sign-in to point at: muster forwards the token of the person's own session
// (MCPServer auth.forwardToken), so a refusal names what the token lacked.
type liveGuard struct {
	verify func(ctx context.Context, token string) (*identity.Identity, error)
	log    *slog.Logger
}

func (g *liveGuard) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerOf(r)
		if token == "" {
			g.challenge(w, "", "no bearer token: this surface takes the ID token muster forwards for the person (auth.forwardToken)")
			return
		}
		who, err := g.verify(r.Context(), token)
		if err != nil {
			g.log.Warn("forwarded token refused", "error", err)
			g.challenge(w, "invalid_token", "the forwarded token was not accepted: "+err.Error())
			return
		}
		ctx := identity.ContextWith(r.Context(), who)
		ctx = identity.ContextWithToken(ctx, token)
		g.log.Debug("authenticated live request", "caller", who.String(), "path", r.URL.Path)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (g *liveGuard) challenge(w http.ResponseWriter, code, description string) {
	challenge := fmt.Sprintf(`Bearer realm=%q`, liveRealm)
	if code != "" {
		challenge += fmt.Sprintf(`, error=%q, error_description=%q`, code, description)
	}
	w.Header().Set("WWW-Authenticate", challenge)
	http.Error(w, description, http.StatusUnauthorized)
}
