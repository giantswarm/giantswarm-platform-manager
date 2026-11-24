package e2e

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/giantswarm/mcp-oauth/requeststate"
	"github.com/giantswarm/mcp-oauth/security"
	"github.com/giantswarm/mcp-toolkit/urlelicitation"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/identity"
	"github.com/giantswarm/giantswarm-platform-manager/internal/tools"
	"github.com/giantswarm/giantswarm-platform-manager/internal/verify"
)

// withSignIns relays muster's sign-ins with a key of the test's own.
func withSignIns(t *testing.T) func(*tools.Deps) {
	t.Helper()
	secret := make([]byte, 32)
	_, err := rand.Read(secret)
	require.NoError(t, err)
	ring, err := security.NewKeyRing(security.Key{ID: security.KeyID(secret), Secret: secret})
	require.NoError(t, err)
	sealer, err := requeststate.New(ring)
	require.NoError(t, err)
	elicitor, err := urlelicitation.New(sealer, urlelicitation.WithExpired(func(err error) bool {
		return errors.Is(err, requeststate.ErrExpired)
	}))
	require.NoError(t, err)
	return func(d *tools.Deps) { d.SignIns = elicitor }
}

// elicitingClient is liveClient for a caller that answers input requests
// by hand, declaring URL mode elicitation when url is set, at protocol.
func (st *stack) elicitingClient(t *testing.T, idToken string, url bool, protocol string) *client.Client {
	t.Helper()
	headers := map[string]string{"Authorization": "Bearer " + githubTokenOf[personOf(idToken)], identity.ForwardedIdentityHeader: idToken}
	tr, err := transport.NewStreamableHTTP(st.srv.URL+"/mcp", transport.WithHTTPHeaders(headers))
	require.NoError(t, err)
	opts := []client.ClientOption{client.WithManualInputRoundTrips(), client.WithDiscoverTimeout(-1)}
	if url {
		opts = append(opts, client.WithClientCapabilities(mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapability{URL: &struct{}{}}}))
	}
	c := client.NewClient(tr, opts...)
	require.NoError(t, c.Start(t.Context()))
	t.Cleanup(func() { _ = c.Close() })
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = protocol
	_, err = c.Initialize(t.Context(), init)
	require.NoError(t, err)
	return c
}

func verifyAnswering(t *testing.T, c *client.Client, roundTrip mcp.MultiRoundTripParams) (*mcp.CallToolResult, error) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = tools.ToolVerifyInstallation
	req.Params.Arguments = map[string]any{tools.ArgInstallation: rowan}
	req.Params.MultiRoundTripParams = roundTrip
	return c.CallTool(t.Context(), req)
}

func relayAnswer(state string, action mcp.ElicitationResponseAction) mcp.MultiRoundTripParams {
	return mcp.MultiRoundTripParams{
		RequestState: state,
		InputResponses: mcp.InputResponses{urlelicitation.RelayKey: mcp.NewElicitationInputResponse(mcp.ElicitationResult{
			ElicitationResponse: mcp.ElicitationResponse{Action: action},
		})},
	}
}

func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := mcp.AsTextContent(c); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// relayStack is a stack whose rowan the admin enabled, so its live
// dimensions have inputs on record to read.
func relayStack(t *testing.T) *stack {
	t.Helper()
	st := newStack(t, withSignIns(t))
	fixtures(st.ghs)
	enableRowanLive(t, st, st.mcpClient(t, aliceToken), kagentEnabled())
	return st
}

// A person not connected in muster gets muster's sign-in as URL elicitation
// in place of a result with checks not read; the retry that accepts goes to
// muster as the read it asked for, with muster's own requestState, and the
// verify reads as them.
func TestVerifyInstallationRelaysMustersSignIn(t *testing.T) {
	st := relayStack(t)
	stranger := st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour)
	c := st.elicitingClient(t, stranger, true, mcp.LATEST_PROTOCOL_VERSION)

	res, err := verifyAnswering(t, c, mcp.MultiRoundTripParams{})
	require.NoError(t, err)
	require.True(t, res.NeedsInput(), resultText(res))
	require.Len(t, res.InputRequests, 1)
	asked := res.InputRequests[urlelicitation.RelayKey].Elicitation
	require.NotNil(t, asked)
	require.Equal(t, mcp.ElicitationModeURL, asked.Mode)
	require.Equal(t, musterSignInURL, asked.URL)
	require.NotEqual(t, musterSignInState, res.RequestState)
	first := st.muster.asked()[0]
	require.Empty(t, first.RequestState)

	res, err = verifyAnswering(t, c, relayAnswer(res.RequestState, mcp.ElicitationResponseActionAccept))
	require.NoError(t, err)
	require.False(t, res.NeedsInput())
	require.False(t, res.IsError, resultText(res))
	var out verify.Result
	require.NoError(t, json.Unmarshal([]byte(resultText(res)), &out))
	d := liveDimensions(out)["live-helmreleases-ready"]
	require.True(t, d.Live == nil || d.Live.AuthRequired == nil, "%+v", d)

	var answered []musterSignIn
	for _, call := range st.muster.asked() {
		if call.Answer != "" {
			answered = append(answered, call)
		}
	}
	require.Len(t, answered, 1)
	require.Equal(t, mcp.ElicitationResponseActionAccept, answered[0].Answer)
	require.Equal(t, musterSignInState, answered[0].RequestState)
	require.Equal(t, first.Tool, answered[0].Tool)
	require.Equal(t, first.Args, answered[0].Args)
}

