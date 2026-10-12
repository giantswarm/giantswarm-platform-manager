package mcpservers

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func server(t *testing.T, name string) Server {
	t.Helper()
	i := slices.IndexFunc(Servers, func(s Server) bool { return s.Name == name })
	if i < 0 {
		t.Fatalf("no server %s", name)
	}
	return Servers[i]
}

// Every server reads its Valkey password from the valkey-auth Secret alone:
// the OAuth credentials Secret carries no copy of it, nor the generated
// value, so a Valkey password drawn anew rewrites that Secret only through
// the revision both share. mcp-prometheus's chart once loaded the copy with
// envFrom, overridden by its VALKEY_PASSWORD env entry: the key is retired,
// so a Secret on record that still holds it keeps it until its next rotation.
func TestOAuthSecretCarriesNoValkeyPassword(t *testing.T) {
	const inst = "x"
	for _, s := range Servers {
		var result render.Result
		s.Extras(&result, "repo", "extras/"+s.Name, Options{Installation: inst})
		files := result.Files["repo"]
		oauth, valkey := files["extras/"+s.Name+"/oauth-credentials.enc.yaml"], files["extras/"+s.Name+"/valkey-credentials.enc.yaml"]
		if strings.Contains(string(oauth.Content), "VALKEY_PASSWORD") || strings.Contains(string(oauth.Content), "valkey-password") {
			t.Errorf("%s: the OAuth Secret carries the Valkey password:\n%s", s.Name, oauth.Content)
		}
		password := inst + "-" + s.Name + "-valkey-password"
		if slices.ContainsFunc(oauth.Generated, func(g render.Generated) bool { return g.Name == password }) {
			t.Errorf("%s: the OAuth Secret holds %s", s.Name, password)
		}
		if !slices.ContainsFunc(valkey.Generated, func(g render.Generated) bool { return g.Name == password }) {
			t.Errorf("%s: the valkey-auth Secret does not hold %s", s.Name, password)
		}
		if got := result.Revisions[password]; got != inst+"-"+s.Name+"-credentials-revision" {
			t.Errorf("%s: %s rolls with revision %q", s.Name, password, got)
		}
		var want []string
		if s.Name == "mcp-prometheus" {
			want = []string{"stringData.VALKEY_PASSWORD"}
		}
		if !slices.Equal(oauth.Retired, want) {
			t.Errorf("%s: the OAuth Secret retires %v, want %v", s.Name, oauth.Retired, want)
		}
	}
}

// The revision reaches each server's pod template through the chart values
// its chart takes: the OAuth Secret's mark, and the valkey-auth Secret's
// where the chart has one. mcp-prometheus's chart has none, so a rotation
// of the Valkey password rolls it through the revision the OAuth Secret's
// mark carries.
func TestChecksumValues(t *testing.T) {
	for name, want := range map[string][]string{
		"mcp-kubernetes": {"mcpKubernetes.oauth.existingSecretChecksum", "mcpKubernetes.oauth.storage.valkey.existingSecretChecksum"},
		"mcp-prometheus": {"app.oauth.existingSecretChecksum"},
		"mcp-capi":       {"oauth.existingSecretChecksum", "oauth.storage.valkey.existingSecretChecksum"},
	} {
		if got := server(t, name).ChecksumValues(); !slices.Equal(got, want) {
			t.Errorf("%s: checksum values %v, want %v", name, got, want)
		}
	}
}
