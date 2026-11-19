package render

import (
	"net/url"
	"slices"
)

// ClientMetadata is a client's metadata document (CIMD) as the client serves
// it at its client_id URL, rendered for an authorization server that pins it
// (preRegisteredClientMetadata): a pinned client is never fetched, so its
// document's address does not matter. It carries no scope: an empty scope
// list lets the client ask for any scope the server supports, and a pinned
// list would refuse a scope the client's own configuration adds.
type ClientMetadata struct {
	ClientID     string
	ClientName   string
	ClientURI    string
	RedirectURIs []string
}

// Map is the pinned document with its JSON field names: a public client
// (token_endpoint_auth_method none) of the authorization code grant with
// refresh tokens.
func (c ClientMetadata) Map() Map {
	return Map{
		{Key: "client_id", Value: c.ClientID},
		{Key: "client_name", Value: c.ClientName},
		{Key: "client_uri", Value: c.ClientURI},
		{Key: "redirect_uris", Value: c.RedirectURIs},
		{Key: "grant_types", Value: []string{"authorization_code", "refresh_token"}},
		{Key: "response_types", Value: []string{"code"}},
		{Key: "token_endpoint_auth_method", Value: "none"},
	}
}

// RedirectHosts are the hostnames of the client's redirect URIs, each once,
// in order: the hosts an authorization server lists for redirect URIs that
// resolve to a private address.
func (c ClientMetadata) RedirectHosts() []string {
	var hosts []string
	for _, uri := range c.RedirectURIs {
		u, err := url.Parse(uri)
		if err != nil || u.Hostname() == "" || slices.Contains(hosts, u.Hostname()) {
			continue
		}
		hosts = append(hosts, u.Hostname())
	}
	return hosts
}
