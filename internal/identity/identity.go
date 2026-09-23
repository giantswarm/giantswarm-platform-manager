// Package identity carries the authenticated caller through a request. On the
// App-pinned path it is the person GitHub named when this server verified the
// bearer — the GitHub user token muster obtained for them through the App
// giantswarm-platform-manager and puts on every call — and that token, which
// every GitHub call of the request runs with. Next to it muster forwards the
// person's platform ID token in ForwardedIdentityHeader (auth.forwardIdentity);
// the request carries it unverified, and the live tools verify it and run as
// the person it names, the loop-back through muster with that token.
// The manager does nothing as itself for a person — every read and write is
// bounded to the person's rights — so a context without an identity only
// exists in tests and in a server that runs without OAuth.
package identity

import (
	"context"
	"log/slog"
)

// SignIn names the one way to a token this server accepts on the App-pinned
// path: the consent muster runs for the App giantswarm-platform-manager, once
// per person.
const SignIn = "connect giantswarm-platform-manager in muster (core_auth_login server=giantswarm-platform-manager), then call again"

// Identity is the authenticated caller: as GET /user answered for a GitHub
// bearer (Login, ID, Email), or as the ID token muster forwarded names the
// person (Subject, Email).
type Identity struct {
	Login string `json:"login,omitempty"`
	ID    int64  `json:"id,omitempty"`
	// Email is the public email GET /user answers, or empty: what the
	// gateway knows a linked Slack member by; for the live tools the ID
	// token's email claim.
	Email string `json:"email,omitempty"`
	// Subject is the ID token's sub for the live tools: the key the
	// loop-back session is held under.
	Subject string `json:"subject,omitempty"`
}

// String is the caller as logged: the GitHub login, else the email, else
// the subject.
func (id *Identity) String() string {
	switch {
	case id == nil:
		return ""
	case id.Login != "":
		return id.Login
	case id.Email != "":
		return id.Email
	}
	return id.Subject
}

type ctxKey int

const (
	identityKey ctxKey = iota
	tokenKey
	forwardedKey
)

// ForwardedIdentityHeader is the header muster puts the person's platform ID
// token in on every call to a registration with auth.forwardIdentity
// (MCPServer spec.auth.forwardIdentity: true), next to the bearer.
const ForwardedIdentityHeader = "X-Muster-Id-Token"

// ForwardIdentity names the muster setting that forwards the header, for the
// refusals and get_info.
const ForwardIdentity = "auth.forwardIdentity"

// ContextWith returns ctx carrying id.
func ContextWith(ctx context.Context, id *Identity) context.Context {
	if id == nil {
		return ctx
	}
	return context.WithValue(ctx, identityKey, id)
}

// FromContext returns the caller, if any.
func FromContext(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityKey).(*Identity)
	return id, ok && id != nil
}

// Caller is the caller's String(), or "" without one.
func Caller(ctx context.Context) string {
	id, _ := FromContext(ctx)
	return id.String()
}

// LogAttr is the structured-log attribute every write carries.
func LogAttr(ctx context.Context) slog.Attr {
	if c := Caller(ctx); c != "" {
		return slog.String("caller", c)
	}
	return slog.String("caller", "anonymous")
}

// ContextWithToken returns ctx carrying the caller's token — the bearer of
// the request: the GitHub user token every GitHub call runs with.
func ContextWithToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, tokenKey, token)
}

// TokenFromContext returns the caller's token, if any.
func TokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(tokenKey).(string)
	return t, ok && t != ""
}

// ContextWithForwardedToken returns ctx carrying the ID token muster
// forwarded in ForwardedIdentityHeader, as received: nothing has verified it.
func ContextWithForwardedToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return context.WithValue(ctx, forwardedKey, token)
}

// ForwardedTokenFromContext returns the forwarded ID token, unverified, if
// the request carried one.
func ForwardedTokenFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(forwardedKey).(string)
	return t, ok && t != ""
}
