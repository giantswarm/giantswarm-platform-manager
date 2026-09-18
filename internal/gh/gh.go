// Package gh is the GitHub client of the manager: every call runs with the
// person's own user token — the bearer muster put on the request. The server
// holds no token of its own.
package gh

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
)

// ErrNotFound is a path or repository GitHub answered 404 for as the person:
// the file is absent, or the repository is one the person (or the App) may
// not see — GitHub does not tell the two apart.
var ErrNotFound = errors.New("not found")

// ErrForbidden is a repository GitHub answered 403 for as the person: the
// person may not read it, or the App is not installed on it.
var ErrForbidden = errors.New("forbidden")

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

// ReadFile is the content of path on the default branch of owner/repo, read
// as the person. An absent file or an invisible repository is ErrNotFound, a
// refused one ErrForbidden; both keep GitHub's *github.ErrorResponse in the
// chain. A directory at path is an error: the manager reads files.
func ReadFile(ctx context.Context, c *github.Client, owner, repo, path string) (string, error) {
	file, dir, _, err := c.Repositories.GetContents(ctx, owner, repo, path, nil)
	if err != nil {
		return "", fmt.Errorf("github: %s/%s:%s: %w", owner, repo, path, classify(err))
	}
	if file == nil || dir != nil {
		return "", fmt.Errorf("github: %s/%s:%s is a directory, not a file", owner, repo, path)
	}
	content, err := file.GetContent()
	if err != nil {
		return "", fmt.Errorf("github: %s/%s:%s: decode content: %w", owner, repo, path, err)
	}
	return content, nil
}

// classify wraps GitHub's 404 and 403 in the sentinels callers branch on.
func classify(err error) error {
	var er *github.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		switch er.Response.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrForbidden, err)
		}
	}
	return err
}

// SplitRepo splits "owner/repo" into its two parts.
func SplitRepo(full string) (owner, repo string, err error) {
	owner, repo, ok := strings.Cut(strings.TrimSpace(full), "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", fmt.Errorf("repository %q is not owner/repo", full)
	}
	return owner, repo, nil
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
