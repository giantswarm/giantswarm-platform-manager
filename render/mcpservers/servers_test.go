package mcpservers

import (
	"slices"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const mcpPrometheus = "mcp-prometheus"

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
		if s.Name == mcpPrometheus {
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
		mcpPrometheus:    {"app.oauth.existingSecretChecksum"},
		"mcp-capi":       {"oauth.existingSecretChecksum", "oauth.storage.valkey.existingSecretChecksum"},
	} {
		if got := server(t, name).ChecksumValues(); !slices.Equal(got, want) {
			t.Errorf("%s: checksum values %v, want %v", name, got, want)
		}
	}
}

// A client on a private address is pinned by mcp-kubernetes alone, the
// chart that takes pins: its document under oauth.cimd and its redirect host
// under oauth.redirectURISecurity, with no private-address flag. The other
// servers render no user values for it, nor does a server without one.
func TestPrivateClientUserValues(t *testing.T) {
	client := &render.ClientMetadata{ClientID: "https://muster.x.example.io/.well-known/oauth-client.json", ClientName: "Muster MCP Aggregator",
		ClientURI: "https://github.com/giantswarm/muster", RedirectURIs: []string{"https://muster.x.example.io/oauth/proxy/callback"}}
	const want = `mcpKubernetes:
  oauth:
    cimd:
      preRegisteredClientMetadata:
        - client_id: https://muster.x.example.io/.well-known/oauth-client.json
          client_name: Muster MCP Aggregator
          client_uri: https://github.com/giantswarm/muster
          redirect_uris:
            - https://muster.x.example.io/oauth/proxy/callback
          grant_types:
            - authorization_code
            - refresh_token
          response_types:
            - code
          token_endpoint_auth_method: none
    redirectURISecurity:
      privateIPHosts:
        - muster.x.example.io
`
	for _, s := range Servers {
		for _, o := range []Options{{Installation: "x", PrivateClient: client}, {Installation: "x"}} {
			var result render.Result
			s.Extras(&result, "repo", "extras/"+s.Name, o)
			f, ok := result.Files["repo"]["extras/"+s.Name+"/user-values.yaml"]
			if s.Name == "mcp-kubernetes" && o.PrivateClient != nil {
				if got := string(f.Content); got != want {
					t.Errorf("%s: user values\n%s\nwant\n%s", s.Name, got, want)
				}
				continue
			}
			if ok {
				t.Errorf("%s (client %v): user values rendered:\n%s", s.Name, o.PrivateClient != nil, f.Content)
			}
		}
	}
}