func TestVerifyInstallationDeclinedSignInReadsNothing(t *testing.T) {
	for _, action := range []mcp.ElicitationResponseAction{mcp.ElicitationResponseActionDecline, mcp.ElicitationResponseActionCancel} {
		t.Run(string(action), func(t *testing.T) {
			st := relayStack(t)
			c := st.elicitingClient(t, st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour), true, mcp.LATEST_PROTOCOL_VERSION)

			res, err := verifyAnswering(t, c, mcp.MultiRoundTripParams{})
			require.NoError(t, err)
			require.True(t, res.NeedsInput())
			reads := len(st.muster.seen())

			res, err = verifyAnswering(t, c, relayAnswer(res.RequestState, action))
			require.NoError(t, err)
			require.True(t, res.IsError)
			require.Contains(t, resultText(res), "did not run")
			require.Len(t, st.muster.seen(), reads)
		})
	}
}

func TestVerifyInstallationRequestStateIsBoundToThePerson(t *testing.T) {
	st := relayStack(t)
	c := st.elicitingClient(t, st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour), true, mcp.LATEST_PROTOCOL_VERSION)
	res, err := verifyAnswering(t, c, mcp.MultiRoundTripParams{})
	require.NoError(t, err)
	require.True(t, res.NeedsInput())
	state := res.RequestState
	reads := len(st.muster.seen())

	t.Run("tampered", func(t *testing.T) {
		_, err := verifyAnswering(t, c, relayAnswer(state[:len(state)-2]+"AA", mcp.ElicitationResponseActionAccept))
		require.ErrorContains(t, err, "invalid requestState")
	})
	t.Run("another person", func(t *testing.T) {
		viewer := st.elicitingClient(t, st.dex.token(t, liveViewer, []string{liveAudience}, time.Hour), true, mcp.LATEST_PROTOCOL_VERSION)
		_, err := verifyAnswering(t, viewer, relayAnswer(state, mcp.ElicitationResponseActionAccept))
		require.ErrorContains(t, err, "invalid requestState")
	})
	require.Len(t, st.muster.seen(), reads)
}

// A caller without URL mode elicitation gets the result with its checks not
// read, each naming muster's sign-in, as without a key.
func TestVerifyInstallationWithoutURLElicitationNamesTheSignIn(t *testing.T) {
	st := relayStack(t)
	c := st.elicitingClient(t, st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour), false, mcp.LATEST_PROTOCOL_VERSION)

	res, err := verifyAnswering(t, c, mcp.MultiRoundTripParams{})
	require.NoError(t, err)
	require.False(t, res.NeedsInput())
	require.False(t, res.IsError, resultText(res))
	var out verify.Result
	require.NoError(t, json.Unmarshal([]byte(resultText(res)), &out))
	d := liveDimensions(out)["live-helmreleases-ready"]
	require.NotNil(t, d.Live)
	require.NotNil(t, d.Live.AuthRequired)
	require.True(t, strings.HasPrefix(d.Live.AuthRequired.URL, "https://muster.example.test/oauth/proxy/start"))
	require.Empty(t, st.muster.asked())
}

func TestVerifyInstallationLegacyURLCallerGetsURLElicitationRequired(t *testing.T) {
	st := relayStack(t)
	c := st.elicitingClient(t, st.dex.token(t, liveStranger, []string{liveAudience}, time.Hour), true, mcp.LATEST_LEGACY_PROTOCOL_VERSION)

	_, err := verifyAnswering(t, c, mcp.MultiRoundTripParams{})
	var required mcp.URLElicitationRequiredError
	require.ErrorAs(t, err, &required)
	require.Len(t, required.Elicitations, 1)
	require.Equal(t, musterSignInURL, required.Elicitations[0].URL)
}
