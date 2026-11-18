package agentplatform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// dexClientSecretNames are the generated values r declares in a Dex client
// Secret (dex-client-<client> in Dex's namespace) or in a hub's
// token-exchange credentials, whose Dex side the target renders.
func dexClientSecretNames(r *render.Result) map[string]bool {
	names := map[string]bool{}
	for _, files := range r.Files {
		for _, f := range files {
			content := string(f.Content)
			dexSide := strings.Contains(content, "  name: dex-client-") && strings.Contains(content, "  namespace: "+dexNamespace+"\n")
			if !dexSide && !strings.Contains(content, "muster.giantswarm.io/type: token-exchange-credentials") {
				continue
			}
			for _, g := range f.Generated {
				names[g.Name] = true
			}
		}
	}
	return names
}

// Every Dex client secret of every shape, on the Dex side and the
// workload's alike, is 43 alphanumeric characters: a client that skips
// form-encoding client_secret_basic presents it as Dex decodes it.
func TestDexClientSecretsAreAlphanumeric(t *testing.T) {
	for _, shape := range shapes {
		t.Run(shape, func(t *testing.T) {
			input, secrets := loadInput(t, shape)
			r, err := Render(input, secrets, render.ModeCommit)
			require.NoError(t, err)
			names := dexClientSecretNames(r)
			require.NotEmpty(t, names)
			for _, files := range r.Files {
				for path, f := range files {
					for _, g := range f.Generated {
						if !names[g.Name] {
							continue
						}
						require.Equalf(t, render.Alphanumeric, g.Kind, "%s in %s", g.Name, path)
						require.Equalf(t, 43, g.Length, "%s in %s", g.Name, path)
					}
				}
			}
		})
	}
}

// A confidential client's Secret, registered anew or kept by hand under
// mcpclients/, declares its value as a Dex client secret.
func TestConfidentialClientSecretIsAlphanumeric(t *testing.T) {
	r, err := renderConfidential(t, nil)
	require.NoError(t, err)
	for _, client := range []string{freshClient, adoptedClient} {
		name := "heron-" + client + "-dex-client-secret"
		var found bool
		for _, files := range r.Files {
			for _, f := range files {
				for _, g := range f.Generated {
					if g.Name != name {
						continue
					}
					found = true
					require.Equal(t, render.Alphanumeric, g.Kind, name)
					require.Equal(t, 43, g.Length, name)
				}
			}
		}
		require.Truef(t, found, "no declaration of %s", name)
	}
}
