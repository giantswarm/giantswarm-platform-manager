package render

import (
	"slices"
	"testing"
)

// The redirect hosts are each redirect URI's hostname once, in order, with
// no port: the exact names an authorization server lists.
func TestClientMetadataRedirectHosts(t *testing.T) {
	c := ClientMetadata{RedirectURIs: []string{
		"https://a.example.io/callback",
		"https://b.example.io:8443/callback",
		"https://a.example.io/other",
	}}
	if got, want := c.RedirectHosts(), []string{"a.example.io", "b.example.io"}; !slices.Equal(got, want) {
		t.Errorf("redirect hosts %v, want %v", got, want)
	}
}
