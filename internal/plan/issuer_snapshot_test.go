package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

const issuerPatch = `muster:
  muster:
    oauth:
      server:
        trustedIssuers:
          - issuer: https://dex.example.test
            audiences: [muster]
            acceptedTypHeaders: [JWT]
`

func TestTrustedIssuerSnapshotCapturesTheCompleteConfiguration(t *testing.T) {
	snapshot, err := TrustedIssuerSnapshot(issuerPatch)
	require.NoError(t, err)
	require.Len(t, snapshot, 1)
	require.Equal(t, ListTrustedIssuers, snapshot[0].List)
	require.Regexp(t, `^sha256:[a-f0-9]{64}$`, snapshot[0].Digest)
	for _, content := range []string{
		strings.ReplaceAll(issuerPatch, "audiences: [muster]", "allowPrivateIPJWKS: true\n            audiences: [muster]"),
		strings.ReplaceAll(issuerPatch, "[JWT]", "[at+jwt]"),
		issuerPatch + "          - issuer: https://other.example.test\n",
	} {
		other, err := TrustedIssuerSnapshot(content)
		require.NoError(t, err)
		require.NotEqual(t, snapshot, other)
	}
	reordered := strings.ReplaceAll(issuerPatch, "issuer: https://dex.example.test\n            audiences: [muster]", "audiences: [muster]\n            issuer: https://dex.example.test")
	other, err := TrustedIssuerSnapshot(reordered)
	require.NoError(t, err)
	require.Equal(t, snapshot, other)
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "https://dex.example.test")
	require.NotContains(t, string(encoded), "acceptedTypHeaders")

	for _, content := range []string{"", "muster: {}", strings.ReplaceAll(issuerPatch, "\n          - issuer: https://dex.example.test\n            audiences: [muster]\n            acceptedTypHeaders: [JWT]", " null")} {
		got, err := TrustedIssuerSnapshot(content)
		require.NoError(t, err)
		require.Empty(t, got)
	}
	_, err = TrustedIssuerSnapshot("muster: [")
	require.Error(t, err)
}

func TestPlanRecordsMergedIssuersWithoutReturningFileContent(t *testing.T) {
	const path = "installations/rowan/apps/agent-platform/configmap-values.yaml.patch"
	current := strings.ReplaceAll(issuerPatch, "[JWT]", "[at+jwt]")
	installation := Build(t.Context(), Options{
		Definition: filesDefinition(map[string]string{path: issuerPatch}), Installation: rowanInstallation(), Inputs: map[string]any{},
		Read: func(_ context.Context, _, file string) (string, error) {
			switch file {
			case path:
				return current, nil
			case rowanKustomization:
				return "resources: []\n", nil
			}
			return "", gh.ErrNotFound
		},
	})
	require.Empty(t, installation.Refused)
	want, err := TrustedIssuerSnapshot(current)
	require.NoError(t, err)
	require.Equal(t, want, installation.LiveKept())
	for _, file := range installation.Files {
		require.Empty(t, file.Content)
	}
}
