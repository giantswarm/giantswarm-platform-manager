package plan

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A Dex client secret on record stands whatever shape it was drawn in: the
// declaration's kind and length are not in the file, so the Dex client's
// Secret and the workload's credentials, both on record encrypted, compare
// unchanged, and the value is kept, neither rotated nor carried.
func TestBuildKeepsADexClientSecretOnRecord(t *testing.T) {
	const name = "rowan-server-dex-client-secret" // #nosec G101 -- a generated value's name, not a value
	rendered := map[string]render.File{
		"server/dex-client-server-secret.yaml": render.DexClientSecret("server", name),
		"server/oauth-credentials.enc.yaml":    render.Secret("server-oauth-credentials", "server", nil, render.DexClientSecretKey("dex-client-secret", name)),
	}
	def := installations.Capability{
		Name:  "dex-secret",
		Parse: func(any) (render.Input, error) { return fakeInput{}, nil },
		Render: func(any, map[string]string, render.Mode) (*render.Result, error) {
			return &render.Result{Files: render.Fileset{renderedConfigs: rendered}}, nil
		},
	}
	onRecord := map[string]string{}
	for path, f := range rendered {
		onRecord[path] = strings.ReplaceAll(string(f.Content), render.Placeholder(name), "ENC[AES256_GCM,data:fixture,iv:fixture,tag:fixture,type:str]") +
			"sops:\n  age:\n    - recipient: age1fixture\n  encrypted_regex: ^(data|stringData)$\n  version: 3.9.0\n"
	}
	read := func(_ context.Context, _, path string) (string, error) { return onRecord[path], nil }

	p := Build(t.Context(), Options{Definition: def, Installation: rowanInstallation(), Inputs: map[string]any{}, Read: read})

	require.Len(t, p.Files, len(rendered))
	for _, f := range p.Files {
		require.Equal(t, ChangeUnchanged, f.Change, f.Path)
		require.False(t, f.Rewritten, f.Path)
	}
	require.Len(t, p.GeneratedSecrets, 1)
	g := p.GeneratedSecrets[0]
	require.Equal(t, string(render.Alphanumeric), g.Kind)
	require.Equal(t, 43, g.Length)
	require.True(t, g.Kept)
	require.False(t, g.Rotates)
	require.Empty(t, g.Carries)
	require.Empty(t, p.FrozenRefusal())
	require.Empty(t, p.CarryRefusal())
}
