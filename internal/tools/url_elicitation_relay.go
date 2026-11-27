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

// URLElicitor relays URL elicitations with state bound to a validated caller.
type URLElicitor = urlelicitation.Elicitor[*providers.UserInfo]

type invalidRequestState struct{ reason string }

func (e invalidRequestState) Error() string { return e.reason }

func (e invalidRequestState) JSONRPCError() mcp.JSONRPCError {
	return mcp.JSONRPCError{
		JSONRPC: mcp.JSONRPC_VERSION,
		Error: mcp.JSONRPCErrorDetails{
			Code: mcp.INVALID_PARAMS, Message: e.Error(),
		},
	}
}

// rejectUnsupportedRequestState keeps continuations on tools that validate them.
func rejectUnsupportedRequestState(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if request.Params.RequestState != "" {
			switch request.Params.Name {
			case ToolVerifyInstallation, ToolMergeAction, ToolWatchAction:
			default:
				return nil, mcpserver.NewHandlerError(invalidRequestState{reason: "requestState is not supported by this tool"})
			}
		}
		return next(ctx, request)
	}
}

// relayURLElicitations validates continuations before handlers and relays the
// first pending URL interaction. Decline and cancel never run the handler.
func (t *Tools) relayURLElicitations(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if t.d.URLElicitations == nil {
			if request.Params.RequestState != "" {
				return nil, mcpserver.NewHandlerError(invalidRequestState{reason: "requestState cannot be verified: URL elicitation relay is disabled"})
			}
			return next(ctx, request)
		}
		user := t.relayCaller(ctx)
		elicitor := t.d.URLElicitations
		answer, err := elicitor.Result(ctx, request, user)
		if err != nil {
			return nil, err
		}
		if user == nil {
			return next(ctx, request)
		}
		var retry *urlelicitation.Retry
		if answer != nil {
			if retry, err = answer.Retry(); err != nil {
				return nil, err
			}
			if retry.Action != mcp.ElicitationResponseActionAccept {
				return mcp.NewToolResultError(fmt.Sprintf("%s did not run: the URL interaction muster requested was answered with %s.", request.Params.Name, retry.Action)), nil
			}
		}
		if request.Params.RequestState == "" && !urlelicitation.SupportsURL(ctx) {
			return next(ctx, request)
		}
		relayCtx, relay := live.WithRelay(ctx, retry)
		result, err := next(relayCtx, request)
		elicitation := relay.Elicitation()
		if elicitation == nil || err != nil {
			return result, err
		}
		relayed, relayErr := elicitor.Relay(ctx, request, user, elicitation.Target, elicitation.Relayed)
		if relayErr != nil && !errors.As(relayErr, new(mcp.URLElicitationRequiredError)) {
			t.d.Log.Warn("URL elicitation relay failed", identity.LogAttr(ctx), "tool", request.Params.Name, "error", relayErr)
			return nil, relayErr
		}
		return relayed, relayErr
	}
}

// relayCaller is the person the forwarded ID token names, as the request
// state is sealed for: its issuer, subject and client; nil without an
// accepted token.
func (t *Tools) relayCaller(ctx context.Context) *providers.UserInfo {
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
