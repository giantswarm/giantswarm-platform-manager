package main

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

// --live-allow-private-ip-jwks is still accepted, from the flag and from the
// environment, and says at start-up that it has no effect.
func TestLiveAllowPrivateIPJWKSIsAcceptedAndWarns(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T) []string{
		"flag": func(*testing.T) []string { return []string{"--live-allow-private-ip-jwks"} },
		"env": func(t *testing.T) []string {
			t.Setenv("LIVE_ALLOW_PRIVATE_IP_JWKS", "true")
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, err := parseFlags(setup(t))
			require.NoError(t, err)
			require.True(t, o.liveAllowPrivateIPJWKS)

			var buf bytes.Buffer
			warnDeprecated(o, slog.New(slog.NewTextHandler(&buf, nil)))
			require.Contains(t, buf.String(), "level=WARN")
			require.Contains(t, buf.String(), "--live-allow-private-ip-jwks (LIVE_ALLOW_PRIVATE_IP_JWKS) has no effect")
		})
	}
}

func TestNoDeprecationWarningByDefault(t *testing.T) {
	t.Setenv("LIVE_ALLOW_PRIVATE_IP_JWKS", "")
	o, err := parseFlags(nil)
	require.NoError(t, err)

	var buf bytes.Buffer
	warnDeprecated(o, slog.New(slog.NewTextHandler(&buf, nil)))
	require.Empty(t, buf.String())
}
