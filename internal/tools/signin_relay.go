package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/giantswarm/mcp-oauth/providers"
	"github.com/giantswarm/mcp-toolkit/urlelicitation"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/live"
)

// SignInElicitor asks a tool's caller to open the sign-in muster asked for,
// sealing requestState for the person the forwarded ID token names.
type SignInElicitor = urlelicitation.Elicitor[*providers.UserInfo]

// relaySignIns runs a live tool's call on a live.Relay. A retry that answers
// a relayed sign-in with decline or cancel runs nothing; accept goes to
// muster's read on the retry. A call whose reads muster asked a sign-in for
// answers with that sign-in, as urlelicitation answers the caller's protocol
// revision, in place of a result whose checks were not read. Without
// SignIns, for a caller that does not declare URL mode elicitation, or for
// one whose forwarded ID token is not accepted, the call runs as it is and
// its not-read checks name muster's sign-in.
func (t *Tools) relaySignIns(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if t.d.SignIns == nil || !urlelicitation.SupportsURL(ctx) {
			return next(ctx, req)
		}
		user := t.signInUser(ctx)
		if user == nil {
			return next(ctx, req)
		}
		elicitor := t.d.SignIns
		answer, err := elicitor.Result(ctx, req, user)
		if err != nil {
			return nil, err
		}
		var retry *urlelicitation.Retry
		if answer != nil {
			if retry, err = answer.Retry(); err != nil {
				return nil, err
			}
			if retry.Action != mcp.ElicitationResponseActionAccept {
				return mcp.NewToolResultError(fmt.Sprintf("%s did not run: the sign-in muster asked for was answered with %s.", req.Params.Name, retry.Action)), nil
			}
		}
		relayCtx, relay := live.WithRelay(ctx, retry)
		res, err := next(relayCtx, req)
		signIn := relay.SignIn()
		if signIn == nil || err != nil {
			return res, err
		}
		relayed, relayErr := elicitor.Relay(ctx, req, user, signIn.Target, signIn.Relayed)
		if relayErr != nil && !errors.As(relayErr, new(mcp.URLElicitationRequiredError)) {
			t.d.Log.Warn("muster sign-in not relayed: answered in the result", identity.LogAttr(ctx), "tool", req.Params.Name, "error", relayErr)
			return res, nil
		}
		return relayed, relayErr
	}
}

// signInUser is the person the forwarded ID token names, as the request
// state is sealed for: its issuer, subject and client; nil without an
// accepted token.
func (t *Tools) signInUser(ctx context.Context) *providers.UserInfo {
	token, ok := identity.ForwardedTokenFromContext(ctx)
	if !ok || t.d.Live == nil {
		return nil
	}
	who, err := t.d.Live.Verify(ctx, token)
	if err != nil {
		return nil
	}
	return &providers.UserInfo{Issuer: who.Issuer, ID: who.Subject, ClientID: who.Client, Email: who.Email}
}
