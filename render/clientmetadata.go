package render

// ClientMetadata is a public OAuth client's pinned metadata document.
type ClientMetadata struct {
	ClientID     string
	ClientName   string
	ClientURI    string
	RedirectURIs []string
}

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
