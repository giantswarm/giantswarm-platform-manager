// Package gh is the GitHub client of the manager: every call runs with the
// person's own user token — the bearer muster put on the request. The server
// holds no token of its own.
package gh

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v92/github"
)

// AsPerson returns a client that calls GitHub with the person's user token.
func AsPerson(apiURL, accessToken string) (*github.Client, error) {
	return newClient(apiURL, github.WithAuthToken(accessToken))
}

// User is the person the token belongs to (GET /user): the call that verifies
// a bearer. A refusal keeps GitHub's *github.ErrorResponse in the chain.
func User(ctx context.Context, apiURL, accessToken string) (login string, id int64, err error) {
	c, err := AsPerson(apiURL, accessToken)
	if err != nil {
		return "", 0, err
	}
	u, _, err := c.Users.Get(ctx, "")
	if err != nil {
		return "", 0, fmt.Errorf("github: GET /user as the person: %w", err)
	}
	return u.GetLogin(), u.GetID(), nil
}

// newClient builds a go-github client with a 15 s timeout, against apiURL
// when set (GitHub Enterprise shape; the fake in tests).
func newClient(apiURL string, opts ...github.ClientOptionsFunc) (*github.Client, error) {
	opts = append(opts, github.WithTimeout(15*time.Second))
	if apiURL != "" {
		opts = append(opts, github.WithEnterpriseURLs(apiURL, apiURL))
	}
	c, err := github.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("github: client for %q: %w", apiURL, err)
	}
	return c, nil
}
